// Step 7 of M30: backup.restore agent command. Reads the manifest
// snapshot, resolves sibling stage snapshots by job-id, restores each
// in order. Stage failures are recorded; only fatal errors (lock
// contention, manifest unreadable) abort the whole run.
//
// Concurrency gate: a single global flock at
// /var/lib/jabali-backups/.restore.lock prevents parallel restores
// from racing nginx reload + PowerDNS NOTIFY + MariaDB DDL.
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dbreserve"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/fsperm"
)

const restoreLockPath = "/var/lib/jabali-backups/.restore.lock"

// restoreHomeRoot is where account homes live; tests point it elsewhere.
var restoreHomeRoot = "/home"

// restoreDBNameRe is the canonical database-identifier policy shared with
// db.create / db.drop (^[a-zA-Z][a-zA-Z0-9_-]{0,63}$). Restore reads the
// database name from the backup manifest — repository-controlled data — and
// interpolates it into privileged psql/createdb/mariadb commands. A poisoned
// manifest must not be able to inject SQL or shell-meaningful identifiers, so
// every manifest db name is validated against this whitelist before use
// (Gitea #463).
var restoreDBNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// restoreEnforcement gates which manifest-named resources an account restore may
// touch. It exists for the TENANT self-service restore (GH #1408), where the
// uploaded tar is untrusted: the panel passes the caller's OWNED database names
// and mail domains, and the agent skips any db/mail stage naming something the
// caller doesn't own — a crafted tar can't `mariadb jabali_panel < …` or inject
// into another tenant's mailbox. Admin restores pass the zero value (Mode="",
// nil lists) and stay unrestricted.
//
// Semantics of the lists: nil = unrestricted; non-nil (EVEN EMPTY) = enforce
// (empty → every stage of that kind is skipped). Mode=="tenant" additionally
// drops docker (not gateable per-account here) and is the flag the panel keys
// its fail-closed version check on.
type restoreEnforcement struct {
	Mode               string
	AllowedDBNames     []string
	AllowedMailDomains []string

	// Upload mode (GH #1993), see restoreModeUpload. AllowedDBNames holds the
	// target account's own databases.
	DBPrefix               string   // "<target>_": the only name a NEW database may take
	ForeignDBNames         []string // databases another account owns on this server
	OwnedDockerSlugs       []string // the target account's docker apps (effective slug)
	ForeignDockerSlugs     []string // docker apps another account (or the server) owns here
	ServerLevelDockerSlugs []string // apps the backup's metadata marks server-level
	DockerMetadataMissing  bool     // the metadata is unreadable: which apps are server-level is unknown
	// Claims collects what the restore created or wrote for the account; nil
	// records nothing.
	Claims *restoreClaims
	// KeepExisting (GH #1993): add only what isn't on this server yet. Home
	// files already there are kept (rsync --ignore-existing, no --delete), and
	// a database that already has tables or a docker app whose data dir isn't
	// empty is left as it is and not claimed. False replaces: the account
	// becomes a mirror of the backup.
	KeepExisting bool
	// SkipPostgres (GH #1993): restore no PostgreSQL database. The panel sets
	// it when PostgreSQL is turned off on this server; each one is left out
	// with a warning.
	SkipPostgres bool
	// Report collects what the restore did for the caller's reply, in every
	// mode; nil records nothing.
	Report *restoreReport
}

// restoreReport is what a restore reports back whatever its mode.
type restoreReport struct {
	// PostgresDatabases are the PostgreSQL databases the restore loaded (GH
	// #1993). Each is a new database whose objects its holder role owns, and
	// the panel grants each database user it has on one again: the first
	// takes the objects over.
	PostgresDatabases []string
}

// notePostgresLoaded records that the restore loaded PostgreSQL database db.
func (e restoreEnforcement) notePostgresLoaded(db string) {
	if e.Report != nil {
		e.Report.PostgresDatabases = append(e.Report.PostgresDatabases, db)
	}
}

// restoreClaims names the databases and docker app data an upload-mode
// restore created or wrote for the account (GH #1993). The panel registers
// database and docker app rows from the file only for these, so a row never
// hands the account data the restore refused, such as a database that exists
// here without a panel row.
type restoreClaims struct {
	Databases   []string
	DockerSlugs []string
	// ArchiveMariaDBs are the MariaDB databases whose data, after the
	// restore, is all the archive's: new or holding nothing (no table, view,
	// routine or event) before it, and loaded without an error. A database
	// that already held something keeps it under the archive's, and one whose
	// load failed holds whatever got in, so the panel lets the archive grant
	// access only to these. Only the MariaDB load claims one: a name restored
	// as PostgreSQL says nothing about the MariaDB database of that name.
	ArchiveMariaDBs []string
	// ArchivePostgresDBs are the same for the PostgreSQL load: new or holding
	// nothing (no relation, routine or large object outside the system
	// schemas) before it, and restored without an error.
	ArchivePostgresDBs []string
}

// loadRestoredMariaDBDump loads a restored database's dump; tests swap it.
var loadRestoredMariaDBDump = loadMariaDBDumpScoped

// loadRestoredPostgresDump loads a restored PostgreSQL database's dump; tests
// swap it. The dump runs as a role with no server-wide rights, never as
// postgres, and what it creates is owned by the database's holder role until
// a database user granted on it takes it over (GH #1993). grantRoles get
// access to the restored database.
var loadRestoredPostgresDump = func(ctx context.Context, db string, dump *os.File, grantRoles []string) error {
	if aerr := pgLoadScoped(ctx, db, dump, "", grantRoles); aerr != nil {
		return aerr
	}
	return nil
}

func (e restoreEnforcement) claimDatabase(db string) {
	if e.Claims != nil {
		e.Claims.Databases = append(e.Claims.Databases, db)
	}
}

func (e restoreEnforcement) claimArchiveMariaDB(db string) {
	if e.Claims != nil {
		e.Claims.ArchiveMariaDBs = append(e.Claims.ArchiveMariaDBs, db)
	}
}

func (e restoreEnforcement) claimArchivePostgresDB(db string) {
	if e.Claims != nil {
		e.Claims.ArchivePostgresDBs = append(e.Claims.ArchivePostgresDBs, db)
	}
}

func (e restoreEnforcement) claimDockerSlug(slug string) {
	if e.Claims != nil {
		e.Claims.DockerSlugs = append(e.Claims.DockerSlugs, slug)
	}
}

// restoreDockerRoot is where a restored docker app's data lands (a var so
// tests can point it elsewhere).
var restoreDockerRoot = dockerAppDataRoot

// keptReason is the report line for something a keep-existing restore left
// as it is.
func keptReason(what string) string {
	return what + `; check "Overwrite existing items with the backup" to replace it`
}

// restoreModeUpload is an admin restore from an uploaded backup file, of one
// account or a whole-server container. Whoever made the file chose every name
// in it, so it may only write into the target account's own databases, mail
// domains (AllowedMailDomains) and docker apps, or create NEW ones in the
// account's own namespace; never this server's own databases or anything
// another account owns, and never anything that already exists here without
// belonging to the account (GH #1993).
const restoreModeUpload = "upload"

func (e restoreEnforcement) upload() bool { return e.Mode == restoreModeUpload }

