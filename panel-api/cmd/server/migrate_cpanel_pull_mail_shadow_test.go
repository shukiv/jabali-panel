package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A cPanel/WHM SSH pull never carried <home>/etc/<domain>/shadow, so every
// mailbox got a new password even with --preserve-source-state. The pull must
// ask the agent for exactly those files, with exactly these parameters.
func TestCpanelPullMailShadow_DispatchParams(t *testing.T) {
	cases := []struct{ kind, wantSSHUser string }{
		{models.MigrationSourceWHMpkgacct, "root"},
		{models.MigrationSourceCpanel, "acct"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			rec := &mailLegRecorder{reply: json.RawMessage(`{"domains":2,"files":2,"bytes":512}`)}
			job := &models.MigrationJob{ID: "job1", SourceKind: tc.kind, SourceHost: "203.0.113.5", SourceUser: "acct", SourcePort: 2222}

			dir, unreadable, lines, notCopied := cpanelPullMailShadow(context.Background(), rec.call, job, "acct", true)

			if notCopied {
				t.Fatal("notCopied on a good copy")
			}
			if dir != "/var/lib/jabali-migrations/job1/mail-shadow" {
				t.Fatalf("dir = %q", dir)
			}
			if len(unreadable) != 0 {
				t.Fatalf("unreadable = %v", unreadable)
			}
			if len(rec.calls) != 1 || rec.calls[0].cmd != "migration.pull_mail_shadow" {
				t.Fatalf("agent calls = %+v, want one migration.pull_mail_shadow", rec.calls)
			}
			want := map[string]any{
				"port":        2222,
				"job_id":      "job1",
				"host":        "203.0.113.5",
				"ssh_user":    tc.wantSSHUser,
				"secret_path": "/etc/jabali-panel/migration-secrets/job1.env",
				"src_account": "acct",
				"src_home":    "/home/acct",
			}
			got := rec.calls[0].params
			var gotKeys, wantKeys []string
			for k := range got {
				gotKeys = append(gotKeys, k)
			}
			for k := range want {
				wantKeys = append(wantKeys, k)
			}
			sort.Strings(gotKeys)
			sort.Strings(wantKeys)
			if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
				t.Fatalf("param keys = %v, want exactly %v", gotKeys, wantKeys)
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("param %s = %v (%T), want %v", k, got[k], got[k], v)
				}
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "copied 2 domain password file(s) (512 bytes)") {
				t.Fatalf("lines = %v", lines)
			}
		})
	}
}

// Nothing is copied when the hashes would not be used, or when there is no
// SSH pull, or for another source kind.
func TestCpanelPullMailShadow_NotRun(t *testing.T) {
	base := models.MigrationJob{ID: "job1", SourceKind: models.MigrationSourceCpanel, SourceHost: "203.0.113.5", SourceUser: "acct"}
	cases := map[string]struct {
		mutate   func(*models.MigrationJob)
		preserve bool
		wantLine bool // a refused copy: a warning and notCopied
		srcUser  string
	}{
		"preserve off":     {func(*models.MigrationJob) {}, false, false, "acct"},
		"offline restore":  {func(j *models.MigrationJob) { j.SourceHost = "" }, true, false, "acct"},
		"directadmin":      {func(j *models.MigrationJob) { j.SourceKind = models.MigrationSourceDirectAdmin }, true, false, "acct"},
		"account mismatch": {func(*models.MigrationJob) {}, true, true, "other"},
		"path-like user":   {func(j *models.MigrationJob) { j.SourceUser = "../x" }, true, true, "../x"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			job := base
			tc.mutate(&job)
			rec := &mailLegRecorder{reply: json.RawMessage(`{}`)}
			dir, _, lines, notCopied := cpanelPullMailShadow(context.Background(), rec.call, &job, tc.srcUser, tc.preserve)
			if len(rec.calls) != 0 || dir != "" {
				t.Fatalf("called agent %+v, dir %q", rec.calls, dir)
			}
			if notCopied != tc.wantLine {
				t.Fatalf("notCopied = %v", notCopied)
			}
			if tc.wantLine != (len(lines) == 1 && strings.Contains(lines[0], "new password")) {
				t.Fatalf("lines = %v", lines)
			}
		})
	}
}

