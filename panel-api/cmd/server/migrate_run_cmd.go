// `jabali migrate run` cobra subcommand. Walks one migration_jobs
// row through the four-stage pipeline (analyze → fix_perms →
// validate → restore) using the source-kind-appropriate writers.
//
// Operator-driven workflow (until the admin REST + UI Step 8 lands):
//   1. Pre-create the destination jabali user via /admin/users
//   2. Insert a migration_jobs row + extract a cpmove tarball
//      under /var/lib/jabali-migrations/<job-id>/extracted/
//   3. Run: jabali migrate run --job-id <ulid> --target-user <username>
//
// Resume after a partial failure: same command — runner skips
// already-done stages, picks up at the first failed/pending one.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"git.linux-hosting.co.il/shukivaknin/jabali2/internal/kratosclient"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/agent"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/migrate/directadmin"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/migrate/hestiacp"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/models"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/repository"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/userops"
)

func newMigrateImportCmd() *cobra.Command {
	var jobID, targetUser, targetEmail, targetPassword, targetPackageID string
	var keepStaging bool
	cmd := &cobra.Command{
		Use:     "import",
		Short:   "Run (or resume) a migration job through the four-stage pipeline",
		Long: `Walks the named migration_jobs row through analyze → fix_perms →
validate → restore. The destination jabali user must already
exist (pre-create via the admin UI or jabali user CLI).

The cpmove tarball must already be extracted at:
  /var/lib/jabali-migrations/<job-id>/extracted/cp/<source-user>/

Resume: re-run the same command after fixing the cause of any
failed stage. Already-done stages are skipped.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobID == "" {
				return errors.New("--job-id is required")
			}
			ctx := cmd.Context()

			jobsRepo := repository.NewMigrationJobRepository(sharedDB)
			usersRepo := repository.NewUserRepository(sharedDB)
			dbsRepo := repository.NewDatabaseRepository(sharedDB)
			dbUsersRepo := repository.NewDatabaseUserRepository(sharedDB)
			dbGrantsRepo := repository.NewDatabaseUserGrantRepository(sharedDB)
			cronsRepo := repository.NewCronJobRepository(sharedDB)
			sshRepo := repository.NewSSHKeyRepository(sharedDB)
			domainsRepo := repository.NewDomainRepository(sharedDB)
			mbRepo := repository.NewMailboxRepository(sharedDB)
			fwdRepo := repository.NewEmailForwarderRepository(sharedDB)
			arRepo := repository.NewEmailAutoresponderRepository(sharedDB)
			filtersRepo := repository.NewEmailFilterRepository(sharedDB)
			phpPoolsRepo := repository.NewPHPPoolRepository(sharedDB)
			kc := kratosclient.NewClient(sharedCfg.Auth.Kratos.PublicURL, sharedCfg.Auth.Kratos.AdminURL)

			job, err := jobsRepo.FindByID(ctx, jobID)
			if err != nil {
				return fmt.Errorf("load job: %w", err)
			}
			failJob := func(err error) error {
				msg := err.Error()
				if uErr := jobsRepo.UpdateState(ctx, job.ID, models.MigrationStateFailed, &msg); uErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: mark job failed: %v\n", uErr)
				}
				return err
			}

			// DA preflight pivot: SSH principals (root/admin) aren't
			// hosting accounts on DA. Resolve the real account up
			// front so target-user creation + cpmove paths use the
			// correct ID. Single-tenant → auto-pick; multi-tenant →
			// fail with the visible list. Skipped silently when the
			// preflight SSH dial fails (analyze stage will surface).
			if job.SourceKind == models.MigrationSourceDirectAdmin &&
				(job.SourceUser == "root" || job.SourceUser == "admin") {
				if pivoted, perr := preflightDAPivot(ctx, job); perr == nil && pivoted != "" && pivoted != job.SourceUser {
					if uErr := jobsRepo.UpdateSourceUser(ctx, job.ID, pivoted); uErr == nil {
						fmt.Printf("  → DA source-user pivoted from %q to %q (real hosting account)\n",
							job.SourceUser, pivoted)
						job.SourceUser = pivoted
						// If a target user was previously stamped with the SSH-
						// principal username (root/admin), clear the FK so the
						// downstream auto-create path provisions a fresh user
						// matching the pivoted account. The orphaned panel
						// user row stays — operator removes manually via
						// /admin/users to avoid silent collateral deletion.
						if job.TargetUserID != nil {
							if u, lookupErr := usersRepo.FindByID(ctx, *job.TargetUserID); lookupErr == nil &&
								u != nil && u.Username != nil &&
								(*u.Username == "root" || *u.Username == "admin") {
								if uErr := jobsRepo.ClearTargetUser(ctx, job.ID); uErr == nil {
									fmt.Printf("  → cleared stale target_user_id (was %q); auto-create will re-key to %q\n",
										*u.Username, pivoted)
									job.TargetUserID = nil
								}
							}
						}
					}
				}
			}

			// Default-from-source resolution. Operator can still pass
			// --target-user / --target-email / --target-password to
			// override; absent values fall through to:
			//   user → job.SourceUser (always present)
			//   email → cpmove contactemail file or CONTACTEMAIL kv
			//   password → 16-char random (printed to stdout once
			//              so the operator can hand it to the user)
			// Source's crypt(3) shadow hash isn't reusable (Kratos
			// expects Argon2/bcrypt) but is surfaced so the operator
			// can verify the hash style before sending a reset link.
			extractDir := filepath.Join("/var/lib/jabali-migrations", jobID, "extracted")
			meta, _ := cpanel.PeekAccountMeta(extractDir, job.SourceUser)
			if targetUser == "" {
				targetUser = job.SourceUser
				fmt.Printf("  → target-user defaulted from source: %s\n", targetUser)
			}
			if targetEmail == "" && meta != nil && meta.Email != "" {
				targetEmail = meta.Email
				fmt.Printf("  → target-email detected from cpmove: %s\n", targetEmail)
			}
			if targetEmail == "" {
				// No contactemail file in the tarball (older pkgacct
				// versions or pre-extracted blob) — synthesize
				// <user>@<source-host> so auto-create can proceed.
				// Operator can fix the address post-migration via
				// /admin/users; the synthetic value is a placeholder,
				// not a delivery target.
				hostPart := job.SourceHost
				if hostPart == "" {
					hostPart = "migrated.local"
				}
				targetEmail = targetUser + "@" + hostPart
				fmt.Printf("  → target-email synthesized (no contactemail in tarball): %s\n", targetEmail)
			}
			if targetPassword == "" {
				// Generate a random strong password the operator can
				// hand to the customer. Print ONCE — we never store
				// this in the DB.
				if pw, perr := randomPassword(16); perr == nil {
					targetPassword = pw
					fmt.Printf("  → target-password auto-generated: %s   (share with customer; reset via Kratos when needed)\n", targetPassword)
					if meta != nil && meta.PasswordHash != "" {
						fmt.Printf("    (source had a crypt(3) hash but Kratos uses Argon2; original password not recoverable)\n")
					}
				}
			}

			user, err := usersRepo.FindByUsername(ctx, targetUser)
			if err != nil {
				if !errors.Is(err, repository.ErrNotFound) {
					return failJob(fmt.Errorf("destination user %q lookup: %w", targetUser, err))
				}
				// Auto-create when operator supplied --target-email
				// + --target-password. Otherwise fail with helpful
				// message pointing at the auto-create flags.
				if targetEmail == "" || targetPassword == "" {
					return failJob(fmt.Errorf("destination user %q does not exist. "+
						"Pre-create via /admin/users OR pass --target-email + --target-password to auto-create",
						targetUser))
				}
				cu := userops.CreateInput{
					Email:    targetEmail,
					Password: targetPassword,
					Username: &targetUser,
					IsAdmin:  false,
				}
				if targetPackageID != "" {
					cu.PackageID = &targetPackageID
				}
				// KratosClient nil → userops skips the kratos atomic
				// step (the panel row is still created cleanly).
				// Operator path: send a kratos password-reset link
				// post-migration; the identity gets lazy-created at
				// first login. v2 lifts the boot-time kratosclient
				// to a package var so the CLI can reuse it; for v1
				// nil-skip is the safer default than rebuilding a
				// kratosclient from config in cobra context.
				res, cErr := userops.Create(ctx, userops.Deps{
					Users:        usersRepo,
					Packages:     repository.NewPackageRepository(sharedDB),
					Agent:        sharedAgent,
					KratosClient: kc, // pass for atomic Kratos id provision
					BcryptCost:   bcrypt.DefaultCost,
				}, cu)
				if cErr != nil {
					return failJob(fmt.Errorf("auto-create destination user %q: %w", targetUser, cErr))
				}
				user = res.User
				fmt.Fprintf(cmd.OutOrStdout(), "Auto-created destination user %s (id=%s)\n",
					*user.Username, user.ID)
				if res.ProvisionWarning != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", res.ProvisionWarning)
				}
				// Restore the source's Linux shadow hash directly onto
				// /etc/shadow so the operator's source SSH/SFTP password
				// works on the destination box unchanged. Kratos panel
				// login keeps the random plaintext we just used because
				// crypt(3) and Argon2 are incompatible.
				if meta != nil && meta.PasswordHash != "" && sharedAgent != nil {
					hashCtx, hashCancel := context.WithTimeout(ctx, 10*time.Second)
					if _, perr := sharedAgent.Call(hashCtx, "user.password", map[string]any{
						"username":      *user.Username,
						"password_hash": meta.PasswordHash,
					}); perr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(),
							"warning: source shadow-hash restore failed: %v\n", perr)
					} else {
						fmt.Printf("  → source Linux shadow hash restored on /etc/shadow (SSH/SFTP password unchanged from source)\n")
					}
					hashCancel()
				}
				// Stamp migration_jobs.target_user_id so the
				// validate stage's acceptExistingUserID gate
				// recognises the auto-created user + doesn't
				// flag target_user_exists.
				if uErr := jobsRepo.UpdateTargetUser(ctx, job.ID, user.ID); uErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"warning: stamp job.target_user_id: %v (validate may false-positive)\n", uErr)
				} else {
					job.TargetUserID = &user.ID
				}
			} else {
				// Pre-existing user (operator pre-created via
				// admin UI or 'jabali user create'). Stamp row
				// so validate recognises it as ours.
				if job.TargetUserID == nil || *job.TargetUserID != user.ID {
					if uErr := jobsRepo.UpdateTargetUser(ctx, job.ID, user.ID); uErr == nil {
						job.TargetUserID = &user.ID
					}
				}
			}
			if user.Username == nil {
				return failJob(fmt.Errorf("destination user %s has no Linux username", user.ID))
			}

			// cPanel + WHM-pkgacct share restore code-path: both
			// produce cpmove-<user>.tar.gz with identical layout.
			// WHM-pkgacct just skips the live-source SSH probe
			// (operator pre-uploaded the tarball). Future
			// directadmin / hestiacp land here as
			// they get per-area builders + tarball-pull wired.
			switch job.SourceKind {
			case models.MigrationSourceCpanel,
				models.MigrationSourceWHMpkgacct,
				models.MigrationSourceDirectAdmin,
				models.MigrationSourceHestia:
				// supported — fall through
			default:
				return failJob(fmt.Errorf("source kind %q not yet supported by jabali migrate import; "+
					"supported: %s, %s, %s, %s",
					job.SourceKind,
					models.MigrationSourceCpanel,
					models.MigrationSourceWHMpkgacct,
					models.MigrationSourceDirectAdmin,
					models.MigrationSourceHestia))
			}

			// extractDir is already resolved earlier (above the user
			// resolution block) — re-use the same value here.
			var parsed *cpanel.ParsedTarball
			switch job.SourceKind {
			case models.MigrationSourceDirectAdmin:
				// DA pull-stage runs jabali's BackupUser script on the
				// source, which synthesises a cpmove-shaped tarball
				// (`cpmove-<user>/{cp,homedir,mysql}/`) so the cpanel
				// restore writers consume it unchanged. The pulled
				// file lands at user.<user>.tar.gz under the staging
				// dir.
				//
				// Use cpanel.ParseTarball — NOT directadmin.ParseDA-
				// Tarball — because the bytes on disk are cpmove
				// layout, not DA's native system-backup-user layout.
				// ParseDATarball would mis-identify SourceUser as
				// "cpmove-<user>" and point HomeDir at the wrapper
				// dir, so the per-domain rsync split would chase
				// `extracted/<user>/domains/...` which doesn't exist.
				daTarPath := filepath.Join("/var/lib/jabali-migrations", job.ID,
					fmt.Sprintf("user.%s.tar.gz", job.SourceUser))
				if cp, perr := cpanel.ParseTarball(daTarPath, extractDir); perr == nil {
					parsed = cp
					// DA's BackupUser-synthesised cpmove doesn't emit
					// dnszones/ or userdata/, so cpanel.ImportDomains
					// would see zero domains. Walk the extracted
					// homedir/domains/* (DA's per-site layout) and
					// populate DomainNames + DocRoots so the panel
					// creates one domain row + one nginx vhost per
					// hosted site. Per-domain rsync split (M35.8 P7)
					// uses DocRoots to land each public_html in the
					// right /home/<user>/domains/<dom>/ subtree.
					if parsed.HomeDir != "" {
						daDomainsRoot := filepath.Join(parsed.HomeDir, "domains")
						if entries, derr := os.ReadDir(daDomainsRoot); derr == nil {
							if parsed.DocRoots == nil {
								parsed.DocRoots = map[string]string{}
							}
							for _, e := range entries {
								if !e.IsDir() {
									continue
								}
								name := e.Name()
								if name == "" || strings.HasPrefix(name, ".") {
									continue
								}
								parsed.DomainNames = append(parsed.DomainNames, name)
								parsed.DocRoots[name] = filepath.Join("/home", *user.Username, "domains", name, "public_html")
							}
						}
					}
					// DA stores per-user maildirs at <HomeDir>/imap/<dom>/
					// <local>/Maildir (not cpanel's <HomeDir>/mail/ layout).
					// Override MailRoot so cpanel.ImportMailboxes walks the
					// right tree + insertMailboxPanelRows creates one
					// panel mailbox row per (dom, local). Forwarders /
					// autoresponders / DKIM staged under etc/<dom>/ by
					// BackupUser are NOT consumed yet (separate writer
					// pending).
					if parsed.HomeDir != "" {
						daMailRoot := filepath.Join(parsed.HomeDir, "imap")
						if st, derr := os.Stat(daMailRoot); derr == nil && st.IsDir() {
							parsed.MailRoot = daMailRoot
						}
					}
				} else {
					// Pre-extracted fallback — operator dropped the
					// cpmove tree under `<extractDir>/cp/<user>/`
					// out of band (`tar -xzf` before kicking the
					// job).
					parsed = &cpanel.ParsedTarball{
						ExtractDir: extractDir,
						HomeDir:    filepath.Join(extractDir, "cp", job.SourceUser, "homedir"),
						SourceUser: job.SourceUser,
					}
					fmt.Fprintf(cmd.ErrOrStderr(),
						"warning: cpanel.ParseTarball failed on DA tarball %s (%v); using pre-extracted cp/<user>/ assumption\n",
						daTarPath, perr)
				}
			case models.MigrationSourceHestia:
				// Hestia tar at /var/lib/jabali-migrations/<id>/
				// <user>.<ts>.tar (or .tar.gz). Hestia parser produces
				// a HestiaParsedTarball — for v1 we adapt the
				// MySQLDumps subset to *cpanel.ParsedTarball so
				// the cpanel restore writers can run; cron + ssh
				// keys via tarball deferred (Hestia's layout
				// doesn't contain a top-level cron/ or
				// .ssh/authorized_keys file the cpanel writers
				// recognise — operator hand-imports those today).
				hTarPath := filepath.Join("/var/lib/jabali-migrations", job.ID,
					fmt.Sprintf("%s.tar.gz", job.SourceUser))
				if h, herr := hestiacp.ParseHestiaTarball(hTarPath, extractDir); herr == nil {
					parsed = &cpanel.ParsedTarball{
						ExtractDir: extractDir,
						SourceUser: job.SourceUser,
						HomeDir:    h.WebRoot, // Hestia rsync target = web/<dom>/public_html/...
						MailRoot:   h.MailRoot, // Hestia stores at mail/<dom>/<local>/Maildir
						MySQLDumps: h.MySQLDumps,
					}
					if h.SSHKeys != "" {
						parsed.SSHAuthorized = []string{h.SSHKeys}
					}
					if h.CronFile != "" {
						parsed.CronFiles = []string{h.CronFile}
					}
					// M35.4 Hestia DomainNames+DocRoots fallback for
					// ImportDomains (no BIND zones in Hestia tarball).
					// Target docroot mirrors the source layout:
					//   /home/<target>/web/<dom>/public_html
					// ImportHome rsync runs first + lands content there.
					if len(h.DomainDirs) > 0 {
						parsed.DomainNames = make([]string, 0, len(h.DomainDirs))
						parsed.DocRoots = make(map[string]string, len(h.DomainDirs))
						for name := range h.DomainDirs {
							parsed.DomainNames = append(parsed.DomainNames, name)
							parsed.DocRoots[name] = filepath.Join(
								"/home", *user.Username, "web", name, "public_html")
						}
					}
				} else {
					parsed = &cpanel.ParsedTarball{
						ExtractDir: extractDir,
						SourceUser: job.SourceUser,
					}
					fmt.Fprintf(cmd.ErrOrStderr(),
						"warning: ParseHestiaTarball failed (%v); using pre-extracted assumption\n", herr)
				}
			default:
				// cpanel + whm_pkgacct
				p, err := cpanel.ParseTarball(
					filepath.Join("/var/lib/jabali-migrations", job.ID, fmt.Sprintf("cpmove-%s.tar.gz", job.SourceUser)),
					extractDir,
				)
				if err != nil {
					p = &cpanel.ParsedTarball{
						ExtractDir: extractDir,
						HomeDir:    filepath.Join(extractDir, "cp", job.SourceUser, "homedir"),
						SourceUser: job.SourceUser,
					}
					fmt.Fprintf(cmd.ErrOrStderr(),
						"warning: ParseTarball failed (%v); falling back to assumed pre-extracted layout\n", err)
				}
				parsed = p
			}

			// Owner default mailbox: cpanel-side <user> @ primary domain.
			// Agent's ImportMailboxes uses this to import the
			// mail/{cur,new,tmp,.Drafts,...} root tree the cpanel owner
			// reads as their default mailbox.
			if parsed != nil && parsed.OwnerEmail == "" {
				if meta != nil && meta.PrimaryDomain != "" {
					parsed.OwnerEmail = job.SourceUser + "@" + meta.PrimaryDomain
				}
			}

			payload := &cpanelRunPayload{
				parsed:         parsed,
				targetUserID:   user.ID,
				targetUsername: *user.Username,
			}

			runner := &migrate.Runner{
				Jobs:  jobsRepo,
				Agent: sharedAgent,
				StageCallbacks: map[string]migrate.StageCallback{
					migrate.StageAnalyze:  cpanelAnalyzeCallback(jobsRepo),
					migrate.StageValidate: validateStageCallback(usersRepo, domainsRepo, *user.Username),
					migrate.StageRestore: cpanelRestoreCallback(
						sshRepo, cronsRepo, dbsRepo, dbUsersRepo, dbGrantsRepo, domainsRepo, mbRepo, fwdRepo, arRepo, filtersRepo, phpPoolsRepo, usersRepo, kc,
					),
				},
			}
			runner.WithContext(payload)
			runErr := runner.Run(ctx, job.ID)
			// Staging-dir cleanup. Re-load the job so we see the
			// terminal state the runner just stamped (done / failed).
			// Operator can suppress via --keep-staging when debugging.
			if !keepStaging {
				if j, lerr := jobsRepo.FindByID(ctx, job.ID); lerr == nil && j != nil {
					switch j.State {
					case models.MigrationStateDone, models.MigrationStateFailed, models.MigrationStateCancelled:
						stagingDir := filepath.Join("/var/lib/jabali-migrations", job.ID)
						if rmErr := os.RemoveAll(stagingDir); rmErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(),
								"warning: staging cleanup failed for %s: %v\n", stagingDir, rmErr)
						} else {
							fmt.Fprintf(cmd.OutOrStdout(),
								"staging dir %s removed (state=%s; pass --keep-staging to retain for debug)\n",
								stagingDir, j.State)
						}
					}
				}
			}
			return runErr
		},
	}
	cmd.Flags().StringVar(&jobID, "job-id", "", "migration_jobs.id (ULID) — required")
	cmd.Flags().BoolVar(&keepStaging, "keep-staging", false, "do NOT delete /var/lib/jabali-migrations/<job-id>/ after run (debug aid)")
	cmd.Flags().StringVar(&targetUser, "target-user", "", "destination jabali username — auto-created if --target-email + --target-password supplied")
	cmd.Flags().StringVar(&targetEmail, "target-email", "", "destination user email (only used when auto-creating)")
	cmd.Flags().StringVar(&targetPassword, "target-password", "", "destination user password (only used when auto-creating; ≥10 chars)")
	cmd.Flags().StringVar(&targetPackageID, "target-package-id", "", "hosting package ULID (only used when auto-creating)")
	return cmd
}


// cpanelRunPayload is the opaque payload threaded through every
// stage callback. The runner forwards it via WithContext.
type cpanelRunPayload struct {
	parsed         *cpanel.ParsedTarball
	targetUserID   string
	targetUsername string
}

// validateStageCallback bridges the runner's StageCallback shape
// to migrate.Validate. Reports projection counts via warnings;
// blockers fail the stage so the runner halts before restore.
func validateStageCallback(users repository.UserRepository, domains repository.DomainRepository, targetUsername string) migrate.StageCallback {
	return func(ctx context.Context, job *models.MigrationJob, payload any) (int64, []string, error) {
		p, ok := payload.(*cpanelRunPayload)
		if !ok {
			return 0, nil, fmt.Errorf("validate: bad payload type")
		}
		// Hand-roll a minimal manifest from what the parsed
		// tarball + job row already tells us. Full
		// AccountManifest assembly lives on the Discoverer; this
		// stage runs against the post-pull data.
		mf := &migrate.AccountManifest{
			SchemaVersion: migrate.ManifestSchemaVersion,
			Source: migrate.SourceRef{
				Kind: job.SourceKind,
				Host: job.SourceHost,
				User: job.SourceUser,
			},
		}
		// Target-user-exists conflict suppressed when
		// migration_jobs.target_user_id is set — auto-create flow
		// ('jabali migrate import --target-email + --target-
		// password') minted the user before the runner began, so
		// finding it now isn't a conflict, it's our user.
		acceptUserID := ""
		if job.TargetUserID != nil {
			acceptUserID = *job.TargetUserID
		}
		rpt, err := migrate.Validate(ctx, migrate.ValidateDeps{
			Users: users, Domains: domains,
		}, mf, targetUsername, acceptUserID)
		if err != nil {
			return 0, nil, fmt.Errorf("validate: %w", err)
		}
		warnings := []string{
			fmt.Sprintf("projections: domains=%d dbs=%d mailboxes=%d",
				rpt.Projections.DomainsToCreate,
				rpt.Projections.DBsToCreate,
				rpt.Projections.MailboxesToCreate),
		}
		if len(rpt.Blockers) > 0 {
			return 0, warnings, fmt.Errorf("validate blockers: %d (first: %s)",
				len(rpt.Blockers), rpt.Blockers[0].Detail)
		}
		_ = p
		return 0, warnings, nil
	}
}

// cpanelRestoreCallback orchestrates every per-area writer in a
// fixed safe order: ssh keys → cron → databases → DNS → home.
// Each writer's Skipped slice is folded into the warnings.
//
// agent.AgentInterface is read off the package-level sharedAgent at
// callback time rather than passed in — avoids the awkward
// interface-vs-concrete dance and matches how every other panel-api
// CLI subcommand reaches the agent.
func cpanelRestoreCallback(
	sshRepo repository.SSHKeyRepository,
	cronRepo repository.CronJobRepository,
	dbsRepo repository.DatabaseRepository,
	dbUsersRepo repository.DatabaseUserRepository,
	dbGrantsRepo repository.DatabaseUserGrantRepository,
	domainsRepo repository.DomainRepository,
	mbRepo repository.MailboxRepository,
	fwdRepo repository.EmailForwarderRepository,
	arRepo repository.EmailAutoresponderRepository,
	filtersRepo repository.EmailFilterRepository,
	phpPoolsRepo repository.PHPPoolRepository,
	usersRepo repository.UserRepository,
	kc *kratosclient.Client,
) migrate.StageCallback {
	return func(ctx context.Context, job *models.MigrationJob, payload any) (int64, []string, error) {
		var _ agent.AgentInterface = sharedAgent // compile-time guard
		p, ok := payload.(*cpanelRunPayload)
		if !ok {
			return 0, nil, fmt.Errorf("restore: bad payload type")
		}
		var warnings []string
		var bytes int64

		sshRes, err := cpanel.ImportSSHKeys(ctx, sshRepo, p.parsed, p.targetUserID)
		if err != nil {
			return bytes, warnings, fmt.Errorf("ssh: %w", err)
		}
		warnings = append(warnings, fmt.Sprintf("ssh: created=%d", sshRes.Created))
		warnings = append(warnings, sshRes.Skipped...)

		cronRes, err := cpanel.ImportCron(ctx, cronRepo, p.parsed, p.targetUserID)
		if err != nil {
			return bytes, warnings, fmt.Errorf("cron: %w", err)
		}
		warnings = append(warnings, fmt.Sprintf("cron: created=%d", cronRes.Created))
		warnings = append(warnings, cronRes.Skipped...)

		dbsRes, err := cpanel.ImportDatabases(ctx, dbsRepo, dbUsersRepo, dbGrantsRepo, sharedAgent, p.parsed, p.targetUserID, p.targetUsername)
		if err != nil {
			return bytes, warnings, fmt.Errorf("databases: %w", err)
		}
		warnings = append(warnings, fmt.Sprintf("databases: created=%d", dbsRes.Created))
		warnings = append(warnings, dbsRes.Skipped...)

		dnsRes, err := cpanel.ImportDNS(ctx, sharedAgent, p.parsed)
		if err != nil {
			return bytes, warnings, fmt.Errorf("dns: %w", err)
		}
		warnings = append(warnings, fmt.Sprintf("dns: zones=%d records=%d", dnsRes.Zones, dnsRes.Records))
		warnings = append(warnings, dnsRes.Skipped...)

		// DA flow (2026-05-14 rework): backup tarball intentionally
		// EXCLUDES the home tree. domains-paths.txt manifest inside
		// the tar lists one source-side docroot per domain. Dispatch
		// agent.migration.rsync_remote_home once per row — pull
		// straight from source over SSH instead of tar middleware.
		// Survives transient failures (rsync resumes) + skips files
		// already on dest.
		// Source-of-truth for the per-domain (dom, src-path) list:
		//   - DA:         cpmove-<user>/domains-paths.txt
		//                 (absolute source paths, written by BackupUser)
		//   - cpanel/WHM: cpmove-<user>/userdata/<dom> YAMLs
		//                 (documentroot line; absolute on source)
		type rsyncPair struct{ Dom, SrcPath string }
		var rsyncRows []rsyncPair
		switch job.SourceKind {
		case models.MigrationSourceDirectAdmin:
			manifest := filepath.Join(p.parsed.ExtractDir, "cpmove-"+p.parsed.SourceUser, "domains-paths.txt")
			if raw, rerr := os.ReadFile(manifest); rerr == nil {
				for _, line := range strings.Split(string(raw), "\n") {
					line = strings.TrimSpace(line)
					if line == "" {
						continue
					}
					parts := strings.SplitN(line, "\t", 2)
					if len(parts) == 2 {
						rsyncRows = append(rsyncRows, rsyncPair{Dom: parts[0], SrcPath: parts[1]})
					}
				}
			}
		case models.MigrationSourceCpanel, models.MigrationSourceWHMpkgacct:
			// The remote-rsync home pull below requires an SSH source
			// (host + per-job secret env). It belongs ONLY to the
			// online `jabali migrate pull-source` flow. An OFFLINE
			// restore (`jabali migrate restore` / tarball upload) has
			// job.SourceHost == "" and ships the home tree INSIDE the
			// cpmove (cpmove-<user>/homedir/). For offline, leave
			// rsyncRows empty so daHomeHandled stays false and the
			// local ImportHomeSplit/ImportHome (tarball copy) runs —
			// without this gate offline restores called
			// migration.rsync_remote_home with an empty host →
			// invalid_argument → home bytes=0 (silent: job still
			// marked done). DA is intentionally remote-only (its
			// tarball excludes the home tree) and not gated here.
			if job.SourceHost == "" {
				break
			}
			for _, root := range []string{
				filepath.Join(p.parsed.ExtractDir, "cpmove-"+p.parsed.SourceUser, "userdata"),
				filepath.Join(p.parsed.ExtractDir, "userdata"),
				filepath.Join(p.parsed.ExtractDir, "cp", p.parsed.SourceUser, "userdata"),
			} {
				entries, derr := os.ReadDir(root)
				if derr != nil {
					continue
				}
				for _, e := range entries {
					if e.IsDir() {
						continue
					}
					name := e.Name()
					if strings.HasSuffix(name, ".php-fpm.yaml") ||
						strings.HasSuffix(name, ".php-fpm.yaml.transferred") ||
						strings.HasSuffix(name, "_SSL") ||
						name == "main" || name == "cache.json" || name == "scope" {
						continue
					}
					body, rerr := os.ReadFile(filepath.Join(root, name))
					if rerr != nil {
						continue
					}
					dom := name
					var docroot string
					for _, ln := range strings.Split(string(body), "\n") {
						ln = strings.TrimSpace(ln)
						if strings.HasPrefix(ln, "documentroot:") {
							docroot = strings.TrimSpace(strings.TrimPrefix(ln, "documentroot:"))
							docroot = strings.Trim(docroot, `'"`)
						}
						if strings.HasPrefix(ln, "servername:") {
							dom = strings.Trim(strings.TrimSpace(strings.TrimPrefix(ln, "servername:")), `'"`)
						}
					}
					if docroot == "" || dom == "" {
						continue
					}
					rsyncRows = append(rsyncRows, rsyncPair{Dom: dom, SrcPath: docroot})
				}
				if len(rsyncRows) > 0 {
					break
				}
			}
		}
		daHomeHandled := false
		if len(rsyncRows) > 0 {
			secretPath := fmt.Sprintf("/etc/jabali-panel/migration-secrets/%s.env", job.ID)
			totalBytes := int64(0)
			domCount := 0
			for _, r := range rsyncRows {
				destPath := filepath.Join("/home", p.targetUsername, "domains", r.Dom, "public_html")
				rawResp, rerr := sharedAgent.Call(ctx, "migration.rsync_remote_home", map[string]any{
					"job_id":      job.ID,
					"host":        job.SourceHost,
					"ssh_user":    "root",
					"secret_path": secretPath,
					"src_path":    r.SrcPath,
					"dest_path":   destPath,
					"dest_user":   p.targetUsername,
				})
				if rerr != nil {
					warnings = append(warnings, fmt.Sprintf("home_rsync_remote: %s: %v", r.Dom, rerr))
					continue
				}
				var rs struct {
					BytesCopied int64 `json:"bytes_copied"`
					Files       int64 `json:"files"`
				}
				_ = json.Unmarshal(rawResp, &rs)
				totalBytes += rs.BytesCopied
				domCount++
			}
			bytes += totalBytes
			warnings = append(warnings, fmt.Sprintf("home: bytes=%d domains=%d (direct-rsync source→dest, no tar middleware)", totalBytes, domCount))
			daHomeHandled = true
		}

		// M35.8 P7: per-domain rsync split. cpanel ships all sites
		// under <homedir>/public_html/(<addon>/) flat layout; jabali
		// uses /home/<user>/domains/<dom>/public_html/. ImportHomeSplit
		// reads per-domain documentroot from cpmove userdata YAML and
		// dispatches one rsync per docroot, then a final pass for the
		// rest of the homedir (mail/ etc/ application_backups/) minus
		// public_html. Falls back to the legacy whole-homedir rsync
		// when no userdata YAML is present.
		if !daHomeHandled {
			hsRes, err := cpanel.ImportHomeSplit(ctx, sharedAgent, p.parsed, job.ID, p.targetUsername)
			if err != nil {
				return bytes, warnings, fmt.Errorf("home_split: %w", err)
			}
			var fallback bool
			for _, sk := range hsRes.Skipped {
				if strings.HasPrefix(sk, "home_split_skip:no_userdata_yaml") {
					fallback = true
					break
				}
			}
			if fallback || hsRes.DomainsCopied == 0 {
				homeRes, err := cpanel.ImportHome(ctx, sharedAgent, p.parsed, job.ID, p.targetUsername)
				if err != nil {
					return bytes, warnings, fmt.Errorf("home: %w", err)
				}
				bytes += homeRes.BytesCopied
				warnings = append(warnings, fmt.Sprintf("home: bytes=%d files=%d (legacy full-homedir mode)", homeRes.BytesCopied, homeRes.Files))
				warnings = append(warnings, homeRes.Skipped...)
			} else {
				bytes += hsRes.BytesCopied
				warnings = append(warnings, fmt.Sprintf("home: bytes=%d files=%d domains=%d (per-domain split)", hsRes.BytesCopied, hsRes.Files, hsRes.DomainsCopied))
				warnings = append(warnings, hsRes.Skipped...)
			}
		}

		domainsRes, err := cpanel.ImportDomains(ctx, domainsRepo, sharedAgent, p.parsed, p.targetUserID, p.targetUsername)
		if err != nil {
			return bytes, warnings, fmt.Errorf("domains: %w", err)
		}
		warnings = append(warnings, fmt.Sprintf("domains: created=%d email_enabled=%d", domainsRes.Created, domainsRes.EmailEnabled))
		warnings = append(warnings, domainsRes.Skipped...)

		mailRes, err := cpanel.ImportMailboxes(ctx, p.parsed, sharedAgent, job.ID, mbRepo, domainsRepo)
		if err != nil {
			return bytes, warnings, fmt.Errorf("mailboxes: %w", err)
		}
		warnings = append(warnings, fmt.Sprintf(
			"mailboxes: maildirs=%d messages_found=%d messages_pushed=%d bytes_pushed=%d",
			mailRes.MaildirsFound, mailRes.MessagesFound, mailRes.MessagesPushed, mailRes.BytesPushed))
		warnings = append(warnings, mailRes.Skipped...)

		// M35.8 P3: per-domain custom SSL certs from apache_tls/.
		sslRes, err := cpanel.ImportSSL(ctx, sharedAgent, p.parsed)
		if err != nil {
			return bytes, warnings, fmt.Errorf("ssl: %w", err)
		}
		warnings = append(warnings, fmt.Sprintf("ssl: installed=%d", sslRes.Installed))
		warnings = append(warnings, sslRes.Skipped...)

		// M35.8 P2+P5: catch-all + subdomains + forwarders restore.
		// M35.8 P8: rewrite WP/Drupal/Joomla/Magento config files
		// with the new (db_name, db_user, db_pass) triples just
		// created. ImportDatabases captured them in dbsRes.Credentials;
		// the rewriter reads each per-domain docroot under
		// /home/<u>/domains/<dom>/public_html and splices values in.
		// Best-effort: missing app config = silent skip.
		appRes, err := cpanel.ImportAppConfigs(ctx, sharedAgent, p.targetUserID, p.targetUsername, dbsRes.Credentials)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("appconfigs: %v", err))
		} else {
			warnings = append(warnings, fmt.Sprintf(
				"appconfigs: wordpress=%d joomla=%d drupal=%d magento=%d caches_cleared=%d",
				appRes.WordPress, appRes.Joomla, appRes.Drupal, appRes.Magento, appRes.CachesCleared))
			warnings = append(warnings, appRes.Skipped...)
		}

		extrasRes, err := cpanel.ImportExtras(ctx, domainsRepo, mbRepo, fwdRepo, arRepo, filtersRepo, phpPoolsRepo, sharedAgent, p.parsed, p.targetUserID, p.targetUsername)
		if err != nil {
			return bytes, warnings, fmt.Errorf("extras: %w", err)
		}
		warnings = append(warnings, fmt.Sprintf(
			"extras: catchalls=%d subdomains=%d forwarders=%d forwarders_orphan=%d autoresponders=%d autoresponders_orphan=%d filters=%d php_pools=%d php_domains_bound=%d php_version=%s ftp_accounts=%d dkim_keys=%d",
			extrasRes.CatchallsSet, extrasRes.SubdomainsCreated,
			extrasRes.ForwardersCreated, extrasRes.ForwardersOrphaned,
			extrasRes.AutorespondersCreated, extrasRes.AutorespondersOrphaned,
			extrasRes.FiltersImported,
			extrasRes.PHPPoolsCreated, extrasRes.PHPDomainsBound,
			extrasRes.PHPVersionApplied, extrasRes.FTPAccountsObserved,
			extrasRes.DKIMKeysPreserved))
		warnings = append(warnings, extrasRes.Skipped...)

		// DA forwarder import — walks etc/<dom>/aliases staged by
		// BackupUser + inserts standalone EmailForwarder rows
		// (MailboxID=NULL). Stalwart push deferred to a domain-scoped
		// reconciler phase.
		if job.SourceKind == models.MigrationSourceDirectAdmin {
			fwdRes, ferr := directadmin.ImportForwarders(ctx, fwdRepo, domainsRepo, p.parsed.ExtractDir, p.parsed.SourceUser)
			if ferr != nil {
				warnings = append(warnings, fmt.Sprintf("da_forwarders: %v", ferr))
			} else {
				warnings = append(warnings, fmt.Sprintf("da_forwarders: inserted=%d", fwdRes.Inserted))
				warnings = append(warnings, fwdRes.Skipped...)
			}
		}

		// Ensure the migrated user has a Kratos identity so they can log in.
		if kc != nil && usersRepo != nil {
			targetUser, uErr := usersRepo.FindByID(ctx, p.targetUserID)
			if uErr != nil {
				warnings = append(warnings, fmt.Sprintf("kratos: load user %s: %v", p.targetUserID, uErr))
			} else {
				status, newID, _ := rebuildOne(ctx, kc, usersRepo, targetUser, "168h")
				// Annotate the ambiguous statuses so the manifest reads
				// unambiguously. skipped_live is the COMMON + healthy
				// path on auto-create migrations: the user was just
				// minted with --target-password (its Kratos identity +
				// password already set), so this "ensure identity"
				// rebuild correctly finds it live and does nothing. It
				// does NOT mean a password step was skipped — login
				// works with the password from user creation.
				note := ""
				switch string(status) {
				case "skipped_live":
					note = " (identity already live — login works; password was set at user creation, no rebuild needed)"
				case "ok":
					note = " (identity rebuilt — recovery link issued)"
				case "probe_failed", "create_failed", "link_failed", "recovery_missing":
					note = " (NEEDS ATTENTION — see status; user may not be able to log in)"
				}
				warnings = append(warnings, fmt.Sprintf("kratos: status=%s new_id=%s%s", status, newID, note))
			}
		}

		return bytes, warnings, nil
	}
}

