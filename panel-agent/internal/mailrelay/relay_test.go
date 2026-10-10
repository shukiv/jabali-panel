package mailrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/smarthost"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/sendmailshim"
)

// sent is one message the fake smarthost got.
type sent struct {
	cfg  smarthost.Config
	from string
	to   []string
	msg  string
}

type fakeSend struct {
	mu   sync.Mutex
	got  []sent
	fail error
}

func (f *fakeSend) send(_ context.Context, c smarthost.Config, from string, to []string, msg []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.got = append(f.got, sent{cfg: c, from: from, to: to, msg: string(msg)})
	return nil
}

func writeConfig(t *testing.T, c Config) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "relay.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func baseConfig() Config {
	return Config{
		Smarthost: Smarthost{Host: "smtp.example.net", Port: 587, TLS: "starttls", Username: "relay", Password: "pw", Helo: "panel.example.net"},
		Senders: map[string]Sender{
			"2001": {User: "alice", Domains: []string{"alice.example", "shop.example"}, Default: "alice.example"},
		},
	}
}

// startRelay runs the relay on a real unix socket and reports every caller
// as uid.
func startRelay(t *testing.T, cfgPath string, uid uint32, f *fakeSend) string {
	t.Helper()
	return startServer(t, &Server{
		ConfigPath: cfgPath,
		Send:       f.send,
		PeerUID:    func(net.Conn) (uint32, error) { return uid, nil },
	})
}

// sockDir is a short directory for a socket: t.TempDir() carries the test
// name, and a unix socket path can't be longer than 107 bytes.
func sockDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "mr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func startServer(t *testing.T, s *Server) string {
	t.Helper()
	sock := filepath.Join(sockDir(t), "relay.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return sock
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	return sendmailshim.ExitCode(err)
}

const contactForm = "From: WordPress <wordpress@shop.example>\nTo: owner@example.org\nBcc: hidden@example.org\nSubject: New enquiry\n\nHello\n"

func TestSubmit_SendsAsTheCallersOwnDomain(t *testing.T) {
	f := &fakeSend{}
	sock := startRelay(t, writeConfig(t, baseConfig()), 2001, f)

	err := Submit(sock, []string{"owner@example.org", "hidden@example.org", "Owner@Example.org"}, []byte(contactForm))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if len(f.got) != 1 {
		t.Fatalf("smarthost got %d messages, want 1", len(f.got))
	}
	g := f.got[0]
	if g.from != "noreply@shop.example" {
		t.Errorf("envelope sender = %q, want noreply@shop.example", g.from)
	}
	if strings.Join(g.to, ",") != "owner@example.org,hidden@example.org" {
		t.Errorf("recipients = %v (duplicates must go, Bcc recipients stay)", g.to)
	}
	if strings.Contains(g.msg, "Bcc:") {
		t.Errorf("the Bcc header reached the smarthost:\n%s", g.msg)
	}
	if !strings.Contains(g.msg, "Sender: <noreply@shop.example>") {
		t.Errorf("no honest Sender header:\n%s", g.msg)
	}
	if g.cfg.Host != "smtp.example.net" || g.cfg.Username != "relay" || g.cfg.Password != "pw" || g.cfg.HeloName != "panel.example.net" {
		t.Errorf("smarthost config not passed through: %+v", g.cfg)
	}
}

func TestSubmit_ForeignFromDomainUsesTheDefault(t *testing.T) {
	f := &fakeSend{}
	sock := startRelay(t, writeConfig(t, baseConfig()), 2001, f)

	msg := "From: someone@victim.example\nTo: x@example.org\nSubject: hi\n\nbody\n"
	if err := Submit(sock, []string{"x@example.org"}, []byte(msg)); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if got := f.got[0].from; got != "noreply@alice.example" {
		t.Errorf("envelope sender = %q, want the user's default domain, never the From domain", got)
	}
}

func TestSubmit_Refusals(t *testing.T) {
	tooMany := make([]string, maxRecipients+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("r%d@example.org", i)
	}
	cases := []struct {
		name  string
		uid   uint32
		rcpts []string
		want  int
	}{
		{"a user not in the sender list", 2002, []string{"x@example.org"}, sendmailshim.ExitNoPerm},
		{"root is not a site user", 0, []string{"x@example.org"}, sendmailshim.ExitNoPerm},
		{"no recipients", 2001, []string{" "}, sendmailshim.ExitDataErr},
		{"a recipient that isn't an address", 2001, []string{"x@example.org", "not an address"}, sendmailshim.ExitDataErr},
		// net/mail unquotes "a>b"@x to a>b@x, which would break out of RCPT TO:<...>.
		{"a recipient with an angle bracket", 2001, []string{`"a>b"@example.org`}, sendmailshim.ExitDataErr},
		{"too many recipients", 2001, tooMany, sendmailshim.ExitDataErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSend{}
			sock := startRelay(t, writeConfig(t, baseConfig()), tc.uid, f)
			err := Submit(sock, tc.rcpts, []byte(contactForm))
			if got := exitCode(err); got != tc.want {
				t.Errorf("exit code = %d (%v), want %d", got, err, tc.want)
			}
			if len(f.got) != 0 {
				t.Errorf("a refused message reached the smarthost")
			}
		})
	}
}