// A failed copy is a warning that says what it costs, not a critical: mail
// still migrates.
func TestCpanelPullMailShadow_FailureIsWarning(t *testing.T) {
	rec := &mailLegRecorder{err: errors.New("agent: rsync failed")}
	job := &models.MigrationJob{ID: "job1", SourceKind: models.MigrationSourceCpanel, SourceHost: "203.0.113.5", SourceUser: "acct"}
	dir, unreadable, lines, notCopied := cpanelPullMailShadow(context.Background(), rec.call, job, "acct", true)
	if dir != "" || unreadable != nil || !notCopied {
		t.Fatalf("dir %q unreadable %v", dir, unreadable)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "every mailbox gets a new password") || !strings.Contains(lines[0], "rsync failed") {
		t.Fatalf("lines = %v", lines)
	}
}

// Domains the source account cannot read are named, normalised, and only when
// they are domain names; a partial copy is reported.
func TestCpanelPullMailShadow_UnreadableAndPartial(t *testing.T) {
	rec := &mailLegRecorder{reply: json.RawMessage(`{"domains":1,"files":1,"bytes":90,"removed":1,"unreadable":["Shop.Example","../etc","bad name"],"partial":true,"exit_code":23}`)}
	job := &models.MigrationJob{ID: "job1", SourceKind: models.MigrationSourceCpanel, SourceHost: "203.0.113.5", SourceUser: "acct"}
	_, unreadable, lines, _ := cpanelPullMailShadow(context.Background(), rec.call, job, "acct", true)
	if len(unreadable) != 1 || unreadable[0] != "shop.example" {
		t.Fatalf("unreadable = %v, want [shop.example]", unreadable)
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"1 entr(ies) other than", "cannot read the password file of shop.example", "partial copy (rsync exit 23"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "../etc") || strings.Contains(all, "bad name") {
		t.Errorf("unvalidated name reached the summary:\n%s", all)
	}
}

// The staged copy holds password hashes: it is removed once the import is
// over and the pointer cleared, so a second call is a no-op.
func TestDropMailShadow_RemovesStagedCopy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mail-shadow")
	if err := os.MkdirAll(filepath.Join(dir, "main.example"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.example", "shadow"), []byte("box:x::\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed := &cpanel.ParsedTarball{MailShadowDir: dir}
	if line := dropMailShadow(parsed); line != "" {
		t.Fatalf("line = %q", line)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("staged copy still there: %v", err)
	}
	if parsed.MailShadowDir != "" {
		t.Fatalf("pointer not cleared: %q", parsed.MailShadowDir)
	}
	if line := dropMailShadow(parsed); line != "" {
		t.Fatalf("second call: %q", line)
	}
	if line := dropMailShadow(nil); line != "" {
		t.Fatalf("nil: %q", line)
	}
}

// Wiring: the password files are pulled after ~/mail and before the import,
// handed to the import through ParsedTarball, and removed straight after it —
// before its error is checked, so a failed import does not leave them.
func TestCpanelMailShadow_WiredAroundImportMailboxes(t *testing.T) {
	src, err := os.ReadFile("migrate_run_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	pullMail := strings.Index(body, "cpanelPullMail(ctx, restoreAgent.Call")
	pullShadow := strings.Index(body, "cpanelPullMailShadow(ctx, restoreAgent.Call, job, p.parsed.SourceUser, preserve.Credentials)")
	setDir := strings.Index(body, "p.parsed.MailShadowDir = shadowDir")
	setUnreadable := strings.Index(body, "p.parsed.MailShadowUnreadable = unreadable")
	setNotCopied := strings.Index(body, "p.parsed.MailShadowNotCopied = notCopied")
	backstop := strings.Index(body, "defer func() { _ = dropMailShadow(p.parsed) }()")
	imp := strings.Index(body, "mailRes, err := cpanel.ImportMailboxes(")
	if pullMail < 0 || pullShadow < 0 || setDir < 0 || setUnreadable < 0 || setNotCopied < 0 || backstop < 0 || imp < 0 {
		t.Fatalf("missing wiring: pullMail=%d pullShadow=%d setDir=%d setUnreadable=%d backstop=%d import=%d",
			pullMail, pullShadow, setDir, setUnreadable, backstop, imp)
	}
	if !(pullMail < pullShadow && pullShadow < setDir && setDir < imp && setUnreadable < imp && setNotCopied < imp && backstop < imp) {
		t.Fatal("password files must be pulled after ~/mail and set on ParsedTarball before ImportMailboxes")
	}
	after := body[imp:]
	drop := strings.Index(after, "dropMailShadow(p.parsed)")
	errCheck := strings.Index(after, "if err != nil {")
	if drop < 0 || errCheck < 0 || drop > errCheck {
		t.Fatal("dropMailShadow must run right after ImportMailboxes, before its error return")
	}
}