// uploadDBRefusal says why an uploaded backup may not load db, or "" when it
// may: the target account owns it, or it is a new name in the account's own
// namespace.
func (e restoreEnforcement) uploadDBRefusal(db string) string {
	switch {
	case dbreserve.Database(db):
		return "it is one of this server's own databases"
	case e.dbAllowed(db):
		return ""
	case containsString(e.ForeignDBNames, db):
		return "it belongs to another account on this server"
	case e.DBPrefix == "" || len(db) <= len(e.DBPrefix) || !strings.HasPrefix(db, e.DBPrefix):
		return fmt.Sprintf("a database from an uploaded backup must be the account's own or named %s<name>", e.DBPrefix)
	}
	return ""
}

// uploadDockerRefusal says why an uploaded backup may not restore the data of
// docker app slug into dst, or "" when it may.
func (e restoreEnforcement) uploadDockerRefusal(slug, dst string) string {
	switch {
	case e.DockerMetadataMissing:
		return "the backup's metadata is unreadable, so it can't show the app is the account's own"
	case containsString(e.ServerLevelDockerSlugs, slug):
		return "a server-level app can't be restored from an uploaded backup"
	case containsString(e.ForeignDockerSlugs, slug):
		return "an app with this name belongs to another account on this server"
	case containsString(e.OwnedDockerSlugs, slug):
		return ""
	}
	if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
		return "an app with this name already exists on this server and isn't this account's"
	}
	return ""
}

func (e restoreEnforcement) enforceDB() bool   { return e.AllowedDBNames != nil }
func (e restoreEnforcement) enforceMail() bool { return e.AllowedMailDomains != nil }

func (e restoreEnforcement) dbAllowed(name string) bool {
	for _, d := range e.AllowedDBNames {
		if d == name { // exact — DB names are case-sensitive on Linux MariaDB/PG
			return true
		}
	}
	return false
}

func (e restoreEnforcement) mailDomainAllowed(domain string) bool {
	domain = strings.ToLower(domain)
	for _, d := range e.AllowedMailDomains {
		if strings.ToLower(d) == domain {
			return true
		}
	}
	return false
}

type backupRestoreParams struct {
	JobID              string `json:"job_id"`
	ManifestSnapshotID string `json:"manifest_snapshot_id"`
	TargetUserID       string `json:"target_user_id"`
	// TargetUsername is the system account name (matches /etc/passwd).
	// Required for the apply step to chown home + scope mariadb loads.
	// API resolves this from the panel users repo before dispatching.
	TargetUsername string            `json:"target_username,omitempty"`
	Overwrite      bool              `json:"overwrite"`
	RepoURL        string            `json:"repo_url,omitempty"`
	CredentialsRef string            `json:"credentials_ref,omitempty"`
	// PasswordFile lets the caller point restic at a repo password other
	// than the box-wide /etc/jabali-panel/restic-repo.password. GH #954
	// (Jabali→Jabali migration): the pulled source repo is encrypted with
	// the SOURCE box's restic password, so the import stages it and passes
	// that password's path here — reading the foreign repo without rekeying
	// it. Empty falls back to the box-wide file (every existing caller).
	// Exact parity with backup.account_list_manifests, which already threads
	// this field.
	PasswordFile string            `json:"password_file,omitempty"`
	SFTP         *backupSFTPInputs `json:"sftp,omitempty"`
	// ApplyStaged: when false the handler stops after materializing
	// stages into /var/lib/jabali-backups/restore-staging/<job_id>/
	// (recon mode). Default true → home+db are applied onto the live
	// system before the call returns.
	ApplyStaged *bool `json:"apply_staged,omitempty"`
}

type backupRestoreResult struct {
	JobID    string               `json:"job_id"`
	Stages   []backupRestoreStage `json:"stages"`
	Applied  []string             `json:"applied,omitempty"`
	Warnings []string             `json:"warnings,omitempty"`
	// User is the manifest's account block (id, username, email,
	// is_admin, uid_at_source). CLI needs it for disaster-recovery
	// mode where the panel row no longer exists and must be
	// reconstructed before reconcile picks the account back up.
	User backup.ManifestUser `json:"user"`
	// StagingCleanup reports whether the staging directory was
	// removed after a successful live apply ("removed", "kept",
	// "cleanup_failed:<err>"). Recon mode (apply=false) always
	// keeps the dir; the value is "kept" with detail.
	StagingCleanup string `json:"staging_cleanup,omitempty"`
	// Metadata is the raw metadata.json bytes pulled from the
	// stage=meta snapshot. CLI feeds it to backupmetadata.Apply to
	// rebuild domains/php_pools/databases/etc rows on disaster
	// recovery. Empty when the snapshot has no meta stage (older
	// snapshots, schema_version=1).
	Metadata json.RawMessage `json:"metadata,omitempty"`
	// RestoredPostgresDBs are the PostgreSQL databases the restore loaded
	// (GH #1993); the panel grants each database user it has on them again.
	RestoredPostgresDBs []string `json:"restored_postgres_databases"`
}

type backupRestoreStage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// ErrRestoreLocked is the typed error returned when another restore is
// already holding the global flock.
var ErrRestoreLocked = errors.New("another restore is in progress")

func backupRestoreHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req backupRestoreParams
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, bkInvalidArg("malformed JSON body")
	}
	if !ulidRE.MatchString(req.JobID) {
		return nil, bkInvalidArg("job_id must be a 26-char ULID")
	}
	if req.ManifestSnapshotID == "" {
		return nil, bkInvalidArg("manifest_snapshot_id required")
	}
	// target_username flows into useradd/loginctl argv and into the rsync
	// destination path join, neither of which is containment-checked
	// downstream. Validate the shape here, next to the ulid check, matching
	// what every other backup handler does.
	if req.TargetUsername != "" && !backupUsernameRE.MatchString(req.TargetUsername) {
		return nil, bkInvalidArg("target_username must match ^[a-z][a-z0-9_-]{0,31}$")
	}

	// Single global flock — held for the duration of the restore. Use
	// LOCK_NB so a busy host returns an error rather than blocking
	// the agent thread.
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

	cfg, cerr := bkResticConfigWithPassword(req.RepoURL, req.CredentialsRef, req.PasswordFile, req.SFTP)
	if cerr != nil {
		return nil, bkInternal("restic config", cerr)
	}
	c := backup.New(cfg)

	// Step 1 — pull the manifest, validate schema, walk stages.
	manifestBytes, err := c.Dump(ctx, req.ManifestSnapshotID, "manifest.json")
	if err != nil {
		return nil, bkInternal("read manifest", err)
	}
	manifest, err := backup.AccountManifestFromBytes(manifestBytes)
	if err != nil {
		return nil, bkInternal("parse manifest", err)
	}

	out := backupRestoreResult{JobID: req.JobID, User: manifest.User}

	// Pull the metadata.json bytes from the stage=meta snapshot up
	// front so the CLI can apply panel_state rows even when the live
	// apply path skips meta (meta is panel-DB rebuild, not data).
	for _, st := range manifest.Stages {
		if st.Name != backup.StageMeta || st.Status != backup.StageStatusOK || st.SnapshotID == "" {
			continue
		}
		metaBytes, derr := c.Dump(ctx, st.SnapshotID, "metadata.json")
		if derr == nil && len(metaBytes) > 0 {
			out.Metadata = metaBytes
		}
		break
	}

	// Stage walk. Each Stages[i] in the manifest carries the snapshot
	// id we restore. Restore order matters in v2 (db before mail
	// because mailboxes link to db rows); v1 honors manifest order
	// and lets the operator re-run targeted stages by hand if needed.
	for _, st := range manifest.Stages {
		if st.SnapshotID == "" || st.Status != backup.StageStatusOK {
			out.Stages = append(out.Stages, backupRestoreStage{
				Name: st.Name, Status: backup.StageStatusSkipped,
			})
			continue
		}
		target := filepath.Join("/var/lib/jabali-backups/restore-staging",
			req.JobID, st.Name)
		if err := os.MkdirAll(target, 0o750); err != nil {
			out.Stages = append(out.Stages, backupRestoreStage{
				Name: st.Name, Status: backup.StageStatusFailed,
				Error: fmt.Sprintf("mkdir staging: %v", err),
			})
			continue
		}
		err := c.Restore(ctx, backup.RestoreOpts{
			SnapshotID: st.SnapshotID,
			Target:     target,
		})
		stageOut := backupRestoreStage{Name: st.Name, Status: backup.StageStatusOK}
		if err != nil {
			stageOut.Status = backup.StageStatusFailed
			stageOut.Error = err.Error()
		}
		out.Stages = append(out.Stages, stageOut)
	}

	apply := true
	if req.ApplyStaged != nil {
		apply = *req.ApplyStaged
	}
	if apply {
		stagingRoot := filepath.Join("/var/lib/jabali-backups/restore-staging", req.JobID)
		applied, warnings, pgDBs := applyAccountRestoreReporting(ctx, stagingRoot, req.TargetUsername, manifest.User, manifest.Stages, out.Stages, restoreEnforcement{})
		out.RestoredPostgresDBs = pgDBs
		out.Applied = applied
		out.Warnings = warnings
		// GH #1361: stage each FTP subaccount's captured /etc/shadow hash for
		// the reconciler to apply when it recreates the account from the
		// restored row (panel-side applyRestoreMetadata rebuilds the row after
		// this call returns, so the files sit waiting — never a race). A bad
		// row is skipped with a warning; the account still restores, it just
		// needs a password reset.
		if len(out.Metadata) > 0 {
			var meta backup.AccountMetadata
			if jerr := json.Unmarshal(out.Metadata, &meta); jerr == nil {
				if staged, skipped := stageFtpRestoreCredentials(&meta, time.Now()); staged > 0 || len(skipped) > 0 {
					out.Applied = append(out.Applied, fmt.Sprintf("ftp: staged %d subaccount password(s) for restore", staged))
					for _, s := range skipped {
						out.Warnings = append(out.Warnings, "ftp credential staging skipped "+s)
					}
				}
			}
		}
		// Live apply succeeded for at least one stage — drop the
		// staging tree so /var/lib/jabali-backups/restore-staging/
		// doesn't accumulate per-job dirs. Recon mode (apply=false)
		// intentionally keeps them so the operator can inspect.
		// On any apply failure (no stages applied) keep the dir so
		// the operator can see what materialized.
		if len(applied) > 0 {
			if err := os.RemoveAll(stagingRoot); err != nil {
				out.StagingCleanup = "cleanup_failed: " + err.Error()
				out.Warnings = append(out.Warnings, "staging cleanup failed: "+err.Error())
			} else {
				out.StagingCleanup = "removed"
			}
		} else {
			out.StagingCleanup = "kept (no stages applied)"
		}
	} else {
		out.Warnings = append(out.Warnings,
			"apply_staged=false — files materialized to "+
				filepath.Join("/var/lib/jabali-backups/restore-staging", req.JobID)+
				"; nothing applied to live system")
		out.StagingCleanup = "kept (recon mode)"
	}
	return out, nil
}

// applyAccountRestore walks the materialized stage tree and applies
// home + db onto the live system. Order:
//
//  1. home → rsync staging/home/<username>/ → /home/<username>/
//     then chown -R <uid>:<gid> /home/<username>
//  2. db → for each stage with Items=[<dbname>]: mariadb <dbname>
//     < staging/db/<dbname>.sql (CREATE/DROP TABLE in dump rebuild
//     schema; existing GRANTs survive because the database row stays)
//
// mail is intentionally NOT auto-applied: stalwart-cli apply over a
// running spool corrupts RocksDB, plan.json has cross-host references
// (Domain/Tenant/Role) that need re-resolution, and bodies.tar untar
// risks lost messages. Operator gets a warning with the staging path
// and applies it manually.
//
// stageResults carries the per-stage materialization outcome from the
// caller; we only apply stages that materialized OK.
// materializedStages reports, per manifest-stage index, whether that stage's
// snapshot materialized OK and is therefore safe to apply. stageResults is
// built one-per-manifest-stage in manifest order by the stage walk, so it
// aligns by index with manifestStages. Indexing (rather than keying by
// stage Name) is deliberate: docker/db fan out multiple stages sharing one
// Name, and a name-keyed lookup would collapse them and drop good stages on a
// single same-named failure (GH #1360). A short/misaligned stageResults slice
// leaves the trailing stages false (not applied) — fail closed.
func materializedStages(manifestStages []backup.ManifestStage, stageResults []backupRestoreStage) []bool {
	ok := make([]bool, len(manifestStages))
	for i := range manifestStages {
		if i < len(stageResults) && stageResults[i].Status == backup.StageStatusOK {
			ok[i] = true
		}
	}
	return ok
}

// applyAccountRestoreReporting is applyAccountRestore that also returns the
// PostgreSQL databases it loaded, as a list (GH #1993): every restore reply
// carries them, and the panel grants their users again.
func applyAccountRestoreReporting(
	ctx context.Context,
	stagingRoot, username string,
	manifestUser backup.ManifestUser,
	manifestStages []backup.ManifestStage,
	stageResults []backupRestoreStage,
	enf restoreEnforcement,
) (applied, warnings, postgresDBs []string) {
	enf.Report = &restoreReport{}
	applied, warnings = applyAccountRestore(ctx, stagingRoot, username, manifestUser, manifestStages, stageResults, enf)
	return applied, warnings, append([]string{}, enf.Report.PostgresDatabases...)
}

