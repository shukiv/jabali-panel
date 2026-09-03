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
	// GH #1408 tenant self-restore: when Mode=="tenant" the uploaded tar is
	// untrusted, so the panel supplies the caller's OWNED resources and the
	// agent skips anything else. AllowedDBNames / AllowedMailDomains: nil =
	// unrestricted (admin); non-nil (even empty) = enforce. Mode=="tenant"
	// REQUIRES both lists (fail-closed) and additionally drops docker.
	Mode               string   `json:"mode,omitempty"`
	AllowedDBNames     []string `json:"allowed_db_names,omitempty"`
	AllowedMailDomains []string `json:"allowed_mail_domains,omitempty"`
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
	// GH #1408 fail-closed ack: a tenant restore's panel handler REQUIRES these
	// to be true. An agent predating the allowlist feature never sets them, so
	// the panel rejects the restore rather than running it unrestricted.
	DBAllowlistEnforced   bool `json:"db_allowlist_enforced"`
	MailAllowlistEnforced bool `json:"mail_allowlist_enforced"`
}

func backupRestoreFromTarHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupRestoreFromTarParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("invalid params: %v", err))
	}
	apply := true
	if p.ApplyStaged != nil {
		apply = *p.ApplyStaged
	}
	enf := restoreEnforcement{
		Mode:               p.Mode,
		AllowedDBNames:     p.AllowedDBNames,
		AllowedMailDomains: p.AllowedMailDomains,
	}
	// Belt-and-suspenders: a tenant restore MUST carry both allowlists (a nil
	// list means "unrestricted", so a caller that forgot one would restore
	// wide-open). Refuse rather than run partially-gated.
	if enf.Mode == "tenant" && (enf.AllowedDBNames == nil || enf.AllowedMailDomains == nil) {
		return nil, bkInvalidArg("mode=tenant requires allowed_db_names and allowed_mail_domains (may be empty, not null)")
	}
	return restoreAccountFromTar(ctx, p.JobID, p.TarPath, p.TargetUsername, p.Components, apply, enf)
}

// restoreAccountFromTar is the reusable core: extract an untrusted account backup
// tar and apply it to targetUsername. Shared by the single-upload restore handler
// and the full-server container restore (which calls it once per inner user tar).
func restoreAccountFromTar(ctx context.Context, jobID, tarPath, targetUsername string, components []string, apply bool, enf restoreEnforcement) (*backupRestoreFromTarResult, error) {
	if !jobIDRE.MatchString(jobID) {
		return nil, bkInvalidArg("job_id must be a 26-char ULID")
	}
	// GH #1408: a tenant self-restore is limited to the audited-safe components;
	// docker/dns are never applied from an untrusted self-service upload.
	if enf.Mode == "tenant" {
		components = intersectComponents(components, []string{"home", "db", "mail"})
		if len(components) == 0 {
			// The caller asked only for components a self-service restore can't
			// apply. An EMPTY list means "all" downstream (componentFilter), so
			// substitute a sentinel that matches no stage — apply nothing rather
			// than fall open to a full restore.
			components = []string{"__none__"}
		}
	}
	if !backupUsernameRE.MatchString(targetUsername) {
		return nil, bkInvalidArg("target_username must match ^[a-z][a-z0-9_-]{0,31}$")
	}
	// The tar path MUST live under the uploads handoff dir and contain no
	// traversal — never let the caller point the extractor at an arbitrary file.
	tarClean := filepath.Clean(tarPath)
	if tarClean != tarPath || !strings.HasPrefix(tarClean, restoreUploadsRoot+"/") {
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

	staging := filepath.Join("/var/lib/jabali-backups/restore-staging", jobID+"-upload")
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

	out := backupRestoreFromTarResult{JobID: jobID, User: manifest.User, BytesExtracted: written}
	// Echo which allowlists this agent enforced so the panel's tenant handler can
	// fail closed against an agent that predates the feature (it never sets these).
	out.DBAllowlistEnforced = enf.enforceDB()
	out.MailAllowlistEnforced = enf.enforceMail()

	// v1 restores into the SAME username the backup was taken from — the home
	// tree inside the archive is /home/<backup-user> and applyAccountRestore
	// keys the source path by the target name, so a mismatch silently leaves
	// home unrestored. The panel sets target = the manifest's username; warn if
	// a caller passes something else (renaming on restore is a follow-up).
	if targetUsername != manifest.User.Username {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"target_username %q differs from the backup's user %q; the home stage will be skipped (restore into the same username)",
			targetUsername, manifest.User.Username))
	}

	// Best-effort metadata (FTP subaccount hashes etc.) from the meta stage.
	if mb, mErr := os.ReadFile(filepath.Join(root, "meta", "metadata.json")); mErr == nil {
		out.Metadata = mb
	}

	// Per-stage gate: a stage is applied only when it materialized in the tar AND
	// (no component filter, or the filter selected it). Aligned by manifest-stage
	// index — same discipline as backup.restore (docker/db fan out same-named
	// stages, so a name-keyed gate would collapse them, GH #1360).
	want := componentFilter(components)
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

	if !apply {
		out.Warnings = append(out.Warnings, "apply_staged=false — extracted to "+staging+"; nothing applied")
		out.StagingCleanup = "kept (recon mode)"
		return &out, nil
	}

	applied, warnings := applyAccountRestore(ctx, root, targetUsername, manifest.User, manifest.Stages, stageResults, enf)
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
	return &out, nil
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

