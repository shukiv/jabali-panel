package commands

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/mailrelay"
)

type mailRelayFixture struct {
	dir      string
	calls    []string
	modeSeen []string // the mode file's content when each systemctl call ran
	startOK  bool     // whether "enable --now" brings the socket up
	ln       net.Listener
	failOut  string
}

func setupMailRelay(t *testing.T) *mailRelayFixture {
	t.Helper()
	dir, err := os.MkdirTemp("", "mra")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &mailRelayFixture{dir: dir, startOK: true}

	prev := struct {
		cfg, mode, sock string
		lu              func(string) (*user.User, error)
		lg              func(string) (*user.Group, error)
		wait            time.Duration
		chown           func(string, int, int) error
		sysctl          func(context.Context, ...string) ([]byte, error)
	}{mailRelayConfigPath, mailRelayModePath, mailRelaySocketPath, mailRelayLookupUser, mailRelayLookupGroup, mailRelaySocketWait, sendmailChown, runSystemctl}
	t.Cleanup(func() {
		mailRelayConfigPath, mailRelayModePath, mailRelaySocketPath = prev.cfg, prev.mode, prev.sock
		mailRelayLookupUser, mailRelayLookupGroup = prev.lu, prev.lg
		mailRelaySocketWait, sendmailChown, runSystemctl = prev.wait, prev.chown, prev.sysctl
		if f.ln != nil {
			f.ln.Close()
		}
	})

	mailRelayConfigPath = filepath.Join(dir, "mailrelay", "relay.json")
	mailRelayModePath = filepath.Join(dir, "sendmail", "website-mail.mode")
	mailRelaySocketPath = filepath.Join(dir, "relay.sock")
	mailRelaySocketWait = 300 * time.Millisecond
	sendmailChown = func(string, int, int) error { return nil }
	mailRelayLookupGroup = func(name string) (*user.Group, error) {
		if name != "jabali-mailrelay" {
			return nil, errors.New("unknown group")
		}
		return &user.Group{Gid: "990", Name: name}, nil
	}
	mailRelayLookupUser = func(name string) (*user.User, error) {
		switch name {
		case "alice":
			return &user.User{Uid: "2001", Username: name}, nil
		case "bob":
			return &user.User{Uid: "2002", Username: name}, nil
		case "toor":
			return &user.User{Uid: "0", Username: name}, nil
		case "www_data":
			return &user.User{Uid: "33", Username: name}, nil
		case "jabali-mailrelay":
			return &user.User{Uid: "990", Gid: "990", Username: name}, nil
		}
		return nil, user.UnknownUserError(name)
	}
	runSystemctl = func(_ context.Context, args ...string) ([]byte, error) {
		f.calls = append(f.calls, strings.Join(args, " "))
		mode, _ := os.ReadFile(mailRelayModePath)
		f.modeSeen = append(f.modeSeen, string(mode))
		if f.failOut != "" {
			return []byte(f.failOut), errors.New("exit status 1")
		}
		if args[0] == "enable" && f.startOK && f.ln == nil {
			ln, err := net.Listen("unix", mailRelaySocketPath)
			if err != nil {
				t.Fatal(err)
			}
			f.ln = ln
		}
		return nil, nil
	}
	return f
}

func callMailRelay(t *testing.T, p any) (mailRelayApplyResponse, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	out, err := mailRelayApplyHandler(context.Background(), raw)
	if err != nil {
		return mailRelayApplyResponse{}, err
	}
	return out.(mailRelayApplyResponse), nil
}

func smarthostParams() map[string]any {
	return map[string]any{
		"mode":      "smarthost",
		"smarthost": map[string]any{"host": "smtp.example.net", "port": 587, "tls": "starttls", "username": "relay", "password": "pw", "helo": "panel.example.net"},
		"senders": []map[string]any{
			{"username": "alice", "domains": []string{"Alice.example", "shop.example", "bad domain", "alice.example"}, "default": "shop.example"},
			{"username": "bob", "domains": []string{"bob.example"}, "default": "other.example"},
			{"username": "ghost", "domains": []string{"ghost.example"}},
			{"username": "toor", "domains": []string{"root.example"}},
			{"username": "www_data", "domains": []string{"www.example"}},
			{"username": "carol", "domains": []string{"bad domain"}},
		},
	}
}