func applyAccountRestore(
	ctx context.Context,
	stagingRoot, username string,
	manifestUser backup.ManifestUser,
	manifestStages []backup.ManifestStage,
	stageResults []backupRestoreStage,
	enf restoreEnforcement,
) ([]string, []string) {
	// Per-stage materialization gate, indexed — NOT keyed by stage Name.
	// Docker and DB fan out one ManifestStage per app / per database, and
	// EVERY one of them carries the same Name ("docker" / "db") — see
	// backup_create.go runDockerStage / the db stage. A name-keyed map
	// collapses those N statuses to whichever same-named stage happened to
	// land last, so a single later failure (or "source missing") would skip
	// EVERY docker app or database, including the ones that materialized
	// fine — the account comes back with its data silently dropped (GH
	// #1360). out.Stages is appended one-per-manifest-stage in manifest
	// order by the caller's stage-walk, so index i aligns exactly.
	applicable := materializedStages(manifestStages, stageResults)
	var applied, warnings []string
	if username == "" {
		warnings = append(warnings,
			"apply skipped: target_username missing from request — pass target_username from API")
		return applied, warnings
	}
	u, uerr := user.Lookup(username)
	if uerr != nil {
		// System user missing — disaster recovery onto a fresh host.
		// Recreate the passwd entry so the chown step at the end of
		// this function has a UID to target. UIDAtSource (when set)
		// keeps the source UID for traceability; on older snapshots
		// where UIDAtSource is 0 we let the system pick — the home
		// stage's chown -R rewrites every file regardless.
		homeDir := filepath.Join("/home", username)
		args := []string{
			"--user-group",
			"--home-dir", homeDir,
			"--groups", "www-data",
			"--shell", "/bin/bash",
			"--no-create-home", // home dir comes from the home stage rsync
		}
		if manifestUser.UIDAtSource != 0 && manifestUser.Username == username {
			args = append(args, "--uid", strconv.FormatUint(uint64(manifestUser.UIDAtSource), 10))
		}
		args = append(args, username)
		cmd := execCommandContext(ctx, "useradd", args...)
		if out, addErr := cmd.CombinedOutput(); addErr != nil {
			warnings = append(warnings,
				fmt.Sprintf("apply skipped: useradd %q failed: %v: %s", username, addErr, strings.TrimSpace(string(out))))
			return applied, warnings
		}
		uidLabel := "system-picked"
		if manifestUser.UIDAtSource != 0 {
			uidLabel = strconv.FormatUint(uint64(manifestUser.UIDAtSource), 10)
		}
		warnings = append(warnings,
			fmt.Sprintf("system user %q created (uid=%s, DR mode)", username, uidLabel))
		// Enable linger so systemctl --user (the cron-timer apply
		// path runs as the user) finds an active user manager. The
		// reconciler eventually calls user.slice.ensure which also
		// enables linger, but a fresh-DR tick can race: cron pass
		// runs before user-slice pass on the same tick, cron.apply
		// fails with "user does not have lingering enabled", and
		// the timer never lands. Doing it here makes the DR path
		// self-sufficient. Idempotent — loginctl returns success on
		// already-enabled users.
		lingerCmd := execCommandContext(ctx, "loginctl", "enable-linger", username)
		if lOut, lErr := lingerCmd.CombinedOutput(); lErr != nil {
			warnings = append(warnings,
				fmt.Sprintf("loginctl enable-linger %q failed: %v: %s — cron timer apply may fail until reconciler retries", username, lErr, strings.TrimSpace(string(lOut))))
		}
		u, uerr = user.Lookup(username)
		if uerr != nil {
			warnings = append(warnings,
				fmt.Sprintf("apply skipped: useradd succeeded but user.Lookup(%q) still fails: %v", username, uerr))
			return applied, warnings
		}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	// GH #1993: report each stage as it starts (a no-op unless the restore
	// is tracked, see backup_restore_progress.go).
	progress := restoreProgressFrom(ctx)
	toApply, nth := 0, 0
	for i := range manifestStages {
		if applicable[i] {
			toApply++
		}
	}

	for i, st := range manifestStages {
		if !applicable[i] {
			continue
		}
		nth++
		item := ""
		if len(st.Items) == 1 {
			item = st.Items[0]
		}
		progress.applying(st.Name, item, nth, toApply)
		switch st.Name {
		case backup.StageHome:
			// Restic preserves the absolute path; staged tree is at
			// stagingRoot/home/home/<username>/. Source needs a
			// trailing slash so rsync copies CONTENTS not the dir.
			src := filepath.Join(stagingRoot, "home", "home", username) + "/"
			dst := filepath.Join(restoreHomeRoot, username) + "/"
			if err := stagedEntry(stagingRoot, filepath.Clean(src), true); err != nil {
				warnings = append(warnings,
					fmt.Sprintf("home: source %s missing: %v", src, err))
				continue
			}
			homeOwner := saveHomeOwnership(dst)
			putHomeBack := func() {
				if err := homeOwner.put(dst, uid, gid, wwwDataGID()); err != nil {
					warnings = append(warnings, fmt.Sprintf("home: owner and mode of %s: %v", filepath.Clean(dst), err))
				}
			}
			// -aH (not -aHAX): do NOT restore ACLs/xattrs from the backup. The
			// repository is untrusted (a poisoned snapshot could carry
			// attacker-controlled ACL/xattr/capability metadata that root would
			// otherwise apply into a live tenant home); owner/mode are
			// re-normalized below regardless (Gitea #462).
			// Replace mirrors the backup (files added since are removed);
			// keep-existing only adds the files that aren't there yet.
			copied := true
			if enf.KeepExisting {
				if err := addMissingHomeFiles(ctx, src, dst, uid, gid); err != nil {
					// Files added before the error still get the owner and
					// docroot group fixes below.
					warnings = append(warnings, fmt.Sprintf("home: %v", err))
					copied = false
				}
			} else if err := execCommandContext(ctx, "rsync", "-aH", "--delete", src, dst).Run(); err != nil {
				warnings = append(warnings, fmt.Sprintf("home: rsync: %v", err))
				putHomeBack()
				continue
			}
			// The user's own copy needs no chown, and the files kept stay
			// as they are.
			if !enf.KeepExisting {
				err := chownTreeRecursive(dst, dst, uid, gid)
				putHomeBack()
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("home: chown: %v", err))
					continue
				}
			}
			// BUG A fix: the blanket uid:gid chown above clobbers the
			// provisioning convention that web docroots are group-owned
			// by www-data (so nginx workers, which run as www-data, can
			// read them). Without this re-flip a restored site is
			// <user>:<user> 0640 → www-data falls to "other" → no read
			// → branded 403 on /. Reconciler does not self-heal the
			// docroot group, so re-apply it here. Mirrors domain_create's
			// <user>:www-data chain.
			if err := restoreDocrootGroup(username); err != nil {
				warnings = append(warnings, fmt.Sprintf("home: docroot group www-data: %v", err))
			}
			// GH #621: strip any source-tenant JABALI_CACHE_* block from restored
			// WordPress sites so a CROSS-tenant restore never reads the source
			// tenant's Redis namespace (prefix/ACL bleed). Re-enabling cache
			// re-stamps the correct per-tenant constants.
			if n := stripRestoredCacheBlocks(username); n > 0 {
				applied = append(applied, fmt.Sprintf("stripped source cache constants from %d wp-config(s)", n))
			}
			if !copied {
				continue
			}
			if enf.KeepExisting {
				applied = append(applied, fmt.Sprintf("home → /home/%s (files already there kept)", username))
			} else {
				applied = append(applied, fmt.Sprintf("home → /home/%s", username))
			}

		case backup.StageDocker:
			// GH #1408: docker data lands in a GLOBAL docker-apps/<slug> dir keyed
			// by the (untrusted) manifest slug, which we can't cheaply scope to the
			// caller — so a tenant self-restore never applies docker (cut from
			// tenant v1). Admin restores are unaffected (Mode="").
			if enf.Mode == "tenant" {
				warnings = append(warnings, "docker: not restored from a self-service upload (restore Docker apps via an admin)")
				continue
			}
			if len(st.Items) == 0 {
				warnings = append(warnings, "docker: manifest stage missing items[0] app slug")
				continue
			}
			slug := st.Items[0]
			// The slug lands in a filesystem path and a compose invocation,
			// so re-validate it here: the manifest came out of the repo and
			// the repo is untrusted.
			if err := validateSlug(slug); err != nil {
				warnings = append(warnings, fmt.Sprintf("docker: refusing slug %q: %v", slug, err))
				continue
			}
			dst := filepath.Join(restoreDockerRoot, slug)
			// An uploaded backup names the slug, and the data dir it names is
			// global: never stop and overwrite another account's app.
			if enf.upload() {
				if why := enf.uploadDockerRefusal(slug, dst); why != "" {
					warnings = append(warnings, fmt.Sprintf("docker %s: not restored: %s", slug, why))
					continue
				}
			}
			if enf.KeepExisting && dirHasEntries(dst) {
				warnings = append(warnings, fmt.Sprintf("docker %s: kept: %s", slug, keptReason("its data is already on this server")))
				continue
			}
			// restic preserves absolute paths, so the staged tree is at
			// stagingRoot/docker/var/lib/jabali/docker-apps/<slug>/.
			src := filepath.Join(stagingRoot, backup.StageDocker, dockerAppDataRoot, slug) + "/"
			if err := stagedEntry(stagingRoot, filepath.Clean(src), true); err != nil {
				warnings = append(warnings, fmt.Sprintf("docker %s: source %s missing: %v", slug, src, err))
				continue
			}
			// Bring the stack down first — rsyncing over volumes a running
			// container is writing is how you get a half-restored database.
			// A down failure on an app that was never up here (the normal
			// case on a migration destination) is not fatal.
			if _, err := runDockerCompose(ctx, dst, "down"); err != nil {
				warnings = append(warnings, fmt.Sprintf("docker %s: compose down: %v (continuing)", slug, err))
			}
			if err := os.MkdirAll(dst, 0o750); err != nil {
				warnings = append(warnings, fmt.Sprintf("docker %s: mkdir %s: %v", slug, dst, err))
				continue
			}
			enf.claimDockerSlug(slug)
			// -aH, not -aHAX: same reasoning as the home stage — never apply
			// ACLs/xattrs/capabilities carried by an untrusted snapshot.
			if err := execCommandContext(ctx, "rsync", "-aH", "--delete", src, dst+"/").Run(); err != nil {
				warnings = append(warnings, fmt.Sprintf("docker %s: rsync: %v", slug, err))
				continue
			}
			// Deliberately NOT `compose up -d`. The restored tree carries a
			// compose.yml from the repo, and starting it here would run
			// untrusted compose as root without the tenant safety gate that
			// docker_app.restore applies (Gitea #509). The data is in place;
			// the app is started from the panel, which validates first.
			applied = append(applied, fmt.Sprintf("docker %s → %s (stopped; start it from the panel)", slug, dst))

		case backup.StageDB:
			if len(st.Items) == 0 {
				warnings = append(warnings, "db: manifest stage missing items[0] db name")
				continue
			}
			db := st.Items[0]
			if !restoreDBNameRe.MatchString(db) {
				warnings = append(warnings,
					fmt.Sprintf("db: manifest name %q rejected (not a valid identifier)", db))
				continue
			}
			// GH #1408: a tenant self-restore may only load databases the caller
			// owns — the untrusted manifest name is otherwise a global handle
			// (`otheruser_db`, or even `jabali_panel`). Skip anything not on the
			// panel-supplied owned list.
			if enf.upload() {
				if why := enf.uploadDBRefusal(db); why != "" {
					warnings = append(warnings, fmt.Sprintf("db %q: not restored: %s", db, why))
					continue
				}
			} else if enf.enforceDB() && !enf.dbAllowed(db) {
				warnings = append(warnings,
					fmt.Sprintf("db %q: not one of your databases — skipped (create it first, then restore)", db))
				continue
			}
			// PG dump first — backup_databases.go writes "<db>.pgdump"
			// for postgres engine. If present, it loads through the
			// scoped PostgreSQL loader.
			pgPath := filepath.Join(stagingRoot, "db", db+".pgdump")
			if stagedEntry(stagingRoot, pgPath, false) == nil {
				if enf.SkipPostgres {
					warnings = append(warnings, fmt.Sprintf("db %s (postgres): not restored: PostgreSQL is turned off on this server", db))
					continue
				}
				// Is there a database of this name here already?
				createSQL := fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s'", db)
				probeCmd := execCommandContext(ctx, "sudo", "-u", "postgres",
					"psql", "-XAtq", "-c", createSQL)
				probeOut, _ := probeCmd.Output()
				pgExists := strings.TrimSpace(string(probeOut)) != ""
				if pgExists && enf.upload() && !enf.dbAllowed(db) {
					warnings = append(warnings, fmt.Sprintf("db %q: not restored: a database with this name already exists on this server and isn't this account's", db))
					continue
				}
				// Keep-existing: the load replaces the whole database, so one
				// that holds anything at all (a function or a view counts) is
				// kept as it is.
				if pgExists && enf.KeepExisting {
					if has, hErr := pgHoldsObjects(ctx, db); hErr != nil {
						warnings = append(warnings, fmt.Sprintf("db %s (postgres): kept: couldn't check whether it already has data: %v", db, hErr))
						continue
					} else if has {
						warnings = append(warnings, fmt.Sprintf("db %s (postgres): kept: %s", db, keptReason("it already has data on this server")))
						continue
					}
				}
				// archive: as for MariaDB below, a database that is new or
				// holds nothing is all the archive's after a good load. A
				// database that can't be checked counts as one that holds
				// something.
				archive := false
				if enf.Claims != nil {
					if !pgExists {
						archive = true
					} else if has, hErr := pgHoldsObjects(ctx, db); hErr == nil && !has {
						archive = true
					}
				}
				// The dump loads into a new database that takes db's place
				// (pgLoadScoped), so the roles that can connect to db now get
				// the same access to it.
				var keepGrants []string
				if pgExists {
					roles, gErr := pgConnectGrantees(ctx, db)
					if gErr != nil {
						warnings = append(warnings, fmt.Sprintf("db %s (postgres): not restored: couldn't read which database users can connect to it: %v", db, gErr))
						continue
					}
					for _, r := range roles {
						if pgValidIdent(r) {
							keepGrants = append(keepGrants, r)
						} else {
							warnings = append(warnings, fmt.Sprintf("db %s (postgres): role %q can connect to it now and won't after the restore: its name isn't one the panel uses", db, r))
						}
					}
				}
				// The dump is opened by root and handed to the loader as an
				// open file: the staging tree is root-only 0750, and the
				// loader's processes never open its path.
				pgFile, oErr := openStagedFile(stagingRoot, pgPath)
				if oErr != nil {
					warnings = append(warnings,
						fmt.Sprintf("db %s (postgres): open dump: %v", db, oErr))
					continue
				}
				lErr := loadRestoredPostgresDump(ctx, db, pgFile, keepGrants)
				pgFile.Close()
				if lErr != nil {
					warnings = append(warnings,
						fmt.Sprintf("db %s (postgres): not restored: %v", db, lErr))
					continue
				}
				// A failed load leaves db as it was, so only now is it the
				// restore's.
				enf.claimDatabase(db)
				if archive {
					enf.claimArchivePostgresDB(db)
				}
				enf.notePostgresLoaded(db)
				applied = append(applied, fmt.Sprintf("db → %s (postgres)", db))
				continue
			}

			if enf.KeepExisting {
				if has, hErr := mariaDBHasTables(ctx, db); hErr != nil {
					warnings = append(warnings, fmt.Sprintf("db %s: kept: couldn't check whether it already has data: %v", db, hErr))
					continue
				} else if has {
					warnings = append(warnings, fmt.Sprintf("db %s: kept: %s", db, keptReason("it already has data on this server")))
					continue
				}
			}
			candidates := []string{
				filepath.Join(stagingRoot, "db", db+".sql"),
				filepath.Join(stagingRoot, "db", "stdin"),
			}
			var src string
			for _, p := range candidates {
				if stagedEntry(stagingRoot, p, false) == nil {
					src = p
					break
				}
			}
			if src == "" {
				warnings = append(warnings, fmt.Sprintf("db %s: dump file not found in staging", db))
				continue
			}
			f, err := openStagedFile(stagingRoot, src)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("db %s: open dump: %v", db, err))
				continue
			}
			// CREATE DATABASE IF NOT EXISTS so a freshly-deleted DB
			// (panel user.delete cascade dropped it) can take the
			// dump load. Charset/collation match what panel-api's
			// db.create defaults to. Idempotent — present DBs are
			// untouched. Backticks around name guard against names
			// with reserved-word collisions (M24 'dual' incident).
			//
			// GH #1993: a database an uploaded backup names that the account
			// doesn't own must be NEW. A plain CREATE fails when it exists, so
			// the file can't load into a database nobody told us about (an
			// orphan, or one outside the panel's rows). db matched
			// restoreDBNameRe, so it can't break out of the backticks.
			// archive: the database holds nothing yet, so after a good load
			// all of it is the archive's. Checked in both modes: keep-existing
			// skipped one with tables above, but a routine or an event
			// outlives the load. A database that can't be checked counts as
			// one that holds something. Between this check and the load only
			// the account itself can write to the database, not the archive.
			archive := false
			if enf.Claims != nil {
				if has, hErr := mariaDBHoldsObjects(ctx, db); hErr == nil && !has {
					archive = true
				}
			}
			createSQL := "CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
			newOnly := enf.upload() && !enf.dbAllowed(db)
			if newOnly {
				createSQL = "CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"
			}
			createCmd := execCommandContext(ctx, "mariadb", "-e", fmt.Sprintf(createSQL, db))
			if cOut, cErr := createCmd.CombinedOutput(); cErr != nil {
				_ = f.Close()
				if newOnly && strings.Contains(string(cOut), "1007") {
					warnings = append(warnings, fmt.Sprintf("db %q: not restored: a database with this name already exists on this server and isn't this account's", db))
					continue
				}
				warnings = append(warnings,
					fmt.Sprintf("db %s: create database: %v: %s", db, cErr, strings.TrimSpace(string(cOut))))
				continue
			}
			enf.claimDatabase(db)
			// JAB-239: the dump is tenant-controlled content (it came
			// from the tenant's own snapshot), so it loads through the
			// db-scoped shadow account as an unprivileged OS user —
			// never as root. See db_load_scoped.go.
			lerr := loadRestoredMariaDBDump(ctx, db, f)
			_ = f.Close()
			if lerr != nil {
				warnings = append(warnings,
					fmt.Sprintf("db %s: mariadb load: %v", db, lerr))
				continue
			}
			if archive {
				enf.claimArchiveMariaDB(db)
			}
			applied = append(applied, fmt.Sprintf("db → %s", db))

		case backup.StageMail:
			// ADR-0123: the mail stage carries a per-user Maildir tree
			// (exported via JMAP at backup). restic preserves the backup-time
			// absolute path, so it materializes at
			//   stagingRoot/mail/run/jabali-backup/<backupJobID>/mail/<domain>/<local>/...
			// Resolve the dir holding the <domain> subdirs.
			mailTree := filepath.Join(stagingRoot, "mail") // flat-layout fallback
			if matches, _ := filepath.Glob(filepath.Join(stagingRoot, "mail", "run", "jabali-backup", "*", "mail")); len(matches) > 0 {
				mailTree = matches[len(matches)-1]
			}
			// The mail stage reads, prunes and imports this tree as root: reach
			// it without following a symlink.
			if err := stagedEntry(stagingRoot, mailTree, true); err != nil {
				warnings = append(warnings, "mail: no message tree in snapshot — skip")
				continue
			}

			// Legacy (pre-ADR-0123) snapshots carried a whole-store
			// bodies.tar instead of a Maildir tree — not replayable per-user.
			// Surface the documented manual path instead of skipping silently.
			if _, err := os.Stat(filepath.Join(mailTree, "bodies.tar")); err == nil {
				warnings = append(warnings,
					"mail: legacy bodies.tar snapshot (pre-ADR-0123) — historical messages need manual restore "+
						"(stop stalwart-mail; tar -xf bodies.tar -C /; start stalwart-mail)")
				continue
			}

			// New snapshot: import the Maildir tree via JMAP Email/import
			// (Stalwart Message-ID dedup → idempotent + multi-tenant-safe).
			if entries, err := os.ReadDir(mailTree); err != nil || len(entries) == 0 {
				warnings = append(warnings, "mail: no message tree in snapshot — skip")
				continue
			}
			// GH #1408: a tenant self-restore may only replay mailboxes for
			// domains the caller owns. The tree is keyed <domain>/<local>, so drop
			// every non-owned <domain> subdir from the (agent-owned) staging before
			// import — a crafted tar otherwise injects messages into another
			// tenant's mailbox via JMAP Email/import.
			// GH #1993: an upload restore uses the same allowlist, the target
			// account's own domains.
			if enf.enforceMail() {
				notOwned, noneLeft := "mail: domain %q is not yours — skipped", "mail: no mailboxes for your domains in this archive — skip"
				if enf.upload() {
					notOwned, noneLeft = "mail: domain %q is not one of this account's domains — skipped", "mail: no mailboxes for this account's domains in this archive — skip"
				}
				dirs, _ := os.ReadDir(mailTree)
				for _, e := range dirs {
					if e.IsDir() && !enf.mailDomainAllowed(e.Name()) {
						_ = os.RemoveAll(filepath.Join(mailTree, e.Name()))
						warnings = append(warnings, fmt.Sprintf(notOwned, e.Name()))
					}
				}
				if left, _ := os.ReadDir(mailTree); len(left) == 0 {
					warnings = append(warnings, noneLeft)
					continue
				}
			}
			if !stalwartActive(ctx) {
				warnings = append(warnings, "mail: Stalwart inactive — message import skipped")
				continue
			}
			// GH #954: scope filesafe to THIS restore's staging dir, not
			// migrationStagingRoots. The mail tree materializes under
			// /var/lib/jabali-backups/restore-staging/<job>/mail, which is NOT a
			// migration root — so openMaildirFileInStaging refused every message
			// file's open, and every account-restore silently imported 0 bodies
			// (the "messages not replayed" symptom). stagingRoot is agent-owned,
			// not tenant-writable; RESOLVE_BENEATH under it still blocks symlink
			// escape inside the restored tree.
			impRes, impErr := importMaildirTree(ctx, mailTree, "", []string{stagingRoot})
			if impErr != nil {
				warnings = append(warnings, fmt.Sprintf("mail: import: %v", impErr))
				continue
			}
			applied = append(applied,
				fmt.Sprintf("mail → %s (%d messages in %d mailboxes)", username, impRes.MessagesImported, impRes.MailboxesProcessed))
			for _, sk := range impRes.Skipped {
				warnings = append(warnings, "mail: "+sk)
			}
		case backup.StageMeta, backup.StageDNS, backup.StageCron, backup.StageSSH,
			backup.StageApps, backup.StagePHP:
			// Metadata-only stages — informational, no apply.
		}
	}
	return applied, warnings
}

