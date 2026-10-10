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
		passwd, group   string
		lu              func(string) (*user.User, error)
		wait            time.Duration
		chown           func(string, int, int) error
		sysctl          func(context.Context, ...string) ([]byte, error)
	}{mailRelayConfigPath, mailRelayModePath, mailRelaySocketPath, mailRelayPasswdPath, mailRelayGroupPath, mailRelayLookupUser, mailRelaySocketWait, sendmailChown, runSystemctl}
	t.Cleanup(func() {
		mailRelayConfigPath, mailRelayModePath, mailRelaySocketPath = prev.cfg, prev.mode, prev.sock
		mailRelayPasswdPath, mailRelayGroupPath = prev.passwd, prev.group
		mailRelayLookupUser = prev.lu
		mailRelaySocketWait, sendmailChown, runSystemctl = prev.wait, prev.chown, prev.sysctl
		if f.ln != nil {
			f.ln.Close()
		}
	})

	mailRelayConfigPath = filepath.Join(dir, "mailrelay", "relay.json")
	mailRelayModePath = filepath.Join(dir, "sendmail", "website-mail.mode")
	mailRelaySocketPath = filepath.Join(dir, "relay.sock")
	mailRelaySocketWait = 300 * time.Millisecond
	mailRelayPasswdPath = filepath.Join(dir, "passwd")
	mailRelayGroupPath = filepath.Join(dir, "group")
	writeRelayAccount(t, relayPasswdLine, relayGroupLine)
	sendmailChown = func(string, int, int) error { return nil }
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
		case "nobody":
			return &user.User{Uid: "65534", Username: name}, nil
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

// The relay account as install.sh creates it: a local system user with its
// own group, no members and no login shell.
const (
	relayPasswdLine = "jabali-mailrelay:x:990:990::/nonexistent:/usr/sbin/nologin"
	relayGroupLine  = "jabali-mailrelay:x:990:"
)

func writeRelayAccount(t *testing.T, passwd, group string) {
	t.Helper()
	if err := os.WriteFile(mailRelayPasswdPath, []byte("root:x:0:0:root:/root:/bin/bash\n"+passwd+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mailRelayGroupPath, []byte("root:x:0:\n"+group+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
			{"username": "nobody", "domains": []string{"nobody.example"}},
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
	if len(resp.Skipped) != 5 || !strings.Contains(strings.Join(resp.Skipped, "|"), "nobody: a system account may not send") {
		t.Errorf("skipped = %q, want ghost, toor, www_data, nobody and carol", resp.Skipped)
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
	if err := os.WriteFile(mailRelayGroupPath, []byte("root:x:0:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := callMailRelay(t, smarthostParams())
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition || !strings.Contains(ae.Message, "missing") {
		t.Fatalf("err = %v, want failed_precondition: the user is missing", err)
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

// The agent applies the same rules as install.sh's mailrelay_account_ok, from
// the local files only: install.sh switching the relay off is undone by the
// next apply unless this gate refuses too. A directory service (LDAP, sssd)
// that answers for jabali-mailrelay is never taken at its word.
func TestMailRelayApply_RelayAccountMustBeTheSystemUser(t *testing.T) {
	cases := map[string][2]string{
		"root":                    {"jabali-mailrelay:x:0:990::/nonexistent:/usr/sbin/nologin", relayGroupLine},
		"a login account":         {"jabali-mailrelay:x:1500:990::/nonexistent:/usr/sbin/nologin", relayGroupLine},
		"another primary group":   {"jabali-mailrelay:x:990:33::/nonexistent:/usr/sbin/nologin", relayGroupLine},
		"a login shell":           {"jabali-mailrelay:x:990:990::/nonexistent:/bin/bash", relayGroupLine},
		"a group with members":    {relayPasswdLine, "jabali-mailrelay:x:990:alice"},
		"no local account":        {"otheruser:x:990:990::/nonexistent:/usr/sbin/nologin", relayGroupLine},
		"no local group":          {relayPasswdLine, "othergroup:x:990:"},
		"a truncated passwd line": {"jabali-mailrelay:x:990:990", relayGroupLine},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := setupMailRelay(t)
			writeRelayAccount(t, c[0], c[1])
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

// The panel's side of the mail.relay.apply contract (GH #2056): the request
// the panel sends decodes into this handler's params with every field kept,
// and the response the panel expects is what this handler returns.
func TestMailRelayApply_PanelContract(t *testing.T) {
	dir := "../../../panel-api/internal/agent/testdata/"
	for _, c := range []struct {
		file string
		into any
	}{
		{"mail_relay_apply_request.json", &mailRelayApplyParams{}},
		{"mail_relay_apply_response.json", &mailRelayApplyResponse{}},
	} {
		raw, err := os.ReadFile(dir + c.file)
		if err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(c.into); err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		again, _ := json.Marshal(c.into)
		var got, want any
		_ = json.Unmarshal(again, &got)
		_ = json.Unmarshal(raw, &want)
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(want)
		if string(gb) != string(wb) {
			t.Errorf("%s: the agent drops or renames fields:\nwant %s\ngot  %s", c.file, wb, gb)
		}
	}
}