func TestHandle_RefusesOversizeBeforeReadingIt(t *testing.T) {
	f := &fakeSend{}
	sock := startRelay(t, writeConfig(t, baseConfig()), 2001, f)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	head, _ := json.Marshal(Request{Recipients: []string{"x@example.org"}, Size: sendmailshim.MaxMessageBytes + 1})
	if _, err := conn.Write(append(head, '\n')); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var rep Reply
	if err := json.NewDecoder(conn).Decode(&rep); err != nil {
		t.Fatalf("no reply: %v", err)
	}
	if rep.Code != sendmailshim.ExitDataErr {
		t.Errorf("code = %d, want %d", rep.Code, sendmailshim.ExitDataErr)
	}
}

// A big message from a caller the relay refuses after the first line: the
// shim still gets the reason, not a broken pipe.
func TestSubmit_RefusedBigMessageStillGetsTheReason(t *testing.T) {
	f := &fakeSend{}
	sock := startRelay(t, writeConfig(t, baseConfig()), 2002, f)
	big := []byte(contactForm + strings.Repeat("x", 8<<20))
	err := Submit(sock, []string{"x@example.org"}, big)
	if got := exitCode(err); got != sendmailshim.ExitNoPerm {
		t.Errorf("exit code = %d (%v), want %d", got, err, sendmailshim.ExitNoPerm)
	}
}

func TestSubmit_NotConfigured(t *testing.T) {
	f := &fakeSend{}
	sock := startRelay(t, filepath.Join(t.TempDir(), "missing.json"), 2001, f)
	if got := exitCode(Submit(sock, []string{"x@example.org"}, []byte(contactForm))); got != sendmailshim.ExitConfig {
		t.Errorf("exit code = %d, want %d", got, sendmailshim.ExitConfig)
	}

	// A config whose smarthost doesn't validate is refused the same way:
	// a login over a plain connection never leaves the box.
	c := baseConfig()
	c.Smarthost.TLS = "none"
	sock = startRelay(t, writeConfig(t, c), 2001, f)
	if got := exitCode(Submit(sock, []string{"x@example.org"}, []byte(contactForm))); got != sendmailshim.ExitConfig {
		t.Errorf("exit code = %d, want %d", got, sendmailshim.ExitConfig)
	}
	if len(f.got) != 0 {
		t.Error("a message went out without a valid smarthost")
	}
}

func TestSubmit_SendFailureCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"refused message", &smarthost.Error{Stage: smarthost.StageSMTP, Err: &textproto.Error{Code: 550, Msg: "5.7.1 rejected"}}, sendmailshim.ExitDataErr},
		{"busy smarthost", &smarthost.Error{Stage: smarthost.StageSMTP, Err: &textproto.Error{Code: 451, Msg: "try later"}}, sendmailshim.ExitTempFail},
		{"unreachable", &smarthost.Error{Stage: smarthost.StageConnect, Err: errors.New("connection refused")}, sendmailshim.ExitTempFail},
		{"wrong login", &smarthost.Error{Stage: smarthost.StageAuth, Err: &textproto.Error{Code: 535, Msg: "bad credentials"}}, sendmailshim.ExitConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSend{fail: tc.err}
			sock := startRelay(t, writeConfig(t, baseConfig()), 2001, f)
			err := Submit(sock, []string{"x@example.org"}, []byte(contactForm))
			if got := exitCode(err); got != tc.want {
				t.Errorf("exit code = %d (%v), want %d", got, err, tc.want)
			}
		})
	}
}

func TestSubmit_RelayDown(t *testing.T) {
	err := Submit(filepath.Join(t.TempDir(), "none.sock"), []string{"x@example.org"}, []byte(contactForm))
	if got := exitCode(err); got != sendmailshim.ExitTempFail {
		t.Errorf("exit code = %d, want %d", got, sendmailshim.ExitTempFail)
	}
}

