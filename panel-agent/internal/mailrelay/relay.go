// Package mailrelay sends the sites' PHP mail() through the operator's
// smarthost (GH #2056, ADR 0174).
//
// The jabali-sendmail shim runs as the site's user, so it can't hold the
// smarthost login: anything it can read, the site's PHP can read too. In
// smarthost mode the shim hands the message to this relay over a unix socket
// instead. The relay runs as `jabali-agent mailrelay` under its own system
// user, holds the login, and decides the envelope sender itself from the
// caller's UID (SO_PEERCRED): noreply@ one of that user's own domains. The
// smarthost can't tell which site sent a message, so the relay also keeps the
// From header to one address in one of those domains (RestrictFrom).
//
// Wire protocol, one message per connection:
//
//	client: {"recipients":["a@example.org"],"size":1234}\n then size bytes
//	relay:  {"code":0}\n  or  {"code":75,"error":"..."}\n
//
// code is a sysexits value the shim exits with (0 sent, 75 try again later,
// 65 refused by the smarthost, 77 not allowed, 78 not configured).
package mailrelay

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/smarthost"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/sendmailshim"
)

// Paths on a box.
const (
	// SocketPath is where the relay listens (systemd RuntimeDirectory).
	SocketPath = "/run/jabali-mailrelay/relay.sock"
	// ConfigPath holds the smarthost and the sender list, written by the
	// agent (0640 root:jabali-mailrelay).
	ConfigPath = "/etc/jabali-panel/mailrelay/relay.json"
	// ModePath tells the shim where website mail goes: "smarthost" or
	// "local". It sits in the shim's credential tree, which the site users
	// can traverse (0711), and is world-readable.
	ModePath = "/etc/jabali-panel/sendmail/website-mail.mode"
)

// Mode values in ModePath.
const (
	ModeLocal     = "local"
	ModeSmarthost = "smarthost"
)

const (
	maxHeaderLine      = 64 << 10
	maxRecipients      = 100
	connTimeout        = 2 * time.Minute
	defaultMaxConc     = 8
	defaultMaxPerUser  = 2
	defaultReadTimeout = 30 * time.Second
)

// Config is relay.json.
type Config struct {
	Smarthost Smarthost `json:"smarthost"`
	// Senders maps a UID (decimal) to the domains that user may send from.
	Senders map[string]Sender `json:"senders"`
}