// restoreDocrootGroup re-applies the provisioning convention that the
// blanket uid:gid home chown clobbers: web docroots are group-owned by
// www-data so nginx workers (www-data) can read them. Mirrors the
// <user>:www-data chain domain_create lays down — group flipped to
// www-data (owner preserved via Lchown uid -1) and the setgid bit set
// on directories so app-created files keep inheriting the www-data
// group after the restore (matches domain_create's 2750).
//
// SECURITY: this runs as root over a tenant-writable tree
// (/home/<user>/domains is owned by the tenant). It must NEVER follow a
// symlink, or a tenant who swaps public_html (or a domain dir) for a
// symlink to /etc, another user's home, etc. could redirect the group
// flip outside the docroot. So: os.Lstat (not Stat) at every decision
// point, refuse symlinked/non-dir paths, and chgrp the tree with an
// Lchown-based walk (filepath.Walk uses Lstat and does not descend into
// symlinks; Lchown changes the link's own group, never its target) —
// the same symlink-safe pattern chownTreeRecursive uses below.
// stripRestoredCacheBlocks removes the panel-managed JABALI_CACHE_* block from
// every restored wp-config.php under the user's docroots (GH #621), preserving
// ownership + mode. Best-effort + symlink-safe; a cross-tenant restore would
// otherwise leave the source tenant's Redis prefix/ACL active.
func stripRestoredCacheBlocks(username string) int {
	patterns := []string{
		"/home/" + username + "/domains/*/public_html",
		"/home/" + username + "/domains/*/public_html/*",
	}
	n := 0
	for _, pat := range patterns {
		dirs, _ := filepath.Glob(pat)
		for _, dir := range dirs {
			// GH #621 + security review: the ONLY op on the tenant path is
			// setWPConfigCacheConstants, which opens wp-config with openat2
			// RESOLVE_NO_SYMLINKS (kernel refuses if the file OR any path component
			// is a symlink — race-free, no TOCTOU) and does read/truncate/write/
			// fchown on the fd. It errors + skips for a non-WP dir (no wp-config),
			// so no pre-stat of the tenant path is needed. enable=false strips the
			// JABALI_CACHE_* block.
			if err := setWPConfigCacheConstants(dir, "", 0, "", "", "", false, 0, false, 0, 0); err == nil {
				n++
			}
		}
	}
	return n
}

