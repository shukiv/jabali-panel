// `jabali migrate pull-source` cobra subcommand. Reads the per-job
// secrets file at /etc/jabali-panel/migration-secrets/<job-id>.env,
// connects to the source via SSH, runs the source-kind appropriate
// backup command (pkgacct / system_backup_user / v-backup-user),
// pulls the produced tarball back to /var/lib/jabali-migrations/
// <job-id>/, and extracts it under .../extracted/.
//
// Closes the operator workflow gap: previously the operator had to
// hand-run pkgacct + scp + tar -xzf before `jabali migrate import`
// could find an extracted tree. Now one cobra invocation handles
// all three.
//
// Operator workflow:
//  1. INSERT migration_jobs row (or via admin SPA drawer)
//  2. echo SSH_PASSWORD=… > /etc/jabali-panel/migration-secrets/<id>.env
//     (or SSH_PRIVATE_KEY=…)
//  3. jabali migrate pull-source --job-id <id>
//  4. jabali migrate import --job-id <id> --target-user … …
//
// WHM-pkgacct skipped — that source-kind is offline by design
// (operator-uploaded tarball, no live source). Returns an error
// directing the operator at scp.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cloudpanel"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cyberpanel"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/directadmin"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/hestiacp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/plesk"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/wordpressplugin"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/wordpressssh"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newMigratePullSourceCmd() *cobra.Command {
	var jobID string
	var sshUser string
	cmd := &cobra.Command{
		Use:   "pull-source",
		Short: "Connect to source via SSH, run kind-appropriate backup, pull + extract tarball",
		Long: `Reads the per-job secrets at
/etc/jabali-panel/migration-secrets/<job-id>.env then connects to
the source host (job.source_host) and runs the source-kind backup
command (pkgacct / system_backup_user / v-backup-user). Pulls the
produced tarball into /var/lib/jabali-migrations/<job-id>/ and
extracts under .../extracted/.

WHM-pkgacct is offline by design — operator-uploaded tarball, no
live source SSH. Use scp directly for that kind.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobID == "" {
				return errors.New("--job-id is required")
			}
			if sshUser == "" {
				sshUser = "root"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Minute)
			defer cancel()

			repo := repository.NewMigrationJobRepository(sharedDB)
			settingsRepo := repository.NewServerSettingsRepository(sharedDB)
			// Best-effort settings lookup. If the row is missing or
			// errors, fall back to the conservative default (private-
			// host SSRF blocked) — matches the pre-toggle behavior.
			allowPrivate := false
			if s, sErr := settingsRepo.Get(ctx); sErr == nil && s != nil {
				allowPrivate = s.MigrationAllowPrivateHosts
			}

			// markPullFailed persists state=failed + last_error so the
			// job row reflects the failure instead of sitting in pending
			// forever. Called from every pre-stage error path below
			// (Connect, secret-load, mkdir, tarball pull, extract).
			// Mirrors the failJob() helper the import command uses for
			// stage-machine failures.
			markPullFailed := func(reason error) error {
				msg := "pull-source: " + reason.Error()
				if uErr := repo.UpdateState(ctx, jobID, models.MigrationStateFailed, &msg); uErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"(warning: could not persist failure to migration_jobs: %v)\n", uErr)
				}
				return reason
			}

			job, err := repo.FindByID(ctx, jobID)
			if err != nil {
				return fmt.Errorf("load job: %w", err)
			}
			if job.SourceHost == "" {
				return markPullFailed(errors.New("job.source_host is empty — pull-source needs a live SSH target"))
			}

			secretPath := fmt.Sprintf("%s/%s.env", migrate.SecretsDir, jobID)
			if _, err := os.Stat(secretPath); err != nil {
				return markPullFailed(fmt.Errorf("secrets file %s missing: %w (drop SSH_PASSWORD or SSH_PRIVATE_KEY there first)", secretPath, err))
			}
			secret := migrate.SecretRef{Path: secretPath}

			// Local destination paths
			localDir := filepath.Join("/var/lib/jabali-migrations", jobID)
			if err := os.MkdirAll(localDir, 0o750); err != nil {
				return markPullFailed(fmt.Errorf("mkdir %s: %w", localDir, err))
			}
			// Re-pull: wipe prior extract tree + any prior-source-user
			// tarballs. The DA preflight pivot can change job.SourceUser
			// between attempts, leaving user.<old>.tar.gz on disk that
			// the restore stage's cpanel.ParseTarball might consume
			// instead of the new one. Cheap: directory is per-job-id
			// + only this run is writing it.
			extractCleanup := filepath.Join(localDir, "extracted")
			if err := os.RemoveAll(extractCleanup); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: clear stale extract dir %s: %v\n", extractCleanup, err)
			}
			matches, _ := filepath.Glob(filepath.Join(localDir, "user.*.tar.gz"))
			matches2, _ := filepath.Glob(filepath.Join(localDir, "cpmove-*.tar.gz"))
			matches3, _ := filepath.Glob(filepath.Join(localDir, "*.tar"))
			for _, m := range append(append(matches, matches2...), matches3...) {
				_ = os.Remove(m)
			}

			// GH #647 wordpress_ssh: the pull is rsync/db-export shaped (not a
			// panel-account tarball). It stages dump.sql + files.tar.gz and STOPS
			// (operator-gated import, A3) — handle before the tarball switch.
			if job.SourceKind == models.MigrationSourceWordPressSSH {
				// GH #686: the SSH login for a wordpress_ssh source is the user the
				// tenant entered (job.SourceUser) — the SAME value preflight verify
				// uses. The old code fell through to the --ssh-user flag (default
				// "root"), so a Cloudways/VPS master user that disables root SSH
				// failed at Start even though verify passed. Fall back to the flag,
				// then root, only when SourceUser is unset.
				wpSSHUser := job.SourceUser
				if wpSSHUser == "" {
					wpSSHUser = sshUser
				}
				if err := pullWordPressSSH(ctx, wpSSHUser, job, secret, localDir, allowPrivate, repo); err != nil {
					return markPullFailed(err)
				}
				return nil
			}

			// GH #648 wordpress_plugin: PULL from the jabali-migrator plugin's REST
			// API (no SSH). Stages dump.sql + files.tar.gz, then operator-gated.
			if job.SourceKind == models.MigrationSourceWordPressPlugin {
				if err := pullWordPressPlugin(ctx, job, secret, localDir, allowPrivate, repo); err != nil {
					return markPullFailed(err)
				}
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(),
				"connecting to %s@%s (kind=%s)...\n",
				sshUser, job.SourceHost, job.SourceKind)

			var localTar string
			switch job.SourceKind {
			case models.MigrationSourceCpanel, models.MigrationSourceWHMpkgacct:
				// WHM = cpanel restore code-path. The wizard discovers
				// accounts via `whmapi1 listaccts` then this command
				// runs pkgacct per account through the same SSH
				// session cpanel uses. Source row's SourceUser is the
				// individual cPanel account from the discover step.
				localTar, err = pullCpanel(ctx, sshUser, job, secret, localDir, allowPrivate)
			case models.MigrationSourceDirectAdmin:
				// DA preflight pivot: SSH principal (root/admin) is almost
				// never a DA hosting account. Auto-resolve the real account
				// so BackupUser targets it + the tarball file lands at
				// user.<real>.tar.gz to match what analyze + restore expect.
				if job.SourceUser == "root" || job.SourceUser == "admin" {
					if pivoted, perr := preflightDAPivot(ctx, job); perr == nil && pivoted != "" && pivoted != job.SourceUser {
						if uErr := repo.UpdateSourceUser(ctx, job.ID, pivoted); uErr == nil {
							fmt.Fprintf(cmd.OutOrStdout(),
								"  → DA source-user pivoted from %q to %q (real hosting account)\n",
								job.SourceUser, pivoted)
							job.SourceUser = pivoted
						}
					}
				}
				localTar, err = pullDirectAdmin(ctx, sshUser, job, secret, localDir, allowPrivate)
			case models.MigrationSourceHestia:
				localTar, err = pullHestia(ctx, sshUser, job, secret, localDir, allowPrivate)
			case models.MigrationSourceCloudPanel:
				localTar, err = pullCloudPanel(ctx, sshUser, job, secret, localDir, allowPrivate)
			case models.MigrationSourceCyberPanel:
				localTar, err = pullCyberPanel(ctx, sshUser, job, secret, localDir, allowPrivate)
			case models.MigrationSourcePlesk:
				localTar, err = pullPlesk(ctx, sshUser, job, secret, localDir, allowPrivate)
			default:
				return markPullFailed(fmt.Errorf("unknown source kind %q", job.SourceKind))
			}
			if err != nil {
				return markPullFailed(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "tarball pulled: %s\n", localTar)

			// Extract.
			extractDir := filepath.Join(localDir, "extracted")
			if err := os.MkdirAll(extractDir, 0o750); err != nil {
				return markPullFailed(fmt.Errorf("mkdir extract dir: %w", err))
			}
			// JAB-44: disk preflight BEFORE extract (the offline path got this in
			// JAB-41 but the live pull-source path skipped it) — a large/highly
			// compressible backup could fill the staging filesystem and leave a
			// partial tree. Fails with required vs available bytes before any write.
			if derr := migrate.CheckExtractDiskSpace(localTar, extractDir); derr != nil {
				return markPullFailed(fmt.Errorf("disk preflight before extract: %w", derr))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "extracting to %s...\n", extractDir)
			if err := migrate.ExtractTarGz(localTar, extractDir); err != nil {
				return markPullFailed(fmt.Errorf("extract: %w", err))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "tarball extracted at %s\n", extractDir)

			// GH #429: the Plesk cpmove is metadata-only (databases.txt
			// manifest, no bundled dumps — a real box had a 6.9 GB DB).
			// Stream each DB straight into the extracted mysql/ tree so the
			// cpanel ImportDatabases writer finds it. Re-connect (pullPlesk
			// closed its session); read-only mysqldump, no source staging.
			if job.SourceKind == models.MigrationSourcePlesk {
				if perr := populatePleskDBsAfterExtract(ctx, sshUser, job, secret, extractDir, allowPrivate); perr != nil {
					return markPullFailed(fmt.Errorf("plesk db populate: %w", perr))
				}
			}
			// Auto-kick import via the agent so the operator's "discover →
			// select → continue" expectation lands at done, not at pending-
			// with-tarball. CLI defaults (above) for target user/email/
			// password mean import doesn't need flags when source provides
			// contactemail. Best-effort: a dispatch failure leaves the job
			// in pending so manual `jabali migrate import` still works.
			if sharedAgent != nil {
				kickCtx, kickCancel := context.WithTimeout(ctx, 10*time.Second)
				defer kickCancel()
				if _, err := sharedAgent.Call(kickCtx, "migration.import_run", map[string]any{
					"job_id":      jobID,
					"target_user": job.SourceUser,
				}); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"  (warning: could not auto-kick import: %v — run `jabali migrate import --job-id %s` manually)\n",
						err, jobID)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(),
						"  → import dispatched (systemd unit jabali-migrate-import-%s.service)\n", jobID)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&jobID, "job-id", "", "migration_jobs.id (ULID) — required")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH login on the source (default 'root')")
	return cmd
}

func pullCpanel(ctx context.Context, sshUser string, job *models.MigrationJob, secret migrate.SecretRef, localDir string, allowPrivate bool) (string, error) {
	d := cpanel.New()
	d.AllowPrivate = allowPrivate
	d.Port = srcSSHPort(job)
	s, err := d.Connect(ctx, job.SourceHost, sshUser, secret)
	if err != nil {
		return "", fmt.Errorf("cpanel.Connect: %w", err)
	}
	defer func() { _ = d.Close(ctx, s) }()
	fmt.Printf("  → running /scripts/pkgacct %s on source (may take many minutes for large accounts)...\n", job.SourceUser)
	remoteTar, err := d.Pkgacct(ctx, s, job.SourceUser)
	if err != nil {
		return "", fmt.Errorf("pkgacct: %w", err)
	}
	fmt.Printf("  → tarball ready on source: %s — downloading...\n", remoteTar)
	localTar := filepath.Join(localDir, fmt.Sprintf("cpmove-%s.tar.gz", job.SourceUser))
	if _, err := d.PullFile(ctx, s, remoteTar, localTar); err != nil {
		return "", fmt.Errorf("PullFile: %w", err)
	}
	// M35.8: clean up source-side cpmove tarball so a multi-account
	// migration doesn't accumulate GB on the source. Best-effort.
	if rmErr := d.RemoveRemote(ctx, s, remoteTar); rmErr != nil {
		fmt.Printf("  (warning: source-side rm %s failed: %v)\n", remoteTar, rmErr)
	}
	return localTar, nil
}

// pullCloudPanel (GH #522) connects to a CloudPanel source over SSH and runs
// BackupUser, which synthesizes a cpmove-shaped tarball from CloudPanel's
// SQLite inventory (cp/<user>, mysql/<db>.sql, cron/<user>, domains-paths.txt,
// homedir/.ssh) — the same DA-style approach so the shared cpanel restore
// writers consume it unchanged. The tarball lands at cpmove-<user>.tar.gz to
// match what the extract + import stages expect.
func pullCloudPanel(ctx context.Context, sshUser string, job *models.MigrationJob, secret migrate.SecretRef, localDir string, allowPrivate bool) (string, error) {
	d := cloudpanel.New()
	d.AllowPrivate = allowPrivate
	d.Port = srcSSHPort(job)
	s, err := d.Connect(ctx, job.SourceHost, sshUser, secret)
	if err != nil {
		return "", fmt.Errorf("cloudpanel.Connect: %w", err)
	}
	defer func() { _ = d.Close(ctx, s) }()
	fmt.Printf("  → synthesizing cpmove for %s on the CloudPanel source (dumping databases)...\n", job.SourceUser)
	remoteTar, err := d.BackupUser(ctx, s, job.SourceUser)
	if err != nil {
		return "", fmt.Errorf("cloudpanel BackupUser: %w", err)
	}
	fmt.Printf("  → tarball ready on source: %s — downloading...\n", remoteTar)
	localTar := filepath.Join(localDir, fmt.Sprintf("cpmove-%s.tar.gz", job.SourceUser))
	if _, err := d.PullFile(ctx, s, remoteTar, localTar); err != nil {
		return "", fmt.Errorf("PullFile: %w", err)
	}
	// JAB-50: don't leave the full account backup on the source server.
	if rmErr := d.RemoveRemote(ctx, s, remoteTar); rmErr != nil {
		fmt.Printf("  (warning: source-side rm %s failed: %v)\n", remoteTar, rmErr)
	}
	return localTar, nil
}

// pullCyberPanel (GH #522 follow-on) connects to a CyberPanel source over SSH
// and runs BackupUser, which synthesises a cpmove-shaped tarball from
// CyberPanel's MySQL inventory (cp/<domain>, mysql/<db>.sql, cron/<domain>,
// domains-paths.txt, homedir/.ssh) — the same DA-style approach so the shared
// cpanel restore writers consume it unchanged. The CyberPanel account IS the
// primary domain; the tarball lands at cpmove-<domain>.tar.gz.
func pullCyberPanel(ctx context.Context, sshUser string, job *models.MigrationJob, secret migrate.SecretRef, localDir string, allowPrivate bool) (string, error) {
	d := cyberpanel.New()
	d.AllowPrivate = allowPrivate
	d.Port = srcSSHPort(job)
	s, err := d.Connect(ctx, job.SourceHost, sshUser, secret)
	if err != nil {
		return "", fmt.Errorf("cyberpanel.Connect: %w", err)
	}
	defer func() { _ = d.Close(ctx, s) }()
	fmt.Printf("  → synthesizing cpmove for %s on the CyberPanel source (dumping databases)...\n", job.SourceUser)
	remoteTar, err := d.BackupUser(ctx, s, job.SourceUser)
	if err != nil {
		return "", fmt.Errorf("cyberpanel BackupUser: %w", err)
	}
	fmt.Printf("  → tarball ready on source: %s — downloading...\n", remoteTar)
	localTar := filepath.Join(localDir, fmt.Sprintf("cpmove-%s.tar.gz", job.SourceUser))
	if _, err := d.PullFile(ctx, s, remoteTar, localTar); err != nil {
		return "", fmt.Errorf("PullFile: %w", err)
	}
	// JAB-50: don't leave the full account backup on the source server.
	if rmErr := d.RemoveRemote(ctx, s, remoteTar); rmErr != nil {
		fmt.Printf("  (warning: source-side rm %s failed: %v)\n", remoteTar, rmErr)
	}
	return localTar, nil
}

// pullWordPressSSH (GH #647) connects to an SSH source, discovers the WordPress
// install, exports its DB (wp db export — no password in argv) into
// <staging>/dump.sql, and streams the file tree into <staging>/files.tar.gz.
// It does NOT import: per A3 the destructive import (migration.import_wp) is
// operator-gated, so this leaves the job staged + in validating state.
// wpMigStartStage/wpMigDoneStage write migration_stages rows so the GUI shows
// progress instead of an idle "pending" during a long WordPress pull (GH #668).
func wpMigStartStage(ctx context.Context, repo repository.MigrationJobRepository, jobID, name string) string {
	id := ids.NewULID()
	now := time.Now().UTC()
	_ = repo.CreateStage(ctx, &models.MigrationStage{
		ID: id, JobID: jobID, StageName: name, State: "running",
		StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	})
	return id
}
func wpMigDoneStage(ctx context.Context, repo repository.MigrationJobRepository, id string) {
	if id != "" {
		_ = repo.UpdateStage(ctx, id, "done", 0, nil)
	}
}

func pullWordPressSSH(ctx context.Context, sshUser string, job *models.MigrationJob, secret migrate.SecretRef, localDir string, allowPrivate bool, repo repository.MigrationJobRepository) error {
	sess, err := wordpressssh.Connect(ctx, job.SourceHost, srcSSHPort(job), sshUser, secret, allowPrivate)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer sess.Close()

	hint := ""
	if job.SourcePath != nil {
		hint = *job.SourcePath
	}
	facts, err := wordpressssh.DiscoverWordPress(ctx, sess, hint)
	if err != nil {
		return fmt.Errorf("discover WordPress: %w", err)
	}
	// Persist the discovered root + facts (source_path + manifest_json).
	_ = repo.UpdateSourcePath(ctx, job.ID, facts.Root)
	if mj, e := json.Marshal(facts); e == nil {
		_ = repo.UpdateManifest(ctx, job.ID, string(mj))
	}

	stageSQL := filepath.Join(localDir, "dump.sql")
	stageTar := filepath.Join(localDir, "files.tar.gz")
	_ = repo.UpdateState(ctx, job.ID, models.MigrationStateAnalyzing, nil)
	stAnalyze := wpMigStartStage(ctx, repo, job.ID, "analyze")
	fmt.Printf("  \u2192 exporting DB from %s ...\n", facts.Root)
	if err := sess.ExportDatabase(ctx, facts.Root, job.ID, stageSQL); err != nil {
		return err
	}
	fmt.Printf("  \u2192 streaming files from %s ...\n", facts.Root)
	if err := sess.PullFilesTarball(ctx, facts.Root, stageTar); err != nil {
		return err
	}
	// Operator-gated import (A3): staged, awaiting `import_wp`. Not auto-kicked.
	wpMigDoneStage(ctx, repo, stAnalyze)
	wpMigDoneStage(ctx, repo, wpMigStartStage(ctx, repo, job.ID, "validate"))
	wpMigDoneStage(ctx, repo, stAnalyze)
	wpMigDoneStage(ctx, repo, wpMigStartStage(ctx, repo, job.ID, "validate"))
	wpMigDoneStage(ctx, repo, stAnalyze)
	wpMigDoneStage(ctx, repo, wpMigStartStage(ctx, repo, job.ID, "validate"))
	_ = repo.UpdateState(ctx, job.ID, models.MigrationStateValidating, nil)
	fmt.Printf("  \u2192 WordPress site staged (dump.sql + files.tar.gz) at %s\n", localDir)
	maybeAutoImportWP(ctx, job)
	return nil
}

// maybeAutoImportWP fire-and-forgets migration.import_wp_run when the job carries
// a destination (set at create for the tenant/background flow), so a migration
// completes without the operator keeping a UI open. Best-effort: a dispatch
// failure just leaves the job staged for a manual import.
func maybeAutoImportWP(ctx context.Context, job *models.MigrationJob) {
	resolveOrCreateDestDomain(ctx, job) // SSH auto-detect: derive + create the dest domain if none
	if sharedAgent == nil || job.DestUser == nil || job.DestDomain == nil || *job.DestUser == "" || *job.DestDomain == "" {
		return
	}
	// Show the migration in the Applications table as "installing" up front, so
	// the operator sees it processing during the whole import (GH #647/#648).
	ensureMigrationAppRow(ctx, job)
	kickCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := sharedAgent.Call(kickCtx, "migration.import_wp_run", map[string]any{
		"job_id":      job.ID,
		"dest_user":   *job.DestUser,
		"dest_domain": *job.DestDomain,
	}); err != nil {
		fmt.Printf("  (warning: auto-import dispatch failed: %v — import manually)\n", err)
	} else {
		fmt.Printf("  \u2192 import auto-dispatched (background)\n")
	}
}

// ensureMigrationAppRow creates a placeholder application_install row
// (status=installing) for the dest domain if one doesn't exist yet, so the
// migration shows in the Applications table while the import runs. Find-first
// avoids duplicating the row the import also touches.
func ensureMigrationAppRow(ctx context.Context, job *models.MigrationJob) {
	dom, err := repository.NewDomainRepository(sharedDB).FindByName(ctx, *job.DestDomain)
	if err != nil || dom == nil {
		return
	}
	appRepo := repository.NewApplicationInstallRepository(sharedDB)
	if existing, _ := appRepo.FindByDomainAndSubdirectoryAndAppType(ctx, dom.ID, "", "wordpress"); existing != nil {
		return
	}
	email := "migrated@" + *job.DestDomain
	if u, e := repository.NewUserRepository(sharedDB).FindByID(ctx, *job.TargetUserID); e == nil && u.Email != "" {
		email = u.Email
	}
	_ = appRepo.Create(ctx, &models.ApplicationInstall{
		ID: ids.NewULID(), UserID: *job.TargetUserID, DomainID: dom.ID,
		AppType: "wordpress", Subdirectory: "", Status: "installing",
		AdminUsername: "admin", AdminEmail: email, Locale: "en_US",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
}

// resolveOrCreateDestDomain: SSH auto-detect flow. When a migration has no
// destination domain, derive it from the source siteurl and create a Jabali
// domain for the target user (owned + provisioned by the reconciler), so the
// migrated site lands on its own domain without the operator pre-creating it.
func resolveOrCreateDestDomain(ctx context.Context, job *models.MigrationJob) {
	if job.DestDomain != nil && *job.DestDomain != "" {
		return
	}
	if job.TargetUserID == nil || *job.TargetUserID == "" || job.ManifestJSON == nil {
		return
	}
	var facts struct {
		SiteURL string `json:"siteurl"`
	}
	_ = json.Unmarshal([]byte(*job.ManifestJSON), &facts)
	dom := deriveDomainFromURL(facts.SiteURL)
	if dom == "" {
		return
	}
	uid := *job.TargetUserID
	domains := repository.NewDomainRepository(sharedDB)
	if existing, err := domains.FindByName(ctx, dom); err == nil && existing != nil {
		if existing.UserID != uid {
			fmt.Printf("  (warning: domain %s exists under another user — not auto-using)\n", dom)
			return
		}
	} else {
		u, uerr := repository.NewUserRepository(sharedDB).FindByID(ctx, uid)
		if uerr != nil || u.Username == nil || *u.Username == "" {
			return
		}
		docRoot := filepath.Join("/home", *u.Username, "domains", dom, "public_html")
		now := time.Now().UTC()
		me, sk := models.DeriveMailFlags(models.MailProviderNone)
		row := &models.Domain{
			ID: ids.NewULID(), UserID: uid, Name: dom, DocRoot: docRoot,
			IsEnabled: true, SSLMode: models.SSLModeLE, SSLEnabled: models.SSLEnabledForMode(models.SSLModeLE),
			MailProvider: models.MailProviderNone, EmailEnabled: me, SkipAutoSAN: sk,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := domains.Create(ctx, row); err != nil {
			fmt.Printf("  (warning: auto-create domain %s failed: %v)\n", dom, err)
			return
		}
		fmt.Printf("  \u2192 auto-created domain %s (provisioning in background)\n", dom)
	}
	_ = repository.NewMigrationJobRepository(sharedDB).UpdateDestDomain(ctx, job.ID, dom)
	job.DestDomain = &dom
}

// deriveDomainFromURL extracts the bare domain (no scheme, no www) from a URL.
func deriveDomainFromURL(siteurl string) string {
	u, err := url.Parse(strings.TrimSpace(siteurl))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// pullWordPressPlugin (GH #648) pulls a WordPress site from the jabali-migrator
// plugin's token-authed REST API over an SSRF/rebind-safe client, stages
// dump.sql + files.tar.gz, and stops in validating state (operator-gated import
// via migrate import-wp — the SHARED spine). No SSH, no public Jabali ingress.
func pullWordPressPlugin(ctx context.Context, job *models.MigrationJob, secret migrate.SecretRef, localDir string, allowPrivate bool, repo repository.MigrationJobRepository) error {
	token, err := readPluginToken(secret.Path)
	if err != nil {
		return fmt.Errorf("read plugin token: %w", err)
	}
	siteURL := job.SourceHost
	if !strings.HasPrefix(siteURL, "http://") && !strings.HasPrefix(siteURL, "https://") {
		siteURL = "https://" + siteURL
	}
	cli := wordpressplugin.New(siteURL, token, allowPrivate)
	if err := cli.Ping(ctx); err != nil {
		return fmt.Errorf("plugin ping (check URL + token): %w", err)
	}
	facts, err := cli.Manifest(ctx)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if facts.WPRoot != "" {
		_ = repo.UpdateSourcePath(ctx, job.ID, facts.WPRoot)
	}
	if mj, e := json.Marshal(facts); e == nil {
		_ = repo.UpdateManifest(ctx, job.ID, string(mj))
	}
	_ = repo.UpdateState(ctx, job.ID, models.MigrationStateAnalyzing, nil)
	stAnalyze := wpMigStartStage(ctx, repo, job.ID, "analyze")
	fmt.Printf("  \u2192 exporting DB from %s ...\n", siteURL)
	if err := cli.ExportDatabase(ctx, filepath.Join(localDir, "dump.sql")); err != nil {
		return err
	}
	// Budget: 2x the manifest's file_bytes (loose headroom) as a runaway guard.
	budget := facts.FileBytes * 2
	fmt.Printf("  \u2192 fetching files from %s ...\n", siteURL)
	if err := cli.PullFilesTarball(ctx, filepath.Join(localDir, "files.tar.gz"), budget); err != nil {
		return err
	}
	wpMigDoneStage(ctx, repo, stAnalyze)
	wpMigDoneStage(ctx, repo, wpMigStartStage(ctx, repo, job.ID, "validate"))
	_ = repo.UpdateState(ctx, job.ID, models.MigrationStateValidating, nil)
	fmt.Printf("  \u2192 WordPress site staged (dump.sql + files.tar.gz) at %s\n", localDir)
	maybeAutoImportWP(ctx, job)
	return nil
}

// readPluginToken pulls PLUGIN_TOKEN=... from the per-job secret file.
func readPluginToken(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "PLUGIN_TOKEN=") {
			return strings.TrimPrefix(line, "PLUGIN_TOKEN="), nil
		}
	}
	return "", fmt.Errorf("PLUGIN_TOKEN not found in secret file")
}

func pullDirectAdmin(ctx context.Context, sshUser string, job *models.MigrationJob, secret migrate.SecretRef, localDir string, allowPrivate bool) (string, error) {
	d := directadmin.New()
	d.AllowPrivate = allowPrivate
	d.Port = srcSSHPort(job)
	s, err := d.Connect(ctx, job.SourceHost, sshUser, secret)
	if err != nil {
		return "", fmt.Errorf("directadmin.Connect: %w", err)
	}
	defer func() { _ = d.Close(ctx, s) }()
	remoteTar, err := d.BackupUser(ctx, s, job.SourceUser)
	if err != nil {
		return "", fmt.Errorf("system_backup_user: %w", err)
	}
	localTar := filepath.Join(localDir, fmt.Sprintf("user.%s.tar.gz", job.SourceUser))
	if _, err := d.PullFile(ctx, s, remoteTar, localTar); err != nil {
		return "", fmt.Errorf("PullFile: %w", err)
	}
	// JAB-50: don't leave the full account backup on the source server.
	if rmErr := d.RemoveRemote(ctx, s, remoteTar); rmErr != nil {
		fmt.Printf("  (warning: source-side rm %s failed: %v)\n", remoteTar, rmErr)
	}
	return localTar, nil
}

// pullPlesk stages the Plesk source's cpmove METADATA tarball into localDir,
// mirroring pullDirectAdmin. The tarball is tiny (~KB) — DB dumps + docroots
// + Maildirs are manifested inside it (databases.txt / domains-paths.txt /
// mail-paths.txt) and streamed/rsynced by the import step, NOT bundled here
// (a real Plesk box carried a 6.9 GB DB). SourceUser is the subscription
// (primary domain). Read-only on the source; stages locally + stops.
func pullPlesk(ctx context.Context, sshUser string, job *models.MigrationJob, secret migrate.SecretRef, localDir string, allowPrivate bool) (string, error) {
	d := plesk.New()
	d.AllowPrivate = allowPrivate
	d.Port = srcSSHPort(job)
	s, err := d.Connect(ctx, job.SourceHost, sshUser, secret)
	if err != nil {
		return "", fmt.Errorf("plesk.Connect: %w", err)
	}
	defer func() { _ = d.Close(ctx, s) }()
	remoteTar, err := d.BackupUser(ctx, s, job.SourceUser)
	if err != nil {
		return "", fmt.Errorf("plesk cpmove synthesize: %w", err)
	}
	localTar := filepath.Join(localDir, fmt.Sprintf("user.%s.tar.gz", job.SourceUser))
	if _, err := d.PullFile(ctx, s, remoteTar, localTar); err != nil {
		return "", fmt.Errorf("PullFile: %w", err)
	}
	// JAB-50: don't leave the metadata backup (DB dumps manifest, contact
	// email) on the source server.
	if rmErr := d.RemoveRemote(ctx, s, remoteTar); rmErr != nil {
		fmt.Printf("  (warning: source-side rm %s failed: %v)\n", remoteTar, rmErr)
	}
	return localTar, nil
}

// populatePleskDBsAfterExtract re-connects to the Plesk source and streams
// each manifested database into extractDir/cpmove-<slug>/mysql/<db>.sql, so
// the cpanel ImportDatabases writer consumes them. Read-only on the source;
// the dump is streamed straight to disk (never buffered — 6.9 GB DBs exist).
func populatePleskDBsAfterExtract(ctx context.Context, sshUser string, job *models.MigrationJob, secret migrate.SecretRef, extractDir string, allowPrivate bool) error {
	d := plesk.New()
	d.AllowPrivate = allowPrivate
	d.Port = srcSSHPort(job)
	s, err := d.Connect(ctx, job.SourceHost, sshUser, secret)
	if err != nil {
		return fmt.Errorf("plesk.Connect: %w", err)
	}
	defer func() { _ = d.Close(ctx, s) }()
	slug := plesk.Slug(job.SourceUser)
	n, err := plesk.PopulatePleskDBs(extractDir, slug, func(cmd, dst string) error {
		return d.StreamDBDump(ctx, s, cmd, dst)
	})
	if err != nil {
		return err
	}
	fmt.Printf("  \u2192 streamed %d database(s) into the cpmove tree\n", n)
	return nil
}

func pullHestia(ctx context.Context, sshUser string, job *models.MigrationJob, secret migrate.SecretRef, localDir string, allowPrivate bool) (string, error) {
	d := hestiacp.New()
	d.AllowPrivate = allowPrivate
	d.Port = srcSSHPort(job)
	s, err := d.Connect(ctx, job.SourceHost, sshUser, secret)
	if err != nil {
		return "", fmt.Errorf("hestiacp.Connect: %w", err)
	}
	defer func() { _ = d.Close(ctx, s) }()
	remoteTar, err := d.BackupUser(ctx, s, job.SourceUser)
	if err != nil {
		return "", fmt.Errorf("v-backup-user: %w", err)
	}
	// Use the remote filename's basename so .tar vs .tar.gz preserved.
	base := filepath.Base(remoteTar)
	if base == "" {
		base = fmt.Sprintf("%s.tar", job.SourceUser)
	}
	localTar := filepath.Join(localDir, base)
	if _, err := d.PullFile(ctx, s, remoteTar, localTar); err != nil {
		return "", fmt.Errorf("PullFile: %w", err)
	}
	// JAB-50: remove the source-side backup (validated against the account).
	if rmErr := d.RemoveRemote(ctx, s, job.SourceUser, remoteTar); rmErr != nil {
		fmt.Printf("  (warning: source-side rm %s failed: %v)\n", remoteTar, rmErr)
	}
	return localTar, nil
}

// extractTar streams a .tar or .tar.gz into dest. Uses the same
// path-escape + size-cap hardening as cpanel.ParseTarball; doesn't
// classify entries since the per-importer parser does that.

// srcSSHPort returns the migration job's source SSH port, defaulting to 22
// when unset (GH #429).
func srcSSHPort(job *models.MigrationJob) int {
	if job != nil && job.SourcePort >= 1 && job.SourcePort <= 65535 {
		return job.SourcePort
	}
	return 22
}
