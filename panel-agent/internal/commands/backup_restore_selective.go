// backup_restore_selective.go — GH #267 Wave 2 (tenant selective restore).
//
// DESTRUCTIVE, fail-closed. Restores a chosen subset (databases and/or the home
// directory) of an account backup's databases into the owner's existing DBs by reusing the exact
// proven apply path (applyAccountRestore) — but with the manifest stage list
// FILTERED to only the requested db stages, so home / mail / meta are never in
// the apply set and cannot be touched. That filtering is the whole selectivity
// guarantee: the negative round-trip (restore one DB, assert the others + home
// are byte-unchanged) is the ship gate.
//
// Scopes (see plans/m267-tenant-selective-restore.md):
//   - databases: drop+reload via applyAccountRestore with a db-only stage list.
//   - home: ADDITIVE rsync WITHOUT --delete — restores the backup files over the
//     live home but NEVER removes files created since the backup (safer than the
//     admin whole-account path, which mirrors with --delete).
//   - mail is NOT auto-appliable (stalwart-cli over a running spool corrupts
//     RocksDB) — the admin path refuses too; safe path is JMAP import (future).
//   - DNS managed records re-derive from domain config; custom records aren't
//     captured.
//
// Safety:
//   - overwrite MUST be true to write anything (DB restore is drop+reload). With
//     overwrite=false the handler applies NOTHING and reports what would change.
//   - target_username (server-derived by the panel from the caller) must equal
//     the manifest owner — defense-in-depth against a mismatched manifest.
//   - reuses the global LOCK_NB restore flock so it can't race an admin restore.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

type backupRestoreSelectiveParams struct {
	JobID              string   `json:"job_id"`
	ManifestSnapshotID string   `json:"manifest_snapshot_id"`
	TargetUsername     string   `json:"target_username"`
	Databases          []string `json:"databases"`
	Mailboxes          []string `json:"mailboxes"`
	RestoreHome        bool     `json:"home"`
	Overwrite          bool     `json:"overwrite"`
	// Restic dest (optional; empty = default local repo, like materialize).
	RepoURL        string            `json:"repo_url,omitempty"`
	CredentialsRef string            `json:"credentials_ref,omitempty"`
	PasswordFile   string            `json:"password_file,omitempty"`
	SFTP           *backupSFTPInputs `json:"sftp,omitempty"`
}

type backupRestoreSelectiveResult struct {
	JobID    string   `json:"job_id"`
	Applied  []string `json:"applied"`
	Skipped  []string `json:"skipped"`
	Warnings []string `json:"warnings"`
}

func backupRestoreSelectiveHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupRestoreSelectiveParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg("malformed JSON body")
	}
	if !ulidRE.MatchString(p.JobID) {
		return nil, bkInvalidArg("job_id must be a 26-char ULID")
	}
	if p.ManifestSnapshotID == "" {
		return nil, bkInvalidArg("manifest_snapshot_id required")
	}
	// Format-validate, not just non-empty: target_username reaches useradd
	// argv (a leading "-" parses as flags) and is joined into the rsync
	// destination path, where a "../" component would turn a root rsync
	// --delete into an arbitrary-directory mirror. Every sibling backup
	// handler already validates with this same regex.
	if !backupUsernameRE.MatchString(p.TargetUsername) {
		return nil, bkInvalidArg("target_username must match ^[a-z][a-z0-9_-]{0,31}$")
	}
	if len(p.Databases) == 0 && !p.RestoreHome && len(p.Mailboxes) == 0 {
		return nil, bkInvalidArg("select at least one database, mailbox, and/or home")
	}

	out := backupRestoreSelectiveResult{JobID: p.JobID}

	// Global restore flock — same lock the admin restore uses, so a tenant
	// selective restore can't run concurrently with any other restore.
	if err := os.MkdirAll(filepath.Dir(restoreLockPath), 0o750); err != nil {
		return nil, bkInternal("mkdir lock dir", err)
	}
	lf, err := os.OpenFile(restoreLockPath, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, bkInternal("open lock file", err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, ErrRestoreLocked
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	cfg, cerr := bkResticConfigWithPassword(p.RepoURL, p.CredentialsRef, p.PasswordFile, p.SFTP)
	if cerr != nil {
		return nil, bkInternal("restic config", cerr)
	}
	c := backup.New(cfg)

	manifestBytes, err := c.Dump(ctx, p.ManifestSnapshotID, "manifest.json")
	if err != nil {
		return nil, bkInternal("read manifest", err)
	}
	manifest, err := backup.AccountManifestFromBytes(manifestBytes)
	if err != nil {
		return nil, bkInternal("parse manifest", err)
	}

	// Defense-in-depth: the manifest's owner must match the server-derived
	// target. The panel already forces target_username from the caller and
	// 404s a cross-user job, but never apply onto a username the snapshot
	// wasn't taken for.
	if manifest.User.Username != p.TargetUsername {
		return nil, bkInvalidArg(fmt.Sprintf(
			"manifest owner %q != target_username %q — refusing",
			manifest.User.Username, p.TargetUsername))
	}

	// Build the requested set; filter the manifest to ONLY db stages whose
	// db name was requested. Anything requested but not in the backup is a
	// warning, not a failure.
	want := map[string]bool{}
	for _, d := range p.Databases {
		want[d] = true
	}
	inManifest := map[string]bool{}
	var dbStages []backup.ManifestStage
	for _, st := range manifest.Stages {
		if st.Name != backup.StageDB || st.Status != backup.StageStatusOK || len(st.Items) == 0 {
			continue
		}
		db := st.Items[0]
		inManifest[db] = true
		if want[db] {
			dbStages = append(dbStages, st)
		}
	}
	for _, d := range p.Databases {
		if !inManifest[d] {
			out.Warnings = append(out.Warnings, fmt.Sprintf("database %q is not in this backup — skipped", d))
		}
	}

	var mailStage *backup.ManifestStage
	if len(p.Mailboxes) > 0 {
		for i := range manifest.Stages {
			st := manifest.Stages[i]
			if st.Name == backup.StageMail && st.Status == backup.StageStatusOK && st.SnapshotID != "" {
				mailStage = &manifest.Stages[i]
				break
			}
		}
		if mailStage == nil {
			out.Warnings = append(out.Warnings, "mail is not in this backup — skipped")
		}
	}

	var homeStage *backup.ManifestStage
	if p.RestoreHome {
		for i := range manifest.Stages {
			st := manifest.Stages[i]
			if st.Name == backup.StageHome && st.Status == backup.StageStatusOK && st.SnapshotID != "" {
				homeStage = &manifest.Stages[i]
				break
			}
		}
		if homeStage == nil {
			out.Warnings = append(out.Warnings, "home directory is not in this backup — skipped")
		}
	}

	if len(dbStages) == 0 && homeStage == nil && mailStage == nil {
		out.Warnings = append(out.Warnings, "nothing selected matched the backup — nothing to do")
		return out, nil
	}

	// Fail-closed: restore overwrites live data. Do nothing unless overwrite.
	if !p.Overwrite {
		for _, st := range dbStages {
			out.Skipped = append(out.Skipped, st.Items[0])
		}
		if homeStage != nil {
			out.Skipped = append(out.Skipped, "home")
		}
		if mailStage != nil {
			for _, mb := range p.Mailboxes {
				out.Skipped = append(out.Skipped, "mail:"+mb)
			}
		}
		out.Warnings = append(out.Warnings,
			"overwrite=false: nothing applied. Restore overwrites live data; "+
				"re-send with overwrite=true to apply.")
		return out, nil
	}

	stagingRoot := filepath.Join("/var/lib/jabali-backups/restore-staging", p.JobID+"-sel")
	defer os.RemoveAll(stagingRoot)

	// Databases — reuse the proven applyAccountRestore with a db-only stage list.
	if len(dbStages) > 0 {
		var stageResults []backupRestoreStage
		for _, st := range dbStages {
			target := filepath.Join(stagingRoot, st.Name)
			if err := os.MkdirAll(target, 0o750); err != nil {
				stageResults = append(stageResults, backupRestoreStage{
					Name: st.Name, Status: backup.StageStatusFailed,
					Error: fmt.Sprintf("mkdir staging: %v", err),
				})
				continue
			}
			rerr := c.Restore(ctx, backup.RestoreOpts{SnapshotID: st.SnapshotID, Target: target})
			sr := backupRestoreStage{Name: st.Name, Status: backup.StageStatusOK}
			if rerr != nil {
				sr.Status = backup.StageStatusFailed
				sr.Error = rerr.Error()
			}
			stageResults = append(stageResults, sr)
		}
		applied, warnings := applyAccountRestore(ctx, stagingRoot, p.TargetUsername, manifest.User, dbStages, stageResults)
		out.Applied = append(out.Applied, applied...)
		out.Warnings = append(out.Warnings, warnings...)
	}

	// Home — ADDITIVE rsync (NO --delete): restores backup files over the live
	// home but never removes files created since the backup (unlike the admin
	// whole-account restore, which mirrors with --delete). overwrite-gated above.
	if homeStage != nil {
		if hwarn := applySelectiveHome(ctx, stagingRoot, p.TargetUsername, homeStage, c); hwarn != "" {
			out.Warnings = append(out.Warnings, hwarn)
		} else {
			out.Applied = append(out.Applied, "home → /home/"+p.TargetUsername+" (additive, no delete)")
		}
	}

	// Mail — ADDITIVE + idempotent: restic-restore the mail stage's Maildir
	// tree, then JMAP-import each requested mailbox via the proven
	// migration import (importOneMailbox dedupes by Message-ID, so existing
	// messages are skipped and only lost ones come back — never duplicates,
	// never deletes; the RocksDB-unsafe store apply is NOT used).
	if mailStage != nil {
		mailApplied, mailWarn := applySelectiveMail(ctx, stagingRoot, p.Mailboxes, mailStage, c)
		out.Applied = append(out.Applied, mailApplied...)
		out.Warnings = append(out.Warnings, mailWarn...)
	}

	return out, nil
}

// applySelectiveMail restic-restores the mail stage to staging and imports each
// requested mailbox's Maildir into running Stalwart via JMAP. Additive +
// idempotent (Message-ID dedup in importOneMailbox).
func applySelectiveMail(ctx context.Context, stagingRoot string, mailboxes []string, st *backup.ManifestStage, c *backup.Client) ([]string, []string) {
	var applied, warnings []string
	want := map[string]bool{}
	for _, mb := range mailboxes {
		want[mb] = true
	}
	target := filepath.Join(stagingRoot, "mail")
	if err := os.MkdirAll(target, 0o750); err != nil {
		return nil, []string{"mail: mkdir staging: " + err.Error()}
	}
	if err := c.Restore(ctx, backup.RestoreOpts{SnapshotID: st.SnapshotID, Target: target}); err != nil {
		return nil, []string{"mail: restic restore: " + err.Error()}
	}
	// Walk the restored tree for Maildirs: any dir containing a "cur" child is
	// a maildir; its basename is the local part and its parent's basename is
	// the domain (layout <...>/mail/<domain>/<local>/{cur,new,tmp}).
	found := map[string]string{} // email -> maildir path
	_ = filepath.Walk(target, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.IsDir() || info.Name() != "cur" {
			return nil
		}
		maildir := filepath.Dir(path)
		local := filepath.Base(maildir)
		domain := filepath.Base(filepath.Dir(maildir))
		email := local + "@" + domain
		if want[email] {
			found[email] = maildir
		}
		return nil
	})
	for _, mb := range mailboxes {
		md, ok := found[mb]
		if !ok {
			warnings = append(warnings, "mail "+mb+": not found in this backup — skipped")
			continue
		}
		// GH #954: scope filesafe to this restore's staging dir (see
		// backup_restore.go) — the tree lives under restore-staging, not a
		// migration root, so migrationStagingRoots refused every open.
		n, _, _, ierr := importOneMailbox(ctx, mb, md, []string{stagingRoot})
		if ierr != nil {
			warnings = append(warnings, "mail "+mb+": import: "+ierr.Error())
			continue
		}
		applied = append(applied, fmt.Sprintf("mail → %s (%d message(s) restored; existing deduped)", mb, n))
	}
	return applied, warnings
}