func restoreDocrootGroup(username string) error {
	grp, err := user.LookupGroup("www-data")
	if err != nil {
		return fmt.Errorf("lookup group www-data: %w", err)
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil {
		return fmt.Errorf("www-data gid %q: %w", grp.Gid, err)
	}

	domainsDir := "/home/" + username + "/domains"
	fi, err := os.Lstat(domainsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // user has no domains → nothing to re-group
		}
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return nil // refuse a symlinked / non-dir domains path
	}
	// domains/ itself must be group www-data (g+x for traversal).
	if err := os.Lchown(domainsDir, -1, gid); err != nil {
		return fmt.Errorf("lchown %s: %w", domainsDir, err)
	}

	entries, err := os.ReadDir(domainsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 || !e.IsDir() {
			continue // skip symlinks + non-dir entries
		}
		domDir := filepath.Join(domainsDir, e.Name())
		pub := filepath.Join(domDir, "public_html")
		pfi, lerr := os.Lstat(pub)
		if lerr != nil || pfi.Mode()&os.ModeSymlink != 0 || !pfi.IsDir() {
			continue // not a real docroot dir (missing or symlinked)
		}
		if err := os.Lchown(domDir, -1, gid); err != nil {
			return fmt.Errorf("lchown %s: %w", domDir, err)
		}
		if err := fsperm.GroupSetgidTree(pub, gid); err != nil {
			return fmt.Errorf("chgrp tree %s: %w", pub, err)
		}
	}
	return nil
}

