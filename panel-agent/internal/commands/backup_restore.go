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
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/fsperm"
)

const restoreLockPath = "/var/lib/jabali-backups/.restore.lock"

// restoreDBNameRe is the canonical database-identifier policy shared with
// db.create / db.drop (^[a-zA-Z][a-zA-Z0-9_-]{0,63}$). Restore reads the
// database name from the backup manifest — repository-controlled data — and
// interpolates it into privileged psql/createdb/mariadb commands. A poisoned
// manifest must not be able to inject SQL or shell-meaningful identifiers, so
// every manifest db name is validated against this whitelist before use
// (Gitea #463).
var restoreDBNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

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
		applied, warnings := applyAccountRestore(ctx, stagingRoot, req.TargetUsername, manifest.User, manifest.Stages, out.Stages)
		out.Applied = applied
		out.Warnings = warnings
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
func applyAccountRestore(
	ctx context.Context,
	stagingRoot, username string,
	manifestUser backup.ManifestUser,
	manifestStages []backup.ManifestStage,
	stageResults []backupRestoreStage,
) ([]string, []string) {
	statusOf := map[string]string{}
	for _, sr := range stageResults {
		statusOf[sr.Name] = sr.Status
	}
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

	for _, st := range manifestStages {
		if statusOf[st.Name] != backup.StageStatusOK {
			continue
		}
		switch st.Name {
		case backup.StageHome:
			// Restic preserves the absolute path; staged tree is at
			// stagingRoot/home/home/<username>/. Source needs a
			// trailing slash so rsync copies CONTENTS not the dir.
			src := filepath.Join(stagingRoot, "home", "home", username) + "/"
			dst := "/home/" + username + "/"
			if _, err := os.Stat(filepath.Clean(src)); err != nil {
				warnings = append(warnings,
					fmt.Sprintf("home: source %s missing: %v", src, err))
				continue
			}
			// -aH (not -aHAX): do NOT restore ACLs/xattrs from the backup. The
			// repository is untrusted (a poisoned snapshot could carry
			// attacker-controlled ACL/xattr/capability metadata that root would
			// otherwise apply into a live tenant home); owner/mode are
			// re-normalized below regardless (Gitea #462).
			if err := execCommandContext(ctx, "rsync", "-aH", "--delete", src, dst).Run(); err != nil {
				warnings = append(warnings, fmt.Sprintf("home: rsync: %v", err))
				continue
			}
			if err := chownTreeRecursive(dst, uid, gid); err != nil {
				warnings = append(warnings, fmt.Sprintf("home: chown: %v", err))
				continue
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
			applied = append(applied, fmt.Sprintf("home → /home/%s", username))

		case backup.StageDocker:
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
			// restic preserves absolute paths, so the staged tree is at
			// stagingRoot/docker/var/lib/jabali/docker-apps/<slug>/.
			src := filepath.Join(stagingRoot, backup.StageDocker, dockerAppDataRoot, slug) + "/"
			if _, err := os.Stat(filepath.Clean(src)); err != nil {
				warnings = append(warnings, fmt.Sprintf("docker %s: source %s missing: %v", slug, src, err))
				continue
			}
			dst := filepath.Join(dockerAppDataRoot, slug)
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
			// PG dump first — backup_databases.go writes "<db>.pgdump"
			// for postgres engine. If present, route to pg_restore.
			pgPath := filepath.Join(stagingRoot, "db", db+".pgdump")
			if _, perr := os.Stat(pgPath); perr == nil {
				// CREATE DATABASE if missing. PG has no
				// IF NOT EXISTS for CREATE DATABASE pre-9.x but
				// we accept an "already exists" error as success.
				createSQL := fmt.Sprintf("SELECT 1 FROM pg_database WHERE datname = '%s'", db)
				probeCmd := execCommandContext(ctx, "sudo", "-u", "postgres",
					"psql", "-XAtq", "-c", createSQL)
				probeOut, _ := probeCmd.Output()
				if strings.TrimSpace(string(probeOut)) == "" {
					mkCmd := execCommandContext(ctx, "sudo", "-u", "postgres",
						"createdb", "--encoding=UTF8", db)
					if cOut, cErr := mkCmd.CombinedOutput(); cErr != nil {
						warnings = append(warnings,
							fmt.Sprintf("db %s (postgres): createdb: %v: %s",
								db, cErr, strings.TrimSpace(string(cOut))))
						continue
					}
				}
				// pg_restore --clean --if-exists drops then re-creates
				// every object in the dump. Idempotent on re-runs.
				//
				// The dump is fed via STDIN, not as a filename: pg_restore
				// runs as the postgres user, and the staging tree is
				// root-owned 0750, so a path argument dies with EACCES
				// every time (found live on GH #1015's round-trip — this
				// branch had never actually restored anything). Root opens
				// the file; the child just inherits the fd, and the staging
				// tree stays root-only. Same trust shape as the MariaDB
				// branch below, which pipes for the same reason.
				pgFile, oErr := os.Open(pgPath)
				if oErr != nil {
					warnings = append(warnings,
						fmt.Sprintf("db %s (postgres): open dump: %v", db, oErr))
					continue
				}
				restoreCmd := execCommandContext(ctx, "sudo", "-u", "postgres",
					"pg_restore", "--clean", "--if-exists", "--no-owner",
					"--no-privileges", "-d", db)
				restoreCmd.Stdin = pgFile
				rOut, rErr := restoreCmd.CombinedOutput()
				pgFile.Close()
				if rErr != nil {
					warnings = append(warnings,
						fmt.Sprintf("db %s (postgres): pg_restore: %v: %s",
							db, rErr, strings.TrimSpace(string(rOut))))
					continue
				}
				applied = append(applied, fmt.Sprintf("db → %s (postgres)", db))
				continue
			}

			candidates := []string{
				filepath.Join(stagingRoot, "db", db+".sql"),
				filepath.Join(stagingRoot, "db", "stdin"),
			}
			var src string
			for _, p := range candidates {
				if _, err := os.Stat(p); err == nil {
					src = p
					break
				}
			}
			if src == "" {
				warnings = append(warnings, fmt.Sprintf("db %s: dump file not found in staging", db))
				continue
			}
			f, err := os.Open(src)
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
			createCmd := execCommandContext(ctx, "mariadb", "-e",
				fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;", db))
			if cOut, cErr := createCmd.CombinedOutput(); cErr != nil {
				_ = f.Close()
				warnings = append(warnings,
					fmt.Sprintf("db %s: create database: %v: %s", db, cErr, strings.TrimSpace(string(cOut))))
				continue
			}
			// JAB-239: the dump is tenant-controlled content (it came
			// from the tenant's own snapshot), so it loads through the
			// db-scoped shadow account as an unprivileged OS user —
			// never as root. See db_load_scoped.go.
			lerr := loadMariaDBDumpScoped(ctx, db, f)
			_ = f.Close()
			if lerr != nil {
				warnings = append(warnings,
					fmt.Sprintf("db %s: mariadb load: %v", db, lerr))
				continue
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

// chownTreeRecursive walks `root` and chowns every entry to uid:gid.
// We avoid `chown -R` because it forks for symlinks and tries to
// follow them; filepath.Walk + Lchown stays inside the tree.
func chownTreeRecursive(root string, uid, gid int) error {
	return filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
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