func TestPeerUID_RealSocket(t *testing.T) {
	sock := filepath.Join(sockDir(t), "p.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o666 {
		t.Errorf("socket mode = %v, want 0666 (who may send is decided per caller)", fi.Mode().Perm())
	}
	go func() {
		c, err := net.Dial("unix", sock)
		if err == nil {
			time.Sleep(100 * time.Millisecond)
			c.Close()
		}
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	uid, err := peerUID(conn)
	if err != nil {
		t.Fatal(err)
	}
	if uid != uint32(os.Getuid()) {
		t.Errorf("peer uid = %d, want %d", uid, os.Getuid())
	}
}

func TestReadMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "website-mail.mode")
	if got := ReadMode(p); got != ModeLocal {
		t.Errorf("missing file: %q, want local", got)
	}
	for content, want := range map[string]string{"smarthost\n": ModeSmarthost, "local\n": ModeLocal, "SMARTHOST": ModeLocal, "": ModeLocal} {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ReadMode(p); got != want {
			t.Errorf("ReadMode(%q) = %q, want %q", content, got, want)
		}
	}
}

// headerLines returns a sent message's header lines with that name.
func headerLines(msg, name string) []string {
	head := msg
	if i := strings.Index(msg, "\n\n"); i >= 0 {
		head = msg[:i]
	}
	var out []string
	for _, l := range strings.Split(head, "\n") {
		if strings.HasPrefix(strings.ToLower(l), strings.ToLower(name)+":") {
			out = append(out, l)
		}
	}
	return out
}

// Nothing downstream of the relay knows which account sent a message, so a
// site can't put someone else's address in From: the smarthost would send it
// with the operator's reputation.
func TestSubmit_FromIsAlwaysOneOwnAddress(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantFrom  string
		wantReply []string
	}{
		{
			name:      "own address kept",
			msg:       "From: Shop <orders@shop.example>\nTo: x@example.org\n\nhi\n",
			wantFrom:  "From: \"Shop\" <orders@shop.example>",
			wantReply: nil,
		},
		{
			name:      "foreign address replaced, replies still reach it",
			msg:       "From: \"Bank CEO\" <ceo@bank.example>\nTo: x@example.org\n\nhi\n",
			wantFrom:  "From: \"Bank CEO\" <noreply@alice.example>",
			wantReply: []string{"Reply-To: <ceo@bank.example>"},
		},
		{
			name:      "a second From can't ride along",
			msg:       "From: orders@shop.example\nFrom: ceo@bank.example\nTo: x@example.org\n\nhi\n",
			wantFrom:  "From: <noreply@shop.example>",
			wantReply: []string{"Reply-To: <orders@shop.example>"},
		},
		{
			name:      "an address list can't smuggle a foreign address",
			msg:       "From: orders@shop.example, ceo@bank.example\nTo: x@example.org\n\nhi\n",
			wantFrom:  "From: <noreply@alice.example>",
			wantReply: []string{"Reply-To: <orders@shop.example>"},
		},
		{
			name:      "the site's own Reply-To is kept",
			msg:       "From: visitor@gmail.example\nReply-To: visitor@gmail.example\nTo: x@example.org\n\nhi\n",
			wantFrom:  "From: <noreply@alice.example>",
			wantReply: []string{"Reply-To: visitor@gmail.example"},
		},
		{
			name:      "no From at all",
			msg:       "To: x@example.org\nSubject: hi\n\nhi\n",
			wantFrom:  "From: <noreply@alice.example>",
			wantReply: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSend{}
			sock := startRelay(t, writeConfig(t, baseConfig()), 2001, f)
			if err := Submit(sock, []string{"x@example.org"}, []byte(tc.msg)); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			got := f.got[0].msg
			if froms := headerLines(got, "From"); len(froms) != 1 || froms[0] != tc.wantFrom {
				t.Errorf("From headers = %q, want [%q]\n%s", froms, tc.wantFrom, got)
			}
			if rt := headerLines(got, "Reply-To"); strings.Join(rt, "|") != strings.Join(tc.wantReply, "|") {
				t.Errorf("Reply-To headers = %q, want %q", rt, tc.wantReply)
			}
		})
	}
}