func TestMailRelayApply_SmarthostWritesSendersStartsRelayThenSwitches(t *testing.T) {
	f := setupMailRelay(t)
	resp, err := callMailRelay(t, smarthostParams())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.Mode != "smarthost" || !resp.Changed || resp.Senders != 2 {
		t.Errorf("response = %+v", resp)
	}
	if len(resp.Skipped) != 4 {
		t.Errorf("skipped = %q, want ghost, toor, www_data and carol", resp.Skipped)
	}

	cfg, err := mailrelay.LoadConfig(mailRelayConfigPath)
	if err != nil {
		t.Fatalf("relay can't load what the agent wrote: %v", err)
	}
	if a := cfg.Senders["2001"]; a.User != "alice" || strings.Join(a.Domains, ",") != "alice.example,shop.example" || a.Default != "shop.example" {
		t.Errorf("alice = %+v", a)
	}
	if b := cfg.Senders["2002"]; b.Default != "bob.example" {
		t.Errorf("bob's default must fall back to one of his domains: %+v", b)
	}
	if _, ok := cfg.Senders["0"]; ok {
		t.Error("root is on the sender list")
	}
	if cfg.Smarthost.Password != "pw" || cfg.Smarthost.Helo != "panel.example.net" {
		t.Errorf("smarthost = %+v", cfg.Smarthost)
	}
	fi, _ := os.Stat(mailRelayConfigPath)
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("config mode = %v, want 0640 (it holds the password)", fi.Mode().Perm())
	}

	if strings.Join(f.calls, "|") != "enable --now jabali-mailrelay.service" {
		t.Errorf("systemctl calls = %q", f.calls)
	}
	if f.modeSeen[0] == "smarthost\n" {
		t.Error("the shim pointed at the relay before it was started")
	}
	if mode := mailrelay.ReadMode(mailRelayModePath); mode != "smarthost" {
		t.Errorf("mode = %q, want smarthost", mode)
	}

	// Same input again: nothing changes.
	resp, err = callMailRelay(t, smarthostParams())
	if err != nil || resp.Changed {
		t.Errorf("second apply: changed=%v err=%v", resp.Changed, err)
	}
}

func TestMailRelayApply_RelayThatDoesntStartLeavesTheShimLocal(t *testing.T) {
	f := setupMailRelay(t)
	f.startOK = false
	_, err := callMailRelay(t, smarthostParams())
	if err == nil {
		t.Fatal("apply succeeded without a listening relay")
	}
	if mode := mailrelay.ReadMode(mailRelayModePath); mode != "local" {
		t.Errorf("mode = %q: the shim points at a relay that isn't listening", mode)
	}
}

func TestMailRelayApply_LocalSwitchesFirstThenStopsAndDeletesThePassword(t *testing.T) {
	f := setupMailRelay(t)
	if _, err := callMailRelay(t, smarthostParams()); err != nil {
		t.Fatal(err)
	}
	f.calls, f.modeSeen = nil, nil

	resp, err := callMailRelay(t, map[string]any{"mode": "local"})
	if err != nil {
		t.Fatalf("apply local: %v", err)
	}
	if !resp.Changed || resp.Mode != "local" {
		t.Errorf("response = %+v", resp)
	}
	if strings.Join(f.calls, "|") != "disable --now jabali-mailrelay.service" {
		t.Errorf("systemctl calls = %q", f.calls)
	}
	if f.modeSeen[0] != "local\n" {
		t.Errorf("mode when the relay stopped = %q: sites would have sent into a stopped relay", f.modeSeen[0])
	}
	if _, err := os.Stat(mailRelayConfigPath); !os.IsNotExist(err) {
		t.Errorf("the relay config (with the password) is still there: %v", err)
	}
}

