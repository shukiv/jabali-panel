package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
)

// backup.restore_from_tar — GH #1408. Restore an account from a re-UPLOADED
// backup archive (the .tar the admin downloaded earlier), instead of a restic
// snapshot. This is the DR / cross-server-migration path: reinstall a box,
// upload a user's backup, restore.
//
// The uploaded tar is untrusted, so it is extracted through safeExtractZstdTar
// (allowlist: regular/dir/in-tree-symlink; no ../ absolute hardlink device;
// bomb-bounded). The extracted tree has the SAME per-stage layout a restic
// restore produces (materialize built the downloaded tar from those very
// snapshots), so once staged it reuses the exact same applyAccountRestore path
// as backup.restore — home rsync + chown, per-DB mariadb load. mail is not
// auto-applied (same as backup.restore).
//
// v1 scope (see GH #1408): admin-only, one tar = one account, target user must
// already exist (the panel enforces this). Component selection gates which
// manifest stages are applied.

const restoreUploadsRoot = "/var/lib/jabali-uploads"

type backupRestoreFromTarParams struct {
	JobID          string `json:"job_id"`
	TarPath        string `json:"tar_path"`
	TargetUsername string `json:"target_username"`
	// Components names the manifest stages to apply (e.g. "home","db","dns").
	// Empty = apply every stage present (a full account restore).
	Components []string `json:"components,omitempty"`
	// ApplyStaged=false stages the extracted tree without touching the live
	// system (recon), mirroring backup.restore.
	ApplyStaged *bool `json:"apply_staged,omitempty"`
}

type backupRestoreFromTarResult struct {
	JobID          string               `json:"job_id"`
	User           backup.ManifestUser  `json:"user"`
	Stages         []backupRestoreStage `json:"stages"`
	Applied        []string             `json:"applied,omitempty"`
	Warnings       []string             `json:"warnings,omitempty"`
	Metadata       json.RawMessage      `json:"metadata,omitempty"`
	StagingCleanup string               `json:"staging_cleanup,omitempty"`
	BytesExtracted int64                `json:"bytes_extracted"`
}

func backupRestoreFromTarHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupRestoreFromTarParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("invalid params: %v", err))
	}
	if !jobIDRE.MatchString(p.JobID) {
		return nil, bkInvalidArg("job_id must be a 26-char ULID")
	}
	if !backupUsernameRE.MatchString(p.TargetUsername) {
		return nil, bkInvalidArg("target_username must match ^[a-z][a-z0-9_-]{0,31}$")
	}
	// The tar path is provided by the panel (the reassembled chunked upload). It
	// MUST live under the uploads handoff dir and contain no traversal — never
	// let the caller point the extractor at an arbitrary file.
	tarClean := filepath.Clean(p.TarPath)
	if tarClean != p.TarPath || !strings.HasPrefix(tarClean, restoreUploadsRoot+"/") {
		return nil, bkInvalidArg("tar_path must be a clean path under " + restoreUploadsRoot)
	}
	if fi, err := os.Lstat(tarClean); err != nil || !fi.Mode().IsRegular() {
		return nil, bkInvalidArg("tar_path is not a regular file")
	}

	// Refuse to stage under the host disk reserve (same guard the DB/PG restore
	// paths use) so a large archive can't wedge the box.
	if err := hostreserve.CheckReserve("/var/lib/jabali-backups", 0); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeUnavailable,
			Message: "restore staging is under the host disk reserve: " + err.Error(),
		}
	}

	staging := filepath.Join("/var/lib/jabali-backups/restore-staging", p.JobID+"-upload")
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o750); err != nil {
		return nil, bkInternal("mkdir staging", err)
	}

	written, err := safeExtractZstdTar(ctx, tarClean, staging)
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, bkInvalidArg("archive rejected: " + err.Error())
	}

	// The downloaded archive is tarred as `<source-job-id>/<stage>/…` (the panel
	// runs `tar -C <downloads> <job-id>`), so the extracted tree has a single
	// job-id dir on top. Descend into it — that dir IS the per-stage staging
	// root applyAccountRestore expects (identical to backup.restore's staging).
	root, rerr := resolveExtractedRoot(staging)
	if rerr != nil {
		_ = os.RemoveAll(staging)
		return nil, bkInvalidArg(rerr.Error())
	}

	// manifest lives under the manifest stage: <root>/manifest/manifest.json.
	manifestBytes, err := os.ReadFile(filepath.Join(root, "manifest", "manifest.json"))
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, bkInvalidArg("archive has no manifest/manifest.json (not a Jabali account backup)")
	}
	manifest, err := backup.AccountManifestFromBytes(manifestBytes)
	if err != nil {
		_ = os.RemoveAll(staging)
		return nil, bkInvalidArg("manifest parse failed: " + err.Error())
	}

	out := backupRestoreFromTarResult{JobID: p.JobID, User: manifest.User, BytesExtracted: written}

	// v1 restores into the SAME username the backup was taken from — the home
	// tree inside the archive is /home/<backup-user> and applyAccountRestore
	// keys the source path by the target name, so a mismatch silently leaves
	// home unrestored. The panel sets target = the manifest's username; warn if
	// a caller passes something else (renaming on restore is a follow-up).
	if p.TargetUsername != manifest.User.Username {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"target_username %q differs from the backup's user %q; the home stage will be skipped (restore into the same username)",
			p.TargetUsername, manifest.User.Username))
	}

	// Best-effort metadata (FTP subaccount hashes etc.) from the meta stage.
	if mb, mErr := os.ReadFile(filepath.Join(root, "meta", "metadata.json")); mErr == nil {
		out.Metadata = mb
	}

	// Per-stage gate: a stage is applied only when it materialized in the tar AND
	// (no component filter, or the filter selected it). Aligned by manifest-stage
	// index — same discipline as backup.restore (docker/db fan out same-named
	// stages, so a name-keyed gate would collapse them, GH #1360).
	want := componentFilter(p.Components)
	stageResults := make([]backupRestoreStage, len(manifest.Stages))
	for i, st := range manifest.Stages {
		res := backupRestoreStage{Name: st.Name, Status: backup.StageStatusSkipped}
		selected := want == nil || want[st.Name]
		if selected && stageMaterializedInTar(root, st.Name) {
			res.Status = backup.StageStatusOK
		}
		stageResults[i] = res
	}
	out.Stages = stageResults

	apply := true
	if p.ApplyStaged != nil {
		apply = *p.ApplyStaged
	}
	if !apply {
		out.Warnings = append(out.Warnings, "apply_staged=false — extracted to "+staging+"; nothing applied")
		out.StagingCleanup = "kept (recon mode)"
		return out, nil
	}

	applied, warnings := applyAccountRestore(ctx, root, p.TargetUsername, manifest.User, manifest.Stages, stageResults)
	out.Applied = applied
	out.Warnings = append(out.Warnings, warnings...)

	if len(applied) > 0 {
		if rmErr := os.RemoveAll(staging); rmErr != nil {
			out.StagingCleanup = "cleanup_failed: " + rmErr.Error()
			out.Warnings = append(out.Warnings, "staging cleanup failed: "+rmErr.Error())
		} else {
			out.StagingCleanup = "removed"
		}
	} else {
		out.StagingCleanup = "kept (no stages applied)"
	}
	return out, nil
}

// componentFilter builds a set of stage names to apply, or nil to apply all.
func componentFilter(components []string) map[string]bool {
	if len(components) == 0 {
		return nil
	}
	m := make(map[string]bool, len(components))
	for _, c := range components {
		if c = strings.TrimSpace(c); c != "" {
			m[c] = true
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

// resolveExtractedRoot returns the per-stage staging root inside the extracted
// tree. The downloaded archive wraps everything in a single <source-job-id>
// directory (the panel runs `tar -C <downloads> <job-id>`), so the real root is
// that lone child. A defensively un-prefixed archive (manifest directly under
// staging) returns staging itself. Anything else is not a Jabali account backup.
func resolveExtractedRoot(staging string) (string, error) {
	if fi, err := os.Stat(filepath.Join(staging, "manifest", "manifest.json")); err == nil && fi.Mode().IsRegular() {
		return staging, nil
	}
	ents, err := os.ReadDir(staging)
	if err != nil {
		return "", fmt.Errorf("read extracted tree: %v", err)
	}
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 1 {
		root := filepath.Join(staging, dirs[0])
		if fi, err := os.Stat(filepath.Join(root, "manifest", "manifest.json")); err == nil && fi.Mode().IsRegular() {
			return root, nil
		}
	}
	return "", fmt.Errorf("archive is not a Jabali account backup (no manifest/manifest.json)")
}

// stageMaterializedInTar reports whether the extracted tree carries this stage's
// directory (the tar is a merge of the per-stage snapshots, so stage <name>
// lands at staging/<name>/).
func stageMaterializedInTar(staging, name string) bool {
	if name == "" {
		return false
	}
	fi, err := os.Stat(filepath.Join(staging, name))
	return err == nil && fi.IsDir()
}

func init() {
	Default.Register("backup.restore_from_tar", backupRestoreFromTarHandler)
}