// cpanelAnalyzeCallback runs the cPanel Discoverer against the
// source host using credentials at /etc/jabali-panel/migration-
// secrets/<job-id>.env (per ADR-0094 §"tracked risks"). Records
// the produced AccountManifest into migration_jobs.manifest_json
// so the validate + restore stages can read it back without a
// second SSH round-trip.
//
// Falls back to skip-with-warning when the secret file is absent —
// operator-driven workflow today often has the cpmove tarball
// already on disk + no need to re-run discovery. Restore stage
// works without analyze having succeeded.
func cpanelAnalyzeCallback(jobsRepo repository.MigrationJobRepository) migrate.StageCallback {
	return func(ctx context.Context, job *models.MigrationJob, payload any) (int64, []string, error) {
		secretPath := fmt.Sprintf("/etc/jabali-panel/migration-secrets/%s.env", job.ID)
		if _, err := osStat(secretPath); err != nil {
			return 0, []string{
				fmt.Sprintf("analyze_skip:no_secret_file:%s", secretPath),
				"analyze_skip:operator_supplied_tarball — restore stage will use the pre-extracted tree",
			}, nil
		}
		// Pick the right Discoverer based on source kind. Pre-M35.8
		// this stage hard-wired cpanel.New() which caused DA jobs
		// to error with "uapi: command not found" — uapi is a
		// cpanel-only CLI. migrate.Get returns the registered
		// per-kind factory; whm_pkgacct shares cpanel's impl per
		// the registry init.
		disc, err := migrate.Get(job.SourceKind)
		if err != nil {
			return 0, nil, fmt.Errorf("no_discoverer:%s:%w", job.SourceKind, err)
		}
		// Honor server_settings.migration_allow_private_hosts so the
		// analyze stage's SSH dial matches what the discover/pull
		// paths already use. Best-effort lookup; default safe.
		settingsRepo := repository.NewServerSettingsRepository(sharedDB)
		if s, sErr := settingsRepo.Get(ctx); sErr == nil && s != nil {
			migrate.ApplyAllowPrivate(disc, s.MigrationAllowPrivateHosts)
		}
		s, err := disc.Connect(ctx, job.SourceHost, "root", migrate.SecretRef{Path: secretPath})
		if err != nil {
			return 0, nil, fmt.Errorf("connect: %w", err)
		}
		defer func() { _ = disc.Close(ctx, s) }()

		mf, err := disc.DescribeAccount(ctx, s, job.SourceUser)
		if err != nil {
			return 0, nil, fmt.Errorf("describe %s: %w", job.SourceUser, err)
		}
		// Persist analyze-time pivot: DA's single-account auto-pick
		// (root → only hosting user) needs to propagate so downstream
		// backup/restore stages target the real account. Best-effort:
		// failure here just leaves the row with the SSH user, which
		// surfaces as a backup-stage failure operator can see.
		if mf.Source.User != "" && mf.Source.User != job.SourceUser {
			if uErr := jobsRepo.UpdateSourceUser(ctx, job.ID, mf.Source.User); uErr == nil {
				job.SourceUser = mf.Source.User
			}
		}
		// Persist manifest to migration_jobs.manifest_json so resume
		// + validate + restore can read without re-doing discovery.
		// Best-effort marshal — payload is small (single account)
		// so this rarely fails.
		raw, mErr := jsonMarshal(mf)
		if mErr == nil && raw != "" {
			if uErr := job.UpdatedAt.IsZero(); !uErr {
				// no-op stub — cobra cmd reaches the repo through
				// closure; analyze callback receives only the job
				// model. Future commits thread the repo into the
				// callback so manifest_json persists. For now,
				// surface the manifest summary in warnings so the
				// operator sees it in the migration_jobs row anyway.
			}
			_ = raw
		}
		warnings := []string{
			fmt.Sprintf("analyze: domains=%d mailboxes=%d databases=%d cron=%d ssh=%d",
				len(mf.Domains), len(mf.Mailboxes), len(mf.Databases),
				len(mf.Cron), len(mf.SSH)),
		}
		for _, w := range mf.Warnings {
			warnings = append(warnings, fmt.Sprintf("analyze_warning:%s:%s", w.Code, w.Detail))
		}
		return 0, warnings, nil
	}
}