// Smarthost is the relay's login to the operator's smarthost.
type Smarthost struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	TLS      string `json:"tls"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Helo     string `json:"helo,omitempty"`
}

// SmarthostConfig converts to the shared client's config.
func (s Smarthost) SmarthostConfig() smarthost.Config {
	return smarthost.Config{Host: s.Host, Port: s.Port, TLS: s.TLS, Username: s.Username, Password: s.Password, HeloName: s.Helo}
}

// Sender is one site user allowed to send.
type Sender struct {
	User    string   `json:"user"`
	Domains []string `json:"domains"`
	// Default is the domain used when the From header names none of Domains.
	Default string `json:"default"`
}

// Request is the first line a client sends.
type Request struct {
	Recipients []string `json:"recipients"`
	Size       int      `json:"size"`
}

// Reply is the relay's answer.
type Reply struct {
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

// Server is the relay.
type Server struct {
	ConfigPath string
	Log        *slog.Logger
	// Send overrides smarthost.Send in tests.
	Send func(ctx context.Context, c smarthost.Config, from string, to []string, msg []byte) error
	// PeerUID overrides the SO_PEERCRED lookup in tests.
	PeerUID func(net.Conn) (uint32, error)
	// MaxConcurrent bounds the messages being sent at once (default 8).
	MaxConcurrent int
	// MaxPerUser bounds one account's connections at once (default 2), so
	// one site can't hold the relay for everyone else.
	MaxPerUser int
	// ReadTimeout bounds receiving the request and the message (default 30s).
	ReadTimeout time.Duration

	once     sync.Once
	slots    chan struct{}
	mu       sync.Mutex
	inFlight map[uint32]int
}

func (s *Server) init() {
	s.once.Do(func() {
		n := s.MaxConcurrent
		if n <= 0 {
			n = defaultMaxConc
		}
		s.slots = make(chan struct{}, n)
		s.inFlight = map[uint32]int{}
		if s.MaxPerUser <= 0 {
			s.MaxPerUser = defaultMaxPerUser
		}
		if s.ReadTimeout <= 0 {
			s.ReadTimeout = defaultReadTimeout
		}
	})
}

// Serve accepts connections until ctx ends or the listener closes. Every
// connection gets its own goroutine: a sending slot is only taken once the
// caller is known and its whole message has arrived.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.init()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			// Out of file descriptors and the like: back off, keep serving.
			s.log().Warn("mail relay accept failed", "err", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go s.Handle(ctx, conn)
	}
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// claim counts one more connection for uid; false when it has too many.
func (s *Server) claim(uid uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight[uid] >= s.MaxPerUser {
		return false
	}
	s.inFlight[uid]++
	return true
}

func (s *Server) release(uid uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight[uid] <= 1 {
		delete(s.inFlight, uid)
		return
	}
	s.inFlight[uid]--
}

// Handle serves one connection: one message.
func (s *Server) Handle(ctx context.Context, conn net.Conn) {
	s.init()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(s.ReadTimeout))
	ctx, cancel := context.WithTimeout(ctx, connTimeout)
	defer cancel()

	res := s.relayOne(ctx, conn)
	attrs := []any{"uid", res.uid, "user", res.user, "from", res.from, "rcpts", res.rcpts, "code", res.reply.Code}
	if res.reason != nil {
		s.log().Warn("website mail not sent", append(attrs, "reason", res.reason.Error())...)
	} else {
		s.log().Info("website mail sent", attrs...)
	}
	b, _ := json.Marshal(res.reply)
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, _ = conn.Write(append(b, '\n'))
}

// result is one handled message, for the log line.
type result struct {
	uid    string
	user   string
	from   string
	rcpts  int
	reply  Reply
	reason error
}

func refuse(res result, code int, err error) result {
	res.reply = Reply{Code: code, Error: err.Error()}
	res.reason = err
	return res
}

// readCode is the exit code for a failed read: a caller too slow to send its
// message can try again, anything else is a bad request.
func readCode(err error) int {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return sendmailshim.ExitTempFail
	}
	return sendmailshim.ExitDataErr
}

func (s *Server) relayOne(ctx context.Context, conn net.Conn) result {
	var res result
	peer := s.PeerUID
	if peer == nil {
		peer = peerUID
	}
	uid, err := peer(conn)
	if err != nil {
		return refuse(res, sendmailshim.ExitNoPerm, fmt.Errorf("can't identify the caller: %w", err))
	}
	res.uid = strconv.FormatUint(uint64(uid), 10)
	if !s.claim(uid) {
		return refuse(res, sendmailshim.ExitTempFail, errors.New("too many messages at once from this account"))
	}
	defer s.release(uid)

	cfg, err := LoadConfig(s.ConfigPath)
	if err != nil {
		return refuse(res, sendmailshim.ExitConfig, fmt.Errorf("the smarthost relay isn't configured: %w", err))
	}
	sender, ok := cfg.Senders[res.uid]
	if !ok {
		return refuse(res, sendmailshim.ExitNoPerm, errors.New("this account may not send email through the smarthost"))
	}
	res.user = sender.User

	r := bufio.NewReaderSize(conn, 32<<10)
	req, err := readRequest(r)
	if err != nil {
		return refuse(res, readCode(err), err)
	}
	rcpts, err := cleanRecipients(req.Recipients)
	if err != nil {
		return refuse(res, sendmailshim.ExitDataErr, err)
	}
	res.rcpts = len(rcpts)
	// Memory follows the bytes that actually arrive, not the declared size.
	raw, err := io.ReadAll(io.LimitReader(r, int64(req.Size)))
	if err != nil {
		return refuse(res, readCode(err), fmt.Errorf("read message: %w", err))
	}
	if len(raw) != req.Size {
		return refuse(res, sendmailshim.ExitDataErr, fmt.Errorf("message ended after %d of %d bytes", len(raw), req.Size))
	}

	msg, err := sendmailshim.ParseMessage(bytes.NewReader(bareCRToLF(raw)), false)
	if err != nil {
		return refuse(res, sendmailshim.ExitCode(err), err)
	}
	domain := sender.pick(msg.FromDomain)
	if domain == "" {
		return refuse(res, sendmailshim.ExitConfig, errors.New("this account has no domain to send from"))
	}
	res.from = "noreply@" + domain
	msg = sendmailshim.RestrictFrom(msg, sender.owns, res.from)
	body := sendmailshim.AddMissingHeaders(sendmailshim.EnsureSender(msg, res.from), domain, time.Now())

	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return refuse(res, sendmailshim.ExitTempFail, errors.New("the relay is busy"))
	}
	_ = conn.SetDeadline(time.Now().Add(connTimeout))

	send := s.Send
	if send == nil {
		send = smarthost.Send
	}
	if err := send(ctx, cfg.Smarthost.SmarthostConfig(), res.from, rcpts, body); err != nil {
		return refuse(res, sendCode(err), err)
	}
	res.reply = Reply{Code: sendmailshim.ExitOK}
	return res
}

// bareCRToLF turns every CR that doesn't start a CRLF into a line break, so
// the relay's header parsing, the SMTP client's dot-stuffing and the
// smarthost all see the same lines: a bare CR can't hide a header from the
// From check, or end the message early at a smarthost that reads it as a line
// break and run what follows as commands in the operator's session.
func bareCRToLF(raw []byte) []byte {
	if !bytes.Contains(raw, []byte{'\r'}) {
		return raw
	}
	out := make([]byte, 0, len(raw))
	for i, c := range raw {
		if c == '\r' && (i+1 >= len(raw) || raw[i+1] != '\n') {
			out = append(out, '\n')
			continue
		}
		out = append(out, c)
	}
	return out
}

// owns reports whether domain is one of the user's.
func (s Sender) owns(domain string) bool {
	for _, d := range s.Domains {
		if strings.EqualFold(d, domain) {
			return true
		}
	}
	return false
}

// pick is the sending domain: the From domain when it is one of the user's,
// else the default.
func (s Sender) pick(fromDomain string) string {
	for _, d := range s.Domains {
		if strings.EqualFold(d, fromDomain) {
			return strings.ToLower(d)
		}
	}
	return strings.ToLower(s.Default)
}

// sendCode maps a send failure to the shim's exit code: a bad smarthost
// setting or a refused login is the operator's to fix, a message the
// smarthost refuses (5xx) is permanent, everything else can be tried again.
func sendCode(err error) int {
	var se *smarthost.Error
	if errors.As(err, &se) && (se.Stage == smarthost.StageConfig || se.Stage == smarthost.StageAuth) {
		return sendmailshim.ExitConfig
	}
	var tp *textproto.Error
	if errors.As(err, &tp) && tp.Code >= 500 {
		return sendmailshim.ExitDataErr
	}
	return sendmailshim.ExitTempFail
}

func readRequest(r *bufio.Reader) (Request, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			return Request{}, fmt.Errorf("read request: %w", err)
		}
		line = append(line, chunk...)
		if len(line) > maxHeaderLine {
			return Request{}, errors.New("request line too long")
		}
		if !isPrefix {
			break
		}
	}
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return Request{}, fmt.Errorf("bad request: %w", err)
	}
	if req.Size <= 0 || req.Size > sendmailshim.MaxMessageBytes {
		return Request{}, fmt.Errorf("message size must be 1 to %d bytes", sendmailshim.MaxMessageBytes)
	}
	return req, nil
}

// cleanRecipients keeps bare addresses, drops duplicates and refuses
// anything that isn't an address.
func cleanRecipients(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		a, err := mail.ParseAddress(r)
		if err != nil || strings.ContainsAny(a.Address, "\r\n<>") {
			return nil, fmt.Errorf("bad recipient %q", r)
		}
		key := strings.ToLower(a.Address)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, a.Address)
	}
	if len(out) == 0 {
		return nil, errors.New("no recipients")
	}
	if len(out) > maxRecipients {
		return nil, fmt.Errorf("more than %d recipients", maxRecipients)
	}
	return out, nil
}

// LoadConfig reads relay.json.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if err := smarthost.Validate(c.Smarthost.SmarthostConfig()); err != nil {
		return nil, err
	}
	return &c, nil
}

// peerUID reads the connecting process's UID (SO_PEERCRED).
func peerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix connection")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return cred.Uid, nil
}

// Submit is the shim's side: hand one message to the relay and return its
// answer as a sysexits-coded error (nil when sent).
func Submit(socketPath string, recipients []string, raw []byte) error {
	conn, err := net.DialTimeout("unix", socketPath, 10*time.Second)
	if err != nil {
		return &sendmailshim.CodedError{Code: sendmailshim.ExitTempFail, Err: fmt.Errorf("the website mail relay isn't reachable: %w", err)}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(connTimeout + 10*time.Second))

	head, _ := json.Marshal(Request{Recipients: recipients, Size: len(raw)})
	// The relay may refuse after the first line and stop reading, so a
	// failed write still reads its answer: that says why.
	bufs := net.Buffers{append(head, '\n'), raw}
	_, werr := bufs.WriteTo(conn)
	line, err := bufio.NewReader(io.LimitReader(conn, maxHeaderLine)).ReadBytes('\n')
	if err != nil {
		if werr != nil {
			err = werr
		}
		return &sendmailshim.CodedError{Code: sendmailshim.ExitTempFail, Err: fmt.Errorf("no answer from relay: %w", err)}
	}
	var rep Reply
	if err := json.Unmarshal(line, &rep); err != nil {
		return &sendmailshim.CodedError{Code: sendmailshim.ExitTempFail, Err: fmt.Errorf("bad answer from relay: %w", err)}
	}
	if rep.Code == sendmailshim.ExitOK {
		return nil
	}
	return &sendmailshim.CodedError{Code: rep.Code, Err: errors.New(rep.Error)}
}

// ReadMode reads ModePath: smarthost, or local for anything else (missing
// included), so a box without the setting keeps today's behavior.
func ReadMode(path string) string {
	b, err := os.ReadFile(path)
	if err == nil && strings.TrimSpace(string(b)) == ModeSmarthost {
		return ModeSmarthost
	}
	return ModeLocal
}

// Listen creates the relay's socket. Every local user may connect: who may
// send is decided per connection from the caller's UID.
func Listen(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
