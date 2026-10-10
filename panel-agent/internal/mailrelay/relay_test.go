package mailrelay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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
	sock := filepath.Join(t.TempDir(), "relay.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		ConfigPath: cfgPath,
		Send:       f.send,
		PeerUID:    func(net.Conn) (uint32, error) { return uid, nil },
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
	sock := filepath.Join(t.TempDir(), "p.sock")
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