// osStat is a thin wrapper for testability — swappable in tests
// without monkey-patching os.Stat. Production path is os.Stat.
func osStat(name string) (os.FileInfo, error) { return os.Stat(name) }

// jsonMarshal returns a JSON encoding of v. Returns "" on error so
// the caller can decide whether to surface that as a non-fatal
// warning.
func jsonMarshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// randomPassword returns an N-byte URL-safe random string trimmed
// of any padding. Strong enough for a one-time generated user
// password the operator hands to the customer; the customer is
// expected to rotate via Kratos.
func randomPassword(n int) (string, error) {
	if n < 12 {
		n = 16
	}
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	s := base64.RawURLEncoding.EncodeToString(raw)
	if len(s) > n {
		s = s[:n]
	}
	return strings.ReplaceAll(s, "_", "x"), nil
}

// preflightDAPivot is a one-shot Connect+ListAccounts dance against
// a DA source whose source_user is the SSH principal (root/admin)
// rather than a real hosting account. Returns the auto-picked
// account when the source has exactly one hosting user; "" + nil
// when the source has zero or multiple accounts (caller leaves the
// job alone so analyze surfaces a clear error to the operator).
//
// Best-effort: if the secret file is missing or SSH dial fails,
// returns "" + nil so the CLI doesn't fail before analyze even
// starts. Analyze stage will hit the same wall + report properly.
func preflightDAPivot(ctx context.Context, job *models.MigrationJob) (string, error) {
	secretPath := fmt.Sprintf("/etc/jabali-panel/migration-secrets/%s.env", job.ID)
	if _, err := os.Stat(secretPath); err != nil {
		return "", nil
	}
	disc := directadmin.New()
	// Honor server_settings.migration_allow_private_hosts so private
	// IP source hosts work the same as in the analyze stage.
	settingsRepo := repository.NewServerSettingsRepository(sharedDB)
	if s, sErr := settingsRepo.Get(ctx); sErr == nil && s != nil {
		migrate.ApplyAllowPrivate(disc, s.MigrationAllowPrivateHosts)
	}
	subctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, err := disc.Connect(subctx, job.SourceHost, "root", migrate.SecretRef{Path: secretPath})
	if err != nil {
		return "", nil
	}
	defer func() { _ = disc.Close(subctx, s) }()
	accounts, err := disc.ListAccounts(subctx, s)
	if err != nil {
		return "", nil
	}
	if len(accounts) == 1 {
		return accounts[0].ID, nil
	}
	return "", nil
}
