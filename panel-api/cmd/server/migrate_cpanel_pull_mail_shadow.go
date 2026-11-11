package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// cpanelMailShadowDir is where the agent's migration.pull_mail_shadow stages
// a job's copied mail password files.
func cpanelMailShadowDir(jobID string) string {
	return filepath.Join("/var/lib/jabali-migrations", jobID, "mail-shadow")
}

// cpanelPullMailShadow copies the source account's per-domain mail password
// files (<home>/etc/<domain>/shadow) for a cPanel or WHM SSH pull, so the
// mailbox import can keep each mailbox's source password. The pull runs
// pkgacct --skiphomedir, which leaves them out of the tarball.
//
// It runs only when the operator chose to preserve credentials: otherwise
// the hashes are not used, so they are not copied. An offline restore
// (SourceHost == "") reads them from the cpmove's homedir/etc instead. A
// failure is a warning, not a critical: mail still migrates, and the import
// names every mailbox that got a new password and why.
//
// Returns the staged dir ("" when nothing was staged), the domains whose
// password file the source account could not read, run-summary lines
// (counts and domain names only), and whether a copy that should have
// happened did not (notCopied).
func cpanelPullMailShadow(ctx context.Context, call agentCallFunc, job *models.MigrationJob, srcUser string, preserveCreds bool) (dir string, unreadable []string, lines []string, notCopied bool) {
	if job == nil || job.SourceHost == "" || !preserveCreds {
		return "", nil, nil, false
	}
	if job.SourceKind != models.MigrationSourceCpanel && job.SourceKind != models.MigrationSourceWHMpkgacct {
		return "", nil, nil, false
	}
	// Same checks as the ~/mail leg (cpanelPullMail), which already raised
	// the critical for a bad or mismatched account.
	if srcUser == "" || srcUser == "." || srcUser == ".." || strings.ContainsAny(srcUser, "/\x00") || srcUser != job.SourceUser {
		return "", nil, []string{"mail_passwords: source mail password files not copied (source account not verified) — every mailbox gets a new password"}, true
	}
	raw, err := call(ctx, "migration.pull_mail_shadow", map[string]any{
		"port":              srcSSHPort(job),
		"job_id":            job.ID,
		"host":              job.SourceHost,
		"ssh_user":          migrationSSHUser(job),
		"secret_path":       fmt.Sprintf("/etc/jabali-panel/migration-secrets/%s.env", job.ID),
		"expected_host_key": job.ExpectedHostKey,
		"src_account":       srcUser,
		"src_home":          "/home/" + srcUser,
	})
	if err != nil {
		return "", nil, []string{fmt.Sprintf(
			"mail_passwords: copying the source's mail password files failed — every mailbox gets a new password: %v", err)}, true
	}
	var rs struct {
		Domains    int      `json:"domains"`
		Files      int      `json:"files"`
		Bytes      int64    `json:"bytes"`
		Removed    int      `json:"removed"`
		Unreadable []string `json:"unreadable"`
	}
	_ = json.Unmarshal(raw, &rs)
	lines = []string{fmt.Sprintf(
		"mail_passwords: copied %d domain password file(s) (%d bytes) from the source", rs.Files, rs.Bytes)}
	if rs.Removed > 0 {
		lines = append(lines, fmt.Sprintf(
			"mail_passwords: %d entr(ies) other than a domain's password file were dropped from the copy", rs.Removed))
	}
	for _, d := range rs.Unreadable {
		d = domainops.NormalizeDomainName(d)
		if domainops.ValidateDomainName(d) == nil {
			unreadable = append(unreadable, d)
		}
	}
	if len(unreadable) > 0 {
		lines = append(lines, fmt.Sprintf(
			"mail_passwords: the source account cannot read the password file of %s — those mailboxes get new passwords; fixing the files' ownership on the source and re-running would carry them",
			strings.Join(unreadable, ", ")))
	}
	if note := rsyncPartialNote("mail_passwords", raw); note != "" {
		lines = append(lines, note)
	}
	return cpanelMailShadowDir(job.ID), unreadable, lines, false
}

// dropMailShadow removes the staged mail password files once the mailbox
// import is over, whatever its outcome, and clears the pointer. Returns a
// summary line only when the removal failed: the dir holds password hashes.
func dropMailShadow(parsed *cpanel.ParsedTarball) string {
	if parsed == nil || parsed.MailShadowDir == "" {
		return ""
	}
	dir := parsed.MailShadowDir
	parsed.MailShadowDir = ""
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Sprintf("mail_passwords: could not remove the staged copy %s: %v — it holds password hashes; remove it by hand", dir, err)
	}
	return ""
}