// applySelectiveHome restores the home stage additively. Returns "" on success
// or a warning string. NO --delete: a file the tenant created after the backup
// stays put; the backup's files are written over the top.
func applySelectiveHome(ctx context.Context, stagingRoot, username string, st *backup.ManifestStage, c *backup.Client) string {
	target := filepath.Join(stagingRoot, "home")
	if err := os.MkdirAll(target, 0o750); err != nil {
		return "home: mkdir staging: " + err.Error()
	}
	if err := c.Restore(ctx, backup.RestoreOpts{SnapshotID: st.SnapshotID, Target: target}); err != nil {
		return "home: restic restore: " + err.Error()
	}
	u, uerr := user.Lookup(username)
	if uerr != nil {
		return "home: user lookup: " + uerr.Error()
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	// Restic preserves the absolute path: staged tree at target/home/<user>/.
	src := filepath.Join(target, "home", username) + "/"
	dst := "/home/" + username + "/"
	if _, err := os.Stat(filepath.Clean(src)); err != nil {
		return "home: staged source missing: " + err.Error()
	}
	// -aH not -aHAX: skip untrusted ACL/xattr restore from the repo;
	// owner/mode are re-normalized below (Gitea #462).
	if err := exec.CommandContext(ctx, "rsync", "-aH", src, dst).Run(); err != nil {
		return "home: rsync: " + err.Error()
	}
	if err := chownTreeRecursive(dst, uid, gid); err != nil {
		return "home: chown: " + err.Error()
	}
	if err := restoreDocrootGroup(username); err != nil {
		return "home: docroot group: " + err.Error()
	}
	return ""
}

func init() {
	Default.Register("backup.restore_selective", backupRestoreSelectiveHandler)
}