// homeOwnership is a home directory's own owner and mode, saved before a
// restore copies over it.
//
// GH #1993: the home's owner and mode come from the account's SSH setting
// (root's 0751 for SFTP, the user's 0750 group www-data for SSH; see
// ssh.user.home_chown), and nginx (www-data) reaches the account's sites
// through it. rsync -a copies the backup's onto it, and the chown pass after
// makes it the user's own 0750: every site of the account then answers 404.
// The reconciler sets it only when the SSH setting changes, so the restore
// puts back what the home had.
type homeOwnership struct {
	uid, gid int
	mode     uint32
	saved    bool
}

// saveHomeOwnership records the home directory's owner and mode; a home that
// doesn't exist yet records nothing (a new home is the reconciler's to set).
func saveHomeOwnership(home string) homeOwnership {
	fi, err := os.Lstat(filepath.Clean(home))
	if err != nil || !fi.IsDir() {
		return homeOwnership{}
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return homeOwnership{}
	}
	return homeOwnership{uid: int(st.Uid), gid: int(st.Gid), mode: st.Mode & 0o7777, saved: true}
}

// homeModeMask keeps a home's mode to what the SSH settings use (0751,
// 0750, setgid): never writable by group or others.
const homeModeMask = 0o2755

// put gives the home of the account uid:gid back the saved owner and mode,
// when they are one of the account's own layouts: owned by root or the
// account, group the account's or wwwGID. Anything else (a home left behind
// by another account, whose uid may since belong to someone else) stays as
// the chown pass made it, the account's. The home's parent is root's, and the
// home is opened without following a link.
func (h homeOwnership) put(home string, uid, gid, wwwGID int) error {
	if !h.saved || (h.uid != 0 && h.uid != uid) || (h.gid != gid && h.gid != wwwGID) {
		return nil
	}
	fd, err := unix.Open(filepath.Clean(home), unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.Fchown(fd, h.uid, h.gid); err != nil {
		return err
	}
	return unix.Fchmod(fd, h.mode&homeModeMask)
}

// wwwDataGID is the www-data group's id, or -1 when there is none. A var so
// tests can stand another group in.
var wwwDataGID = func() int {
	g, err := user.LookupGroup("www-data")
	if err != nil {
		return -1
	}
	id, err := strconv.Atoi(g.Gid)
	if err != nil {
		return -1
	}
	return id
}

// chownTreeRecursive chowns root and every entry under it to uid:gid, root
// reached from anchor (the account's home). Never `chown -R`, which follows
// symlinks, and never a walk by path: the tenant can swap a directory for a
// symlink mid-walk and send root's chown out of the tree. fsperm.ChownTree
// walks by file descriptor.
func chownTreeRecursive(anchor, root string, uid, gid int) error {
	return fsperm.ChownTree(anchor, root, uid, gid)
}

// backupAccountListManifestsHandler enumerates kind=account_backup
// stage=manifest snapshots in a repo, used by the interactive
// `jabali backup account-restore` prompt so the operator picks a
// snapshot from a real list instead of typing a ULID. Mirrors
// systemRestoreListManifestsHandler; difference is the kind tag and
// the per-snapshot tag set carries user-id + job-id which the CLI
// renders as a friendly grouping.
func backupAccountListManifestsHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req struct {
		RepoURL        string            `json:"repo_url"`
		CredentialsRef string            `json:"credentials_ref,omitempty"`
		PasswordFile   string            `json:"password_file,omitempty"`
		SFTP           *backupSFTPInputs `json:"sftp,omitempty"`
		// UserID optional; when set returns only that account's
		// manifests. Useful to narrow when the repo carries many users.
		UserID string `json:"user_id,omitempty"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, bkInvalidArg("malformed JSON body")
	}
	if req.RepoURL == "" {
		return nil, bkInvalidArg("repo_url required")
	}
	cfg, cerr := bkResticConfigWithPassword(req.RepoURL, req.CredentialsRef, req.PasswordFile, req.SFTP)
	if cerr != nil {
		return nil, bkInternal("restic config", cerr)
	}
	c := backup.New(cfg)
	tagFilter := []backup.Tag{
		backup.MakeTag(backup.TagKeyKind, backup.KindAccountBackup),
		backup.MakeTag(backup.TagKeyStage, backup.StageManifest),
	}
	if req.UserID != "" {
		tagFilter = append(tagFilter, backup.MakeTag(backup.TagKeyUserID, req.UserID))
	}
	snaps, err := c.Snapshots(ctx, tagFilter)
	if err != nil {
		return nil, bkInternal("restic snapshots", err)
	}
	type manifestRow struct {
		SnapshotID string    `json:"snapshot_id"`
		Time       time.Time `json:"time"`
		Hostname   string    `json:"hostname,omitempty"`
		UserID     string    `json:"user_id,omitempty"`
		JobID      string    `json:"job_id,omitempty"`
		Tags       []string  `json:"tags,omitempty"`
	}
	out := make([]manifestRow, 0, len(snaps))
	for _, s := range snaps {
		row := manifestRow{
			SnapshotID: s.ID,
			Time:       s.Time,
			Hostname:   s.Hostname,
			Tags:       s.Tags,
		}
		// Tags arrive as "user-id=<ulid>", "job-id=<ulid>", … —
		// extract the two we need without forcing the CLI to parse.
		for _, t := range s.Tags {
			if strings.HasPrefix(t, "user-id=") {
				row.UserID = strings.TrimPrefix(t, "user-id=")
			} else if strings.HasPrefix(t, "job-id=") {
				row.JobID = strings.TrimPrefix(t, "job-id=")
			}
		}
		out = append(out, row)
	}
	// Newest first.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Time.After(out[i].Time) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return map[string]any{"manifests": out, "total": len(out)}, nil
}

// backupRestoreStatusHandler reports the materialized staging area for
// a restore job. v1 surface; v2 wires unit-status + import-progress.
func backupRestoreStatusHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, bkInvalidArg("malformed JSON body")
	}
	if !ulidRE.MatchString(req.JobID) {
		return nil, bkInvalidArg("job_id must be a 26-char ULID")
	}
	staging := filepath.Join("/var/lib/jabali-backups/restore-staging", req.JobID)
	entries, err := os.ReadDir(staging)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{"job_id": req.JobID, "stages_present": []string{}}, nil
		}
		return nil, bkInternal("read staging dir", err)
	}
	var stages []string
	for _, e := range entries {
		if e.IsDir() {
			stages = append(stages, e.Name())
		}
	}
	return map[string]any{
		"job_id":         req.JobID,
		"stages_present": stages,
		"updated_at":     time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// trim keeps log noise down on errors. (Kept here to avoid a circular
// import; matches the existing pattern in security_malware.go.)
func _trimRestore(s string) string {
	return strings.TrimSpace(s)
}

func init() {
	Default.Register("backup.restore", backupRestoreHandler)
	Default.Register("backup.restore_status", backupRestoreStatusHandler)
	Default.Register("backup.account_list_manifests", backupAccountListManifestsHandler)
}

// mariaDBHasTables reports whether MariaDB database db exists and has a
// table. db matched restoreDBNameRe (it starts with a letter) and goes in as
// the database argument: no SQL is built from it.
func mariaDBHasTables(ctx context.Context, db string) (bool, error) {
	out, err := execCommandContext(ctx, "mariadb", "-N", "-B", "-e", "SHOW TABLES", db).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "ERROR 1049") { // unknown database
			return false, nil
		}
		return false, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// mariaDBHoldsObjects reports whether MariaDB database db holds anything: a
// table, a view, a stored routine or an event. A routine or an event outlives
// a dump loaded over it and can run with its definer's rights, so a database
// with only those isn't empty. A database that doesn't exist holds nothing.
// db goes in as the default database: no SQL is built from it.
func mariaDBHoldsObjects(ctx context.Context, db string) (bool, error) {
	out, err := execCommandContext(ctx, "mariadb", "-N", "-B", "-e",
		"SELECT (SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE())"+
			" + (SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE())"+
			" + (SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA = DATABASE())", db).CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "ERROR 1049") { // unknown database
			return false, nil
		}
		return false, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return objectCount(out)
}

func objectCount(out []byte) (bool, error) {
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return false, fmt.Errorf("unexpected object count %q", strings.TrimSpace(string(out)))
	}
	return n > 0, nil
}

// pgHoldsObjects reports whether PostgreSQL database db holds anything
// outside the system schemas: a relation of any kind (table, view, sequence,
// index, …), a routine, or a large object. A routine outlives a dump restored
// over it and can run with its owner's rights, and a large object is data
// outside any table, so a database with only those isn't empty. db goes in as
// the -d argument: no SQL is built from it.
func pgHoldsObjects(ctx context.Context, db string) (bool, error) {
	out, err := execCommandContext(ctx, "sudo", "-u", "postgres", "psql", "-XAtq", "-d", db, "-c",
		"SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace"+
			" WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\\_toast%' AND n.nspname NOT LIKE 'pg\\_temp%')"+
			" + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace"+
			" WHERE n.nspname NOT IN ('pg_catalog', 'information_schema'))"+
			" + (SELECT count(*) FROM pg_largeobject_metadata)").Output()
	if err != nil {
		return false, err
	}
	return objectCount(out)
}

// dirHasEntries reports whether path is a directory with anything in it.
func dirHasEntries(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}