// intersectComponents restricts a requested component set to an allowed set. An
// EMPTY request means "all", so it becomes exactly the allowed set (not empty) —
// this is what keeps a tenant restore that asked for nothing-in-particular from
// falling through to an unrestricted all-stages apply. GH #1408.
func intersectComponents(requested, allowed []string) []string {
	if len(requested) == 0 {
		return allowed
	}
	allow := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allow[a] = true
	}
	out := make([]string, 0, len(requested))
	for _, r := range requested {
		if allow[strings.TrimSpace(r)] {
			out = append(out, r)
		}
	}
	return out
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

// backup.inspect_uploaded_tar — GH #1408. Read ONLY the manifest from an
// uploaded archive (no full extraction) so the panel can show what the backup
// contains + which components can be restored, before the admin commits to the
// destructive apply. Cheap: it streams the zstd tar and stops at the manifest.
type backupInspectUploadedTarParams struct {
	TarPath string `json:"tar_path"`
}

type backupInspectUploadedTarResult struct {
	User       backup.ManifestUser `json:"user"`
	Components []string            `json:"components"`
	// AllowlistSupported is always true on an agent that has the GH #1408 tenant
	// enforcement. The tenant restore handler gates on it BEFORE applying, so an
	// older agent (which never sets it) can't run an unrestricted tenant restore.
	AllowlistSupported bool `json:"allowlist_supported"`
}

func backupInspectUploadedTarHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupInspectUploadedTarParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("invalid params: %v", err))
	}
	tarClean := filepath.Clean(p.TarPath)
	if tarClean != p.TarPath || !strings.HasPrefix(tarClean, restoreUploadsRoot+"/") {
		return nil, bkInvalidArg("tar_path must be a clean path under " + restoreUploadsRoot)
	}
	if fi, err := os.Lstat(tarClean); err != nil || !fi.Mode().IsRegular() {
		return nil, bkInvalidArg("tar_path is not a regular file")
	}

	manifestBytes, err := readManifestFromZstdTar(ctx, tarClean)
	if err != nil {
		return nil, bkInvalidArg("archive inspect failed: " + err.Error())
	}
	manifest, err := backup.AccountManifestFromBytes(manifestBytes)
	if err != nil {
		return nil, bkInvalidArg("manifest parse failed: " + err.Error())
	}
	// Components the UI can offer = the distinct restorable stage names in the
	// manifest (manifest/meta are internal). Apply re-gates on actual presence.
	comps := make([]string, 0, len(manifest.Stages))
	seen := map[string]bool{}
	for _, st := range manifest.Stages {
		if st.Name == "" || st.Name == "manifest" || st.Name == "meta" || seen[st.Name] {
			continue
		}
		comps = append(comps, st.Name)
		seen[st.Name] = true
	}
	return backupInspectUploadedTarResult{User: manifest.User, Components: comps, AllowlistSupported: true}, nil
}

func init() {
	Default.Register("backup.restore_from_tar", backupRestoreFromTarHandler)
	Default.Register("backup.inspect_uploaded_tar", backupInspectUploadedTarHandler)
}