func TestMailRelayApply_LocalOnABoxWithoutTheRelay(t *testing.T) {
	f := setupMailRelay(t)
	f.failOut = "Failed to disable unit: Unit file jabali-mailrelay.service does not exist."
	if _, err := callMailRelay(t, map[string]any{"mode": "local"}); err != nil {
		t.Fatalf("apply local: %v", err)
	}
	if mode := mailrelay.ReadMode(mailRelayModePath); mode != "local" {
		t.Errorf("mode = %q", mode)
	}
}

func TestMailRelayApply_Refusals(t *testing.T) {
	plain := smarthostParams()
	plain["smarthost"] = map[string]any{"host": "smtp.example.net", "port": 25, "tls": "none", "username": "relay", "password": "pw"}
	badHelo := smarthostParams()
	badHelo["smarthost"] = map[string]any{"host": "smtp.example.net", "port": 587, "tls": "starttls", "helo": "x\r\nRCPT TO:<a@b>"}
	cases := []struct {
		name string
		p    any
		code string
	}{
		{"unknown mode", map[string]any{"mode": "direct"}, agentwire.CodeInvalidArgument},
		{"no smarthost", map[string]any{"mode": "smarthost"}, agentwire.CodeInvalidArgument},
		{"a login without encryption", plain, agentwire.CodeInvalidArgument},
		{"a helo name with a line break", badHelo, agentwire.CodeInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMailRelay(t)
			_, err := callMailRelay(t, tc.p)
			var ae *agentwire.AgentError
			if !errors.As(err, &ae) || ae.Code != tc.code {
				t.Fatalf("err = %v, want code %s", err, tc.code)
			}
			if len(f.calls) != 0 {
				t.Errorf("systemctl ran: %q", f.calls)
			}
			if _, err := os.Stat(mailRelayConfigPath); !os.IsNotExist(err) {
				t.Error("a refused apply wrote the relay config")
			}
		})
	}
}

func TestMailRelayApply_MissingRelayUser(t *testing.T) {
	setupMailRelay(t)
	mailRelayLookupGroup = func(string) (*user.Group, error) { return nil, errors.New("unknown group") }
	_, err := callMailRelay(t, smarthostParams())
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("err = %v, want failed_precondition", err)
	}
}

func TestStartMailRelayIfSelected(t *testing.T) {
	f := setupMailRelay(t)
	StartMailRelayIfSelected(context.Background(), slog.Default())
	if len(f.calls) != 0 {
		t.Errorf("started the relay with no smarthost selected: %q", f.calls)
	}
	if err := os.MkdirAll(filepath.Dir(mailRelayModePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mailRelayModePath, []byte("smarthost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	StartMailRelayIfSelected(context.Background(), slog.Default())
	if strings.Join(f.calls, "|") != "start --no-block jabali-mailrelay.service" {
		t.Errorf("systemctl calls = %q", f.calls)
	}
}

func TestMailRelayApply_RelayAccountMustBeTheSystemUser(t *testing.T) {
	cases := map[string]*user.User{
		"root":               {Uid: "0", Gid: "990"},
		"a login account":    {Uid: "1500", Gid: "990"},
		"another prim group": {Uid: "990", Gid: "33"},
	}
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			f := setupMailRelay(t)
			base := mailRelayLookupUser
			mailRelayLookupUser = func(n string) (*user.User, error) {
				if n == "jabali-mailrelay" {
					return u, nil
				}
				return base(n)
			}
			_, err := callMailRelay(t, smarthostParams())
			var ae *agentwire.AgentError
			if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
				t.Fatalf("err = %v, want failed_precondition", err)
			}
			if _, err := os.Stat(mailRelayConfigPath); !os.IsNotExist(err) {
				t.Error("the password was written for an account that isn't the relay's")
			}
			if len(f.calls) != 0 {
				t.Errorf("systemctl ran: %q", f.calls)
			}
		})
	}
}
