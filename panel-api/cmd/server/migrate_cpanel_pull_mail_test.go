package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type mailLegCall struct {
	cmd    string
	params map[string]any
}

// mailLegRecorder stands in for (*agent.Client).Call and records every call.
type mailLegRecorder struct {
	calls []mailLegCall
	reply json.RawMessage
	err   error
}

func (r *mailLegRecorder) call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	m, _ := params.(map[string]any)
	r.calls = append(r.calls, mailLegCall{cmd: cmd, params: m})
	if r.err != nil {
		return nil, r.err
	}
	return r.reply, nil
}

// The cPanel/WHM SSH pull runs pkgacct --skiphomedir, so ~/mail never reaches
// the tarball and the import walked an empty tree: zero mailboxes, job "done".
// The mail leg must rsync ~/mail from the source and point MailRoot at the copy.
func TestCpanelPullMail_CopiesHomeMail(t *testing.T) {
	cases := []struct {
		kind, wantSSHUser string
	}{
		{models.MigrationSourceWHMpkgacct, "root"}, // WHM pulls over the root session
		{models.MigrationSourceCpanel, "webup"},    // cPanel: the account is the login
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			rec := &mailLegRecorder{reply: json.RawMessage(`{"bytes_copied":934,"files":12}`)}
			job := &models.MigrationJob{ID: "job1", SourceKind: tc.kind, SourceHost: "203.0.113.5", SourceUser: "webup"}

			root, lines, crit := cpanelPullMail(context.Background(), rec.call, job, "webup", "gil")

			if crit != "" {
				t.Fatalf("unexpected critical: %s", crit)
			}
			if root != "/home/gil/mail" {
				t.Fatalf("MailRoot = %q, want /home/gil/mail", root)
			}
			if len(rec.calls) != 1 || rec.calls[0].cmd != "migration.rsync_remote_home" {
				t.Fatalf("agent calls = %+v, want one migration.rsync_remote_home", rec.calls)
			}
			want := map[string]any{
				"job_id":      "job1",
				"host":        "203.0.113.5",
				"ssh_user":    tc.wantSSHUser,
				"secret_path": "/etc/jabali-panel/migration-secrets/job1.env",
				"src_account": "webup",
				"src_path":    "/home/webup/mail",
				"dest_path":   "/home/gil/mail",
				"dest_user":   "gil",
			}
			got := rec.calls[0].params
			for k, v := range want {
				if got[k] != v {
					t.Errorf("param %s = %v, want %v", k, got[k], v)
				}
			}
			if len(lines) != 1 || !strings.Contains(lines[0], "bytes=934") {
				t.Errorf("summary lines = %v, want one line with bytes=934", lines)
			}
		})
	}
}

func TestCpanelPullMail_NoOpOutsideACpanelPull(t *testing.T) {
	cases := map[string]*models.MigrationJob{
		"nil job": nil,
		"offline restore: the full cpmove already carries homedir/mail": {
			ID: "j", SourceKind: models.MigrationSourceCpanel, SourceUser: "webup"},
		"plesk has its own mail leg": {
			ID: "j", SourceKind: models.MigrationSourcePlesk, SourceHost: "203.0.113.5", SourceUser: "a.test"},
		"cyberpanel has its own mail leg": {
			ID: "j", SourceKind: models.MigrationSourceCyberPanel, SourceHost: "203.0.113.5", SourceUser: "a.test"},
		"directadmin": {
			ID: "j", SourceKind: models.MigrationSourceDirectAdmin, SourceHost: "203.0.113.5", SourceUser: "admin"},
	}
	for name, job := range cases {
		t.Run(name, func(t *testing.T) {
			rec := &mailLegRecorder{}
			root, lines, crit := cpanelPullMail(context.Background(), rec.call, job, "webup", "gil")
			if root != "" || len(lines) != 0 || crit != "" || len(rec.calls) != 0 {
				t.Fatalf("want no-op, got root=%q lines=%v crit=%q calls=%d", root, lines, crit, len(rec.calls))
			}
		})
	}
}

// A failed copy loses every mailbox of the account. It must come back as a
// critical (the job ends degraded), with MailRoot left unset.
func TestCpanelPullMail_FailedCopyIsCritical(t *testing.T) {
	rec := &mailLegRecorder{err: errors.New("rsync failed: exit status 23")}
	job := &models.MigrationJob{ID: "j", SourceKind: models.MigrationSourceWHMpkgacct, SourceHost: "203.0.113.5", SourceUser: "webup"}

	root, _, crit := cpanelPullMail(context.Background(), rec.call, job, "webup", "gil")

	if root != "" {
		t.Fatalf("MailRoot must stay unset after a failed copy, got %q", root)
	}
	if !strings.Contains(crit, "no mailboxes migrated") || !strings.Contains(crit, "exit status 23") {
		t.Fatalf("critical = %q, want the loss stated and the agent error kept", crit)
	}
}

// The account read from the pulled tarball must be the one the operator picked
// before a root rsync reads /home/<account>.
func TestCpanelPullMail_RefusesTarballAccountMismatch(t *testing.T) {
	rec := &mailLegRecorder{}
	job := &models.MigrationJob{ID: "j", SourceKind: models.MigrationSourceWHMpkgacct, SourceHost: "203.0.113.5", SourceUser: "webup"}

	root, _, crit := cpanelPullMail(context.Background(), rec.call, job, "otheracct", "gil")

	if root != "" || len(rec.calls) != 0 || !strings.Contains(crit, "does not match the job's source account") {
		t.Fatalf("want refused with no agent call, got root=%q calls=%d crit=%q", root, len(rec.calls), crit)
	}
}

func TestCpanelPullMail_RejectsBadSourceUser(t *testing.T) {
	for _, u := range []string{"", ".", "..", "../root", "a/b"} {
		t.Run(u, func(t *testing.T) {
			rec := &mailLegRecorder{}
			job := &models.MigrationJob{ID: "j", SourceKind: models.MigrationSourceCpanel, SourceHost: "203.0.113.5", SourceUser: u}
			root, _, crit := cpanelPullMail(context.Background(), rec.call, job, u, "gil")
			if root != "" || crit == "" || len(rec.calls) != 0 {
				t.Fatalf("source user %q: want refused with a critical and no agent call, got root=%q crit=%q calls=%d",
					u, root, crit, len(rec.calls))
			}
		})
	}
}