func TestSubmit_SiteSenderHeaderNeverSurvives(t *testing.T) {
	f := &fakeSend{}
	sock := startRelay(t, writeConfig(t, baseConfig()), 2001, f)
	msg := "From: <noreply@shop.example>\nSender: ceo@bank.example\nTo: x@example.org\n\nhi\n"
	if err := Submit(sock, []string{"x@example.org"}, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	if s := headerLines(f.got[0].msg, "Sender"); len(s) != 0 {
		t.Errorf("the site's Sender header reached the smarthost: %q", s)
	}
}

// Like sendmail, the relay adds the Date and Message-ID a site's mail()
// leaves out: PHP doesn't write them, a Postfix smarthost adds them only for
// its own local clients, and Gmail refuses mail without a Message-ID.
func TestSubmit_AddsTheDateAndMessageIDASiteLeftOut(t *testing.T) {
	f := &fakeSend{}
	sock := startRelay(t, writeConfig(t, baseConfig()), 2001, f)
	for range 2 {
		if err := Submit(sock, []string{"x@example.org"}, []byte(contactForm)); err != nil {
			t.Fatal(err)
		}
	}
	m := f.got[0].msg
	date := headerLines(m, "Date")
	if len(date) != 1 {
		t.Fatalf("Date = %q, want one", date)
	}
	if _, err := mail.ParseDate(strings.TrimSpace(date[0][len("Date:"):])); err != nil {
		t.Errorf("Date %q doesn't parse: %v", date[0], err)
	}
	id := headerLines(m, "Message-ID")
	if len(id) != 1 || !strings.HasPrefix(id[0], "Message-ID: <") || !strings.HasSuffix(id[0], "@shop.example>") {
		t.Fatalf("Message-ID = %q, want one <...@shop.example>, the sending domain", id)
	}
	if again := headerLines(f.got[1].msg, "Message-ID"); len(again) != 1 || again[0] == id[0] {
		t.Errorf("two messages got Message-IDs %q and %q", id[0], again)
	}

	// A site's own are kept, not doubled.
	own := "Date: Mon, 2 Jan 2006 15:04:05 +0000\nMessage-Id: <abc@shop.example>\nFrom: <noreply@shop.example>\nTo: x@example.org\n\nhi\n"
	if err := Submit(sock, []string{"x@example.org"}, []byte(own)); err != nil {
		t.Fatal(err)
	}
	m = f.got[2].msg
	if d, i := headerLines(m, "Date"), headerLines(m, "Message-ID"); len(d) != 1 || d[0] != "Date: Mon, 2 Jan 2006 15:04:05 +0000" || len(i) != 1 || i[0] != "Message-Id: <abc@shop.example>" {
		t.Errorf("the site's own headers: Date %q, Message-ID %q", d, i)
	}
}

// One site holding connections open must not stop another site's mail.
func TestServe_IdleConnectionsDontBlockOtherSites(t *testing.T) {
	c := baseConfig()
	c.Senders["2002"] = Sender{User: "bob", Domains: []string{"bob.example"}, Default: "bob.example"}
	f := &fakeSend{}
	var calls sync.Mutex
	n := 0
	sock := startServer(t, &Server{
		ConfigPath:    writeConfig(t, c),
		Send:          f.send,
		MaxConcurrent: 2,
		PeerUID: func(net.Conn) (uint32, error) {
			calls.Lock()
			defer calls.Unlock()
			n++
			if n <= 2 {
				return 2002, nil
			}
			return 2001, nil
		},
	})
	for i := 0; i < 2; i++ {
		idle, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer idle.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls.Lock()
		seen := n
		calls.Unlock()
		if seen >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	done := make(chan error, 1)
	go func() { done <- Submit(sock, []string{"x@example.org"}, []byte(contactForm)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("another site's idle connections blocked this message")
	}
}

// countingPeer reports uid for every caller and counts the calls.
type countingPeer struct {
	mu  sync.Mutex
	n   int
	uid uint32
}

func (c *countingPeer) peer(net.Conn) (uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.uid, nil
}

func (c *countingPeer) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		seen := c.n
		c.mu.Unlock()
		if seen >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the relay saw fewer than %d callers", n)
}

func TestServe_OneAccountCantHoldMoreThanItsShare(t *testing.T) {
	cp := &countingPeer{uid: 2001}
	f := &fakeSend{}
	sock := startServer(t, &Server{
		ConfigPath:  writeConfig(t, baseConfig()),
		Send:        f.send,
		PeerUID:     cp.peer,
		MaxPerUser:  2,
		ReadTimeout: 10 * time.Second,
	})
	for i := 0; i < 2; i++ {
		idle, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer idle.Close()
	}
	cp.waitFor(t, 2)
	err := Submit(sock, []string{"x@example.org"}, []byte(contactForm))
	if got := exitCode(err); got != sendmailshim.ExitTempFail {
		t.Errorf("exit code = %d (%v), want %d", got, err, sendmailshim.ExitTempFail)
	}
	if len(f.got) != 0 {
		t.Error("a third connection from the same account was served")
	}
}

func TestHandle_SlowCallerTimesOut(t *testing.T) {
	sock := startServer(t, &Server{
		ConfigPath:  writeConfig(t, baseConfig()),
		Send:        (&fakeSend{}).send,
		PeerUID:     func(net.Conn) (uint32, error) { return 2001, nil },
		ReadTimeout: 200 * time.Millisecond,
	})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var rep Reply
	if err := json.NewDecoder(conn).Decode(&rep); err != nil {
		t.Fatalf("no reply: %v", err)
	}
	if rep.Code != sendmailshim.ExitTempFail {
		t.Errorf("code = %d, want %d", rep.Code, sendmailshim.ExitTempFail)
	}
}

func TestHandle_ShortMessageIsRefused(t *testing.T) {
	sock := startRelay(t, writeConfig(t, baseConfig()), 2001, &fakeSend{})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	head, _ := json.Marshal(Request{Recipients: []string{"x@example.org"}, Size: 1000})
	_, _ = conn.Write(append(head, '\n'))
	_, _ = conn.Write([]byte(contactForm))
	_ = conn.(*net.UnixConn).CloseWrite()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var rep Reply
	if err := json.NewDecoder(conn).Decode(&rep); err != nil {
		t.Fatalf("no reply: %v", err)
	}
	if rep.Code != sendmailshim.ExitDataErr {
		t.Errorf("code = %d (%s), want %d", rep.Code, rep.Error, sendmailshim.ExitDataErr)
	}
}

// The relay's header parser, the SMTP client and the smarthost must agree on
// where lines end, or a header can hide from the From check.
func TestSubmit_NoParserDifferentials(t *testing.T) {
	cases := []struct {
		name      string
		msg       string
		wantFrom  string
		wantReply []string
	}{
		{
			name:      "a bare CR can't hide a second From",
			msg:       "From: orders@shop.example\nSubject: hi\rFrom: ceo@bank.example\nTo: x@example.org\n\nhi\n",
			wantFrom:  "From: <noreply@shop.example>",
			wantReply: []string{"Reply-To: <orders@shop.example>"},
		},
		{
			name:     "a comment in an own From is dropped",
			msg:      "From: orders@shop.example (ceo@bank.example)\nTo: x@example.org\n\nhi\n",
			wantFrom: "From: <orders@shop.example>",
		},
		{
			name:     "an address in an own From's display name is dropped",
			msg:      "From: \"ceo@bank.example\" <orders@shop.example>\nTo: x@example.org\n\nhi\n",
			wantFrom: "From: <orders@shop.example>",
		},
		{
			name:      "an address in a replaced From's display name is dropped",
			msg:       "From: \"billing@bank.example\" <ceo@bank.example>\nTo: x@example.org\n\nhi\n",
			wantFrom:  "From: <noreply@alice.example>",
			wantReply: []string{"Reply-To: <ceo@bank.example>"},
		},
		{
			name:     "an encoded address in a display name is dropped too",
			msg:      "From: =?utf-8?q?ceo=40bank.example?= <orders@shop.example>\nTo: x@example.org\n\nhi\n",
			wantFrom: "From: <orders@shop.example>",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSend{}
			sock := startRelay(t, writeConfig(t, baseConfig()), 2001, f)
			if err := Submit(sock, []string{"x@example.org"}, []byte(tc.msg)); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			got := f.got[0].msg
			if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\r") {
				t.Errorf("a bare CR reached the smarthost: %q", got)
			}
			if froms := headerLines(got, "From"); len(froms) != 1 || froms[0] != tc.wantFrom {
				t.Errorf("From headers = %q, want [%q]\n%q", froms, tc.wantFrom, got)
			}
			if rt := headerLines(got, "Reply-To"); strings.Join(rt, "|") != strings.Join(tc.wantReply, "|") {
				t.Errorf("Reply-To headers = %q, want %q", rt, tc.wantReply)
			}
		})
	}
}

// A bare CR in the body could end the message early at a smarthost that
// reads it as a line break, and what follows would run as SMTP commands in
// the operator's session.
func TestSubmit_BodyBareCRIsNormalized(t *testing.T) {
	f := &fakeSend{}
	sock := startRelay(t, writeConfig(t, baseConfig()), 2001, f)
	msg := "From: orders@shop.example\nTo: x@example.org\n\nhi\r.\rMAIL FROM:<ceo@bank.example>\r\n"
	if err := Submit(sock, []string{"x@example.org"}, []byte(msg)); err != nil {
		t.Fatal(err)
	}
	got := f.got[0].msg
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\r") {
		t.Errorf("a bare CR reached the smarthost: %q", got)
	}
}
