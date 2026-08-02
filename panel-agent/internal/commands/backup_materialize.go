// M30.1 follow-up — agent-side materialize for the admin "Download"
// button on backup_jobs. panel-api runs as the jabali user and can't
// read /etc/jabali-panel/restic-repo.password (root:root 0600) or the
// repo dir (/var/lib/jabali-backups/repo, root:root 0700), so the
// `restic restore` step has to happen here as root.
//
// Flow:
//  1. panel-api calls backup.materialize → agent restores snapshot to
//     /var/lib/jabali-backups/downloads/<job_id>/ as root:jabali 0750.
//  2. panel-api streams `tar -I zstd -cf -` over the restored dir to
//     the HTTP client (jabali user can read because of the group).
//  3. panel-api calls backup.materialize_cleanup → agent rm -rf's the
//     dir to free the disk.
//
// The download root is created lazily on first call. install.sh
// guarantees the parent /var/lib/jabali-backups exists; the per-job
// subdir is created here. All ULIDs are validated to keep the path
// from escaping the root.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// downloadRoot is the parent directory for materialized snapshots.
// One subdir per job_id; lifetime = duration of the HTTP download.
const downloadRoot = "/var/lib/jabali-backups/downloads"

// jobIDRE constrains the subdir name to a 26-char Crockford ULID so
// path-traversal via crafted job_id is structurally impossible.
var jobIDRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

type backupMaterializeParams struct {
	JobID string `json:"job_id"`
	// SnapshotID is accepted for compatibility but ignored — the
	// handler always materializes ALL snapshots tagged job-id=<JobID>
	// because the manifest snapshot alone holds only manifest.json
	// (the real data lives in sibling stage=home/db/mail snapshots).
	SnapshotID     string            `json:"snapshot_id,omitempty"`
	RepoURL        string            `json:"repo_url,omitempty"`
	PasswordFile   string            `json:"password_file,omitempty"`
	CredentialsRef string            `json:"credentials_ref,omitempty"`
	SFTP           *backupSFTPInputs `json:"sftp,omitempty"`
}

type backupMaterializeResult struct {
	Path   string   `json:"path"`
	Stages []string `json:"stages"`
}

func backupMaterializeHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupMaterializeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid_arg: %w", err)
	}
	if !jobIDRE.MatchString(p.JobID) {
		return nil, fmt.Errorf("invalid_arg: job_id must be a 26-char ULID")
	}
	if err := os.MkdirAll(downloadRoot, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir downloadRoot: %w", err)
	}
	// /var/lib/jabali-backups is created by install.sh as 0700 root:root
	// — fine for the repo, fatal for downloads which the jabali user
	// (panel-api) needs to traverse to tar the materialized tree. Open
	// just the traversal bit (g+x) without exposing repo contents
	// (the repo dir itself stays 0700). chgrp gives the directory the
	// jabali group so o-x stays closed.
	if err := exec.Command("chgrp", "jabali", filepath.Dir(downloadRoot)).Run(); err != nil {
		return nil, fmt.Errorf("chgrp /var/lib/jabali-backups: %w", err)
	}
	if err := os.Chmod(filepath.Dir(downloadRoot), 0o750); err != nil {
		return nil, fmt.Errorf("chmod /var/lib/jabali-backups: %w", err)
	}
	if err := exec.Command("chgrp", "jabali", downloadRoot).Run(); err != nil {
		return nil, fmt.Errorf("chgrp downloadRoot: %w", err)
	}
	if err := os.Chmod(downloadRoot, 0o750); err != nil {
		return nil, fmt.Errorf("chmod downloadRoot: %w", err)
	}
	target := filepath.Join(downloadRoot, p.JobID)
	// Clean any stale dir from a prior failed download so restic doesn't
	// merge into it (restic refuses to clobber existing files).
	_ = os.RemoveAll(target)
	if err := os.MkdirAll(target, 0o750); err != nil {
		return nil, fmt.Errorf("mkdir target: %w", err)
	}

	cfg, cerr := bkResticConfigWithPassword(p.RepoURL, p.CredentialsRef, p.PasswordFile, p.SFTP)
	if cerr != nil {
		return nil, fmt.Errorf("restic config: %w", cerr)
	}
	c := backup.New(cfg)
	snaps, err := c.Snapshots(ctx, []backup.Tag{backup.MakeTag(backup.TagKeyJobID, p.JobID)})
	if err != nil {
		return nil, fmt.Errorf("list job snapshots: %w", err)
	}
	if len(snaps) == 0 {
		return nil, fmt.Errorf("no snapshots for job %s", p.JobID)
	}
	stages := make([]string, 0, len(snaps))
	for _, s := range snaps {
		stage := stageFromTags(s.Tags)
		// Restic snapshot tags are repository-controlled data. A poisoned
		// remote backup repo could attach stage=../../etc or an absolute
		// path and, via filepath.Join(target, stage) below, make the root
		// restore write outside the download staging tree. Accept only a
		// flat [A-Za-z0-9_-] stage name; anything else (path separators,
		// "..", control chars, empty) falls back to the snapshot short ID,
		// which is hex and always safe (Gitea #464).
		if !materializeStageNameRe.MatchString(stage) {
			stage = s.ID[:8]
		}
		// Restore each stage into a sibling subdir so the final
		// archive layout is <job_id>/<stage>/<paths>. Avoids
		// path collisions when /home/<u>/ + /manifest.json + db
		// dumps would otherwise overlap at the same target root.
		stageTarget := filepath.Join(target, stage)
		if err := os.MkdirAll(stageTarget, 0o750); err != nil {
			return nil, fmt.Errorf("mkdir stage %s: %w", stage, err)
		}
		if err := c.Restore(ctx, backup.RestoreOpts{
			SnapshotID: s.ID,
			Target:     stageTarget,
		}); err != nil {
			return nil, fmt.Errorf("restic restore %s (%s): %w", stage, s.ID[:8], err)
		}
		stages = append(stages, stage)
	}
	// Hand off read access to the jabali user (panel-api). Group
	// `jabali` exists since M9 (per-user-slices baseline). chmod -R
	// keeps directories traversable + files readable. exec'ing chown
	// is cheaper than walking the tree in Go for typical home dirs.
	if err := exec.Command("chgrp", "-R", "jabali", target).Run(); err != nil {
		return nil, fmt.Errorf("chgrp jabali: %w", err)
	}
	if err := exec.Command("chmod", "-R", "g+rX", target).Run(); err != nil {
		return nil, fmt.Errorf("chmod g+rX: %w", err)
	}
	if err := os.Chmod(target, 0o750); err != nil {
		return nil, fmt.Errorf("chmod target: %w", err)
	}
	return backupMaterializeResult{Path: target, Stages: stages}, nil
}

// materializeStageNameRe whitelists a flat restic stage tag used as a
// filesystem path component under the download staging tree (Gitea #464).
var materializeStageNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// stageFromTags returns the stage name (e.g. "home") if a `stage=X`
// tag is present on the snapshot. Empty when no stage tag matches —
// falls back to the snapshot short ID upstream.
func stageFromTags(tags []string) string {
	for _, t := range tags {
		if len(t) > len("stage=") && t[:len("stage=")] == "stage=" {
			s := t[len("stage="):]
			// stage=db,db=foo collapses to "db" — anything past the
			// first comma is metadata, not the stage name.
			for i := 0; i < len(s); i++ {
				if s[i] == ',' {
					return s[:i]
				}
			}
			return s
		}
	}
	return ""
}

type backupMaterializeCleanupParams struct {
	JobID string `json:"job_id"`
}

func backupMaterializeCleanupHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupMaterializeCleanupParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid_arg: %w", err)
	}
	if !jobIDRE.MatchString(p.JobID) {
		return nil, fmt.Errorf("invalid_arg: job_id must be a 26-char ULID")
	}
	target := filepath.Join(downloadRoot, p.JobID)
	if err := os.RemoveAll(target); err != nil {
		return nil, fmt.Errorf("rm -rf %s: %w", target, err)
	}
	return map[string]string{"status": "ok"}, nil
}

func init() {
	Default.Register("backup.materialize", backupMaterializeHandler)
	Default.Register("backup.materialize_cleanup", backupMaterializeCleanupHandler)
}
