// `jabali migrate run` cobra subcommand. Walks one migration_jobs
// row through the four-stage pipeline (analyze → fix_perms →
// validate → restore) using the source-kind-appropriate writers.
//
// Operator-driven workflow (until the admin REST + UI Step 8 lands):
//  1. Pre-create the destination jabali user via /admin/users
//  2. Insert a migration_jobs row + extract a cpmove tarball
//     under /var/lib/jabali-migrations/<job-id>/extracted/
//  3. Run: jabali migrate run --job-id <ulid> --target-user <username>
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

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/directadmin"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/hestiacp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/plesk"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

func newMigrateImportCmd() *cobra.Command {
	var jobID, targetUser, targetEmail, targetPassword, targetPackageID string
	var keepStaging bool
	var allowDegraded bool
	var preserveSourceState bool
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Run (or resume) a migration job through the four-stage pipeline",
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
			// #496: accept the target password from the environment so it never
			// has to ride argv (/proc/<pid>/cmdline). The agent's systemd-run
			// passes it via an EnvironmentFile and the offline restore via the
			// child env, both as JABALI_TARGET_PASSWORD.
			if targetPassword == "" {
				targetPassword = os.Getenv("JABALI_TARGET_PASSWORD")
			}
			// Consume + delete the secret env-file the agent wrote (#496).
			if f := os.Getenv("JABALI_TARGET_PASSWORD_FILE"); f != "" {
				_ = os.Remove(f)
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
			dnsZonesRepo := repository.NewDNSZoneRepository(sharedDB)
			dnsRecordsRepo := repository.NewDNSRecordRepository(sharedDB)
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
				// GH #327 (johnnyq): carry the HestiaCP Contact Name onto the
				// created user's name. Read it from the STAGED tarball, not the
				// extractDir — the offline restore path hasn't extracted yet at
				// this point (extraction happens later in the parse step).
				if job.SourceKind == models.MigrationSourceHestia {
					stagingDir := filepath.Join("/var/lib/jabali-migrations", jobID)
					if fn, ln := hestiacp.PeekUserNameFromStaging(stagingDir, job.SourceUser); fn != "" || ln != "" {
						cu.NameFirst, cu.NameLast = fn, ln
						fmt.Printf("  → carried Hestia contact name: %s %s\n", fn, ln)
					}
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
				// JAB-47: only carry the source's Linux shadow hash onto
				// /etc/shadow when the operator opts in. Default: the migrated
				// account uses the panel-set password (reset via panel) + SSH
				// keys, so a compromised/weak SOURCE SSH password is not silently
				// valid on the new host.
				preserveCreds := preserveSourceState || migrationPlanPreserve(job).Credentials
				if meta != nil && meta.PasswordHash != "" && sharedAgent != nil && preserveCreds {
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
				models.MigrationSourceHestia,
				models.MigrationSourcePlesk:
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
			case models.MigrationSourcePlesk:
				// Plesk pull synthesises a METADATA cpmove (cpmove-<slug>/
				// {cp,mysql,databases.txt,domains-paths.txt,mail-paths.txt,
				// dnszones}). DBs were streamed into mysql/ during pull;
				// docroots are rsynced source->dest at restore (below), NOT
				// staged — a real box carried a 6.9 GB DB. Parse the cpmove
				// for cp meta + the streamed mysql dumps, then take the
				// domain list from domains-paths.txt (no homedir to walk).
				pleskTar := filepath.Join("/var/lib/jabali-migrations", job.ID,
					fmt.Sprintf("user.%s.tar.gz", job.SourceUser))
				if cp, perr := cpanel.ParseTarball(pleskTar, extractDir); perr == nil {
					parsed = cp
					slug := plesk.Slug(job.SourceUser)
					manifest := filepath.Join(extractDir, "cpmove-"+slug, "domains-paths.txt")
					if raw, rerr := os.ReadFile(manifest); rerr == nil {
						if parsed.DocRoots == nil {
							parsed.DocRoots = map[string]string{}
						}
						for _, line := range strings.Split(string(raw), "\n") {
							line = strings.TrimSpace(line)
							if line == "" {
								continue
							}
							parts := strings.SplitN(line, "\t", 2)
							if len(parts) != 2 {
								continue
							}
							dom := parts[0]
							parsed.DomainNames = append(parsed.DomainNames, dom)
							parsed.DocRoots[dom] = filepath.Join("/home", *user.Username, "domains", dom, "public_html")
						}
					}
				} else {
					return failJob(fmt.Errorf("plesk cpmove parse %s: %w", pleskTar, perr))
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
				// GH #327: the pull saves the Hestia backup as
				// user.<user>.tar.gz (migrate_pull_cmd.go), same as the DA
				// branch above — the import MUST use the same name or
				// ParseHestiaTarball 404s, falls through to an empty tarball,
				// and the job completes 'done' having imported only the user
				// (no domains/DBs/mail). This was the 'created the user but
				// nothing imported' bug.
				// GH #327: the Hestia pull (pullHestia) saves the tar under
				// its REMOTE basename to preserve .tar vs .tar.gz — e.g.
				// itflowapp.2026-07-05_16-10-38.tar — NOT user.<user>.tar.gz.
				// The old hardcoded name mismatched, so ParseHestiaTarball
				// 404'd, the import fell through to an empty tarball, and the
				// job finished "done" having created only the user (no
				// domains/DBs/mail). Glob the job dir for whatever single tar
				// the pull actually saved.
				hDir := filepath.Join("/var/lib/jabali-migrations", job.ID)
				hMatches, _ := filepath.Glob(filepath.Join(hDir, "*.tar"))
				if gz, _ := filepath.Glob(filepath.Join(hDir, "*.tar.gz")); len(gz) > 0 {
					hMatches = append(hMatches, gz...)
				}
				hTarPath := filepath.Join(hDir, fmt.Sprintf("user.%s.tar.gz", job.SourceUser))
				if len(hMatches) > 0 {
					hTarPath = hMatches[0]
				}
				if h, herr := hestiacp.ParseHestiaTarball(hTarPath, extractDir); herr == nil {
					// GH #327 diag: make an empty import self-explaining.
					fmt.Fprintf(cmd.ErrOrStderr(),
						"hestia import: parsed %d web domain(s), %d db dump(s); tar top-level=%v\n",
						len(h.DomainDirs), len(h.MySQLDumps), h.TopLevel)
					parsed = &cpanel.ParsedTarball{
						ExtractDir: extractDir,
						SourceUser: job.SourceUser,
						HomeDir:    h.WebRoot,  // Hestia rsync target = web/<dom>/public_html/...
						MailRoot:   h.MailRoot, // Hestia stores at mail/<dom>/<local>/Maildir
						MySQLDumps: h.MySQLDumps,
					}
					if h.SSHKeys != "" {
						parsed.SSHAuthorized = []string{h.SSHKeys}
					}
					if h.CronFile != "" {
						parsed.CronFiles = []string{h.CronFile}
					}
					// JAB-28: feed Hestia BIND zones (dns/<dom>/conf/<dom>.db)
					// to cpanel.ImportDNS so source records migrate instead of
					// only the generic bootstrap zone.
					if len(h.ZoneFiles) > 0 {
						parsed.ZoneFiles = h.ZoneFiles
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
								"/home", *user.Username, "domains", name, "public_html")
						}
					}
				} else if _, statErr := os.Stat(hTarPath); statErr == nil {
					// GH #327: the tar is PRESENT but unparseable — do NOT
					// silently fall through to an empty import + mark the job
					// done. That is exactly the "created the user, imported
					// nothing" failure. Fail loudly so the operator sees it.
					return fmt.Errorf("hestia import: backup %s exists but ParseHestiaTarball failed: %w", hTarPath, herr)
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
						sshRepo, cronsRepo, dbsRepo, dbUsersRepo, dbGrantsRepo, domainsRepo, mbRepo, fwdRepo, arRepo, filtersRepo, phpPoolsRepo, dnsZonesRepo, dnsRecordsRepo, usersRepo, kc, preserveSourceState,
					),
				},
			}
			runner.WithContext(payload)
			runErr := runner.Run(ctx, job.ID)
			// JAB-31: the pipeline stamps 'done' on best-effort imports even when
			// core areas failed. If the restore recorded critical failures,
			// downgrade done→degraded and (unless --allow-degraded) exit non-zero
			// so operators don't trust an unusable account.
			if runErr == nil && len(payload.criticals) > 0 {
				msg := "critical failures: " + strings.Join(payload.criticals, "; ")
				if j, lerr := jobsRepo.FindByID(ctx, job.ID); lerr == nil && j != nil && shouldDowngradeToDegraded(j.State, payload.criticals) {
					if uErr := jobsRepo.UpdateState(ctx, job.ID, models.MigrationStateDegraded, &msg); uErr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "warning: mark degraded: %v\n", uErr)
					}
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "DEGRADED: restore completed but %s\n", msg)
				if !allowDegraded {
					runErr = fmt.Errorf("migration degraded — %s (re-run after fixing, or pass --allow-degraded to accept)", msg)
				}
			}
			// JAB-88: surface the restore stage summary (mailboxes, dbs, domains,
			// ssl, …) on the operator's terminal. The runner only folds these into
			// manifest_json (DB), so `jabali migrate restore` printed NOTHING about
			// mail — an operator couldn't see that a migrated mailbox got a fresh
			// password (silent lockout). Print them so the notice is actually read.
			if j, lerr := jobsRepo.FindByID(ctx, job.ID); lerr == nil && j != nil && j.ManifestJSON != nil {
				var stageMsgs []string
				if json.Unmarshal([]byte(*j.ManifestJSON), &stageMsgs) == nil && len(stageMsgs) > 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "  restore summary:")
					for _, msg := range stageMsgs {
						fmt.Fprintf(cmd.OutOrStdout(), "    - %s\n", msg)
					}
				}
			}

			// JAB-87: record where an offline restore landed (dest_user +
			// dest_domain) so the admin Migrations list/detail shows the
			// destination — only the SSH pull path set these before. Best-effort:
			// a successful restore must never fail because we couldn't stamp
			// observability columns. Gate on a terminal SUCCESS state (done or
			// degraded — the account was actually restored).
			if j, lerr := jobsRepo.FindByID(ctx, job.ID); lerr == nil && j != nil {
				switch j.State {
				case models.MigrationStateDone, models.MigrationStateDegraded:
					if payload.targetUsername != "" {
						if uerr := jobsRepo.UpdateDestUser(ctx, job.ID, payload.targetUsername); uerr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "warning: record dest_user: %v\n", uerr)
						}
					}
					destDomain := ""
					if meta != nil {
						destDomain = meta.PrimaryDomain // cpanel: authoritative DNS= value
					}
					if destDomain == "" && payload.parsed != nil && len(payload.parsed.DomainNames) > 0 {
						// Hestia/DirectAdmin tarballs carry no primary flag; the parser +
						// describe treat the FIRST scanned domain as primary
						// (hestiacp/describe.go). Follow the same convention.
						destDomain = payload.parsed.DomainNames[0]
					}
					if destDomain == "" {
						// Defensive: some jobs may carry a full AccountManifest here.
						destDomain = primaryDomainFromManifest(j.ManifestJSON)
					}
					if destDomain != "" {
						if derr := jobsRepo.UpdateDestDomain(ctx, job.ID, destDomain); derr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "warning: record dest_domain: %v\n", derr)
						}
					}
				}
			}

			// Staging-dir cleanup. Re-load the job so we see the
			// terminal state the runner just stamped (done / failed).
			// Operator can suppress via --keep-staging when debugging.
			// JAB-43: degraded is terminal too — clean it like done/failed so a
			// degraded restore doesn't silently leave GBs in /var/lib/jabali-
			// migrations (pass --keep-staging to retain for debugging).
			if !keepStaging {
				if j, lerr := jobsRepo.FindByID(ctx, job.ID); lerr == nil && j != nil {
					switch j.State {
					case models.MigrationStateDone, models.MigrationStateFailed,
						models.MigrationStateCancelled, models.MigrationStateDegraded:
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
	cmd.Flags().BoolVar(&allowDegraded, "allow-degraded", false, "exit 0 even if the restore ends degraded (a core area — DB/mail/health — failed)")
	cmd.Flags().BoolVar(&preserveSourceState, "preserve-source-state", false, "keep imported source state ACTIVE (mail forwarders/catchalls/filters/autoresponders, and — as later PRs land — credentials/SSL/SSH). Default OFF: imported artifacts land inert and the operator re-activates trusted ones in the panel (JAB-46). Only use for trusted same-owner migrations.")
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
	// criticals (JAB-31) collects core-area failures observed during the restore
	// stage (DB dumps all skipped, mail found-but-not-pushed, 0 healthy domains).
	// The run command reads it after the runner finishes and, when non-empty,
	// downgrades the job from done to degraded + exits non-zero.
	criticals []string
}

// Core-area failure thresholds (JAB-31). Each returns true when an area ran but
// produced nothing usable — a "done" job in that state is misleading.
func dbDumpsAllSkipped(dumps, created int) bool        { return dumps > 0 && created == 0 }
func mailFoundNonePushed(found int, pushed int64) bool { return found > 0 && pushed == 0 }

// shouldDowngradeToDegraded gates the done→degraded transition: the runner
// stamped done but the restore recorded at least one core-area failure.
func shouldDowngradeToDegraded(state string, criticals []string) bool {
	return state == models.MigrationStateDone && len(criticals) > 0
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
		// JAB-55: populate the manifest from the parsed backup so Validate can
		// actually catch domain/DB conflicts before any destructive restore stage
		// runs — it used to validate an empty manifest (zero projections, no
		// blockers). Domains come from ZoneFiles (cPanel BIND) or DomainNames
		// (DA/Hestia fallback); databases from the per-DB SQL dumps.
		if p != nil && p.parsed != nil {
			seenDom := map[string]bool{}
			addDom := func(name string) {
				name = strings.TrimSpace(name)
				if name == "" || seenDom[name] {
					return
				}
				seenDom[name] = true
				mf.Domains = append(mf.Domains, migrate.DomainSpec{Name: name, DocRoot: p.parsed.DocRoots[name]})
			}
			for _, zf := range p.parsed.ZoneFiles {
				addDom(strings.TrimSuffix(filepath.Base(zf), ".db"))
			}
			for _, name := range p.parsed.DomainNames {
				addDom(name)
			}
			seenDB := map[string]bool{}
			for _, dump := range p.parsed.MySQLDumps {
				name := strings.TrimSuffix(filepath.Base(dump), ".sql")
				if name == "" || seenDB[name] {
					continue
				}
				seenDB[name] = true
				mf.Databases = append(mf.Databases, migrate.DatabaseSpec{Engine: "mysql", Name: name})
			}
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
		// GH #646: a refresh job legitimately targets its own already-present
		// domain; accept-existing-domain is same-user-only inside Validate.
		acceptDomain := job.Mode == "refresh"
		rpt, err := migrate.Validate(ctx, migrate.ValidateDeps{
			Users: users, Domains: domains,
		}, mf, targetUsername, acceptUserID, acceptDomain)
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
	dnsZonesRepo repository.DNSZoneRepository,
	dnsRecordsRepo repository.DNSRecordRepository,
	usersRepo repository.UserRepository,
	kc *kratosclient.Client,
	preserveSourceState bool,
) migrate.StageCallback {
	return func(ctx context.Context, job *models.MigrationJob, payload any) (int64, []string, error) {
		var _ agent.AgentInterface = sharedAgent // compile-time guard
		p, ok := payload.(*cpanelRunPayload)
		if !ok {
			return 0, nil, fmt.Errorf("restore: bad payload type")
		}

		// JAB-46: what imported source state may come in ACTIVE. Read the
		// wizard's per-concern opt-in from PlanJSON (absent/malformed => preserve
		// nothing = secure); the --preserve-source-state CLI flag forces all-on
		// for trusted same-owner migrations.
		preserve := migrationPlanPreserve(job)
		if preserveSourceState {
			preserve = planPreserve{MailRouting: true, Credentials: true, SSL: true, SSH: true}
		}
		var warnings []string
		var bytes int64

		// The migration imports (home rsync, mailbox maildirs, big DB
		// loads) routinely run far longer than the shared agent client's
		// default per-call deadline (config default 30s / 120s fallback),
		// which killed every big account with
		// "agent.migration.import_home: agent: read: i/o timeout". Use a
		// dedicated long-deadline client for the whole restore stage —
		// same as account-restore (1h) and system-restore (4h) already do.
		restoreAgent := agent.NewClient(agent.Config{Timeout: 4 * time.Hour})

		plan, planOK := migrationPlanAreasChecked(job)
		if !planOK {
			// JAB-56: fail closed on a malformed plan instead of importing all.
			return bytes, warnings, fmt.Errorf("migration plan (PlanJSON) is malformed — refusing to import; fix or clear the plan")
		}

		// JAB-56: SSH keys are account/shell data — gate under the websites area
		// (the account-hosting toggle). Skipped areas are reported, not imported.
		var err error
		var sshRes *cpanel.SSHKeyImportResult
		if plan.Websites {
			sshRes, err = cpanel.ImportSSHKeys(ctx, sshRepo, p.parsed, p.targetUserID, preserve.SSH)
			if err != nil {
				return bytes, warnings, fmt.Errorf("ssh: %w", err)
			}
			warnings = append(warnings, fmt.Sprintf("ssh: created=%d", sshRes.Created))
			warnings = append(warnings, sshRes.Skipped...)
			// #327: when keys were found but none imported, say WHY prominently —
			// the per-key skip lines above are easy to miss. Two common reasons:
			// migrations reset SSH access by default (JAB-53), and keys weaker than
			// RSA-2048 are rejected.
			if sshRes.Created == 0 && len(sshRes.Skipped) > 0 && !preserve.SSH {
				warnings = append(warnings, "ssh: NO keys imported — SSH access is RESET on migration by default; re-add trusted keys in the SSH Keys page, or re-run with --preserve-source-state. (Keys weaker than RSA-2048 are always rejected as too weak — regenerate as ed25519 or RSA>=2048.)")
			} else if sshRes.Created == 0 && len(sshRes.Skipped) > 0 {
				warnings = append(warnings, "ssh: keys were found but none imported — see the ssh: lines above (a common cause is a key weaker than RSA-2048, which is rejected; regenerate as ed25519 or RSA>=2048).")
			}
		} else {
			warnings = append(warnings, "ssh: skipped (websites disabled in plan)")
		}

		// Non-nil so a disabled databases area still yields empty Credentials for
		// the app-config rewriter below (no nil deref).
		dbsRes := &cpanel.DBImportResult{}
		if plan.Databases {
			dbsRes, err = cpanel.ImportDatabases(ctx, dbsRepo, dbUsersRepo, dbGrantsRepo, restoreAgent, p.parsed, p.targetUserID, p.targetUsername, preserve.Credentials)
			if err != nil {
				return bytes, warnings, fmt.Errorf("databases: %w", err)
			}
			warnings = append(warnings, fmt.Sprintf("databases: created=%d", dbsRes.Created))
			warnings = append(warnings, dbsRes.Skipped...)
			// JAB-31: DB dumps present but none imported is a core failure, not a
			// warning — a "done" job here has no databases.
			if dbDumpsAllSkipped(len(p.parsed.MySQLDumps), dbsRes.Created) {
				p.criticals = append(p.criticals, fmt.Sprintf(
					"databases: %d dump(s) present but 0 imported", len(p.parsed.MySQLDumps)))
			}
		} else {
			warnings = append(warnings, "databases: skipped (databases disabled in plan)")
		}

		// JAB-56: gate all website content (home/docroot rsync + domain rows) on
		// the websites plan area. Skipped => no home copy, no domain creation.
		if plan.Websites {
			// JAB-28 follow-up: DNS import moved to AFTER ImportDomains (below) so the
			// bootstrap zone that domain.create renders can't clobber the imported
			// source records (QA saw dns: zones=2 records=28 in the manifest but only
			// bootstrap records in PowerDNS).

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
			case models.MigrationSourceDirectAdmin, models.MigrationSourcePlesk:
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
				// JAB-45: the remote source paths must live under the SOURCE ACCOUNT
				// home (/home/<SourceUser>/), not just /home/. A tampered manifest
				// pointing a docroot at /home/victim/... would otherwise copy another
				// source tenant's files (the SSH session is root/admin). Reject rows
				// outside the account root (clean path, no traversal) as a security
				// critical — the job degrades rather than silently copying.
				if job.SourceKind == models.MigrationSourcePlesk {
					// SECURITY (cross-tenant), airtight: Plesk has NO
					// per-subscription filesystem root (each domain is its
					// own /var/www/vhosts/<domain>/), so we allowlist ONLY
					// the docroots of the domains this subscription owns per
					// the SOURCE's psa DB — queried fresh here, never trusting
					// the staged manifest. FAIL CLOSED: if the source can't be
					// verified, refuse every home rsync rather than risk
					// copying another source tenant's files (SSH is root).
					allowed, aerr := pleskAuthorizedDocroots(ctx, job, sharedDB)
					if aerr != nil {
						p.criticals = append(p.criticals, fmt.Sprintf(
							"security: could not verify Plesk subscription domains from source psa (%v) — home rsync refused (fail-closed)", aerr))
						rsyncRows = nil
					} else {
						kept := rsyncRows[:0]
						for _, r := range rsyncRows {
							cl := filepath.Clean(r.SrcPath)
							ok := false
							for a := range allowed {
								if cl == a || strings.HasPrefix(cl, a+"/") {
									ok = true
									break
								}
							}
							if ok {
								kept = append(kept, r)
							} else {
								p.criticals = append(p.criticals, fmt.Sprintf(
									"security: rsync source %q for domain %q is not an authoritative docroot of subscription %q — refused",
									r.SrcPath, r.Dom, job.SourceUser))
							}
						}
						rsyncRows = kept
					}
				} else if p.parsed.SourceUser != "" {
					srcRoot := "/home/" + p.parsed.SourceUser
					kept := rsyncRows[:0]
					for _, r := range rsyncRows {
						cl := filepath.Clean(r.SrcPath)
						if cl == srcRoot || strings.HasPrefix(cl, srcRoot+"/") {
							kept = append(kept, r)
						} else {
							p.criticals = append(p.criticals, fmt.Sprintf(
								"security: rsync source %q for domain %q is outside the source account %s — refused",
								r.SrcPath, r.Dom, srcRoot))
						}
					}
					rsyncRows = kept
				}
			}
			if len(rsyncRows) > 0 {
				secretPath := fmt.Sprintf("/etc/jabali-panel/migration-secrets/%s.env", job.ID)
				// JAB-52: reuse the operator's pull-source SSH login instead of
				// hardcoding root. migrationSSHUser resolves it per source kind —
				// cPanel/DA use SourceUser (the SSH login), HestiaCP forces root
				// since its accounts have no SSH shell (GH #327). Legacy jobs with
				// no source_user fall back to root with a warning.
				remoteSSHUser := migrationSSHUser(job)
				if job.SourceUser == "" {
					warnings = append(warnings, "home_rsync_remote: job has no source_user — defaulting to root")
				}
				totalBytes := int64(0)
				domCount := 0
				for _, r := range rsyncRows {
					destPath := filepath.Join("/home", p.targetUsername, "domains", r.Dom, "public_html")
					rawResp, rerr := restoreAgent.Call(ctx, "migration.rsync_remote_home", map[string]any{
						"port":        srcSSHPort(job),
						"job_id":      job.ID,
						"host":        job.SourceHost,
						"ssh_user":    remoteSSHUser,
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
			if !daHomeHandled && job.SourceKind == models.MigrationSourceHestia {
				// JAB-26: Hestia web content must land in the configured docroot
				// /home/<user>/web/<dom>/public_html, not /home/<user>/<dom> via the
				// legacy full-homedir rsync. Per-domain copy into DocRoots.
				hhRes, err := cpanel.ImportHomeHestia(ctx, restoreAgent, p.parsed, job.ID, p.targetUsername)
				if err != nil {
					return bytes, warnings, fmt.Errorf("home_hestia: %w", err)
				}
				bytes += hhRes.BytesCopied
				warnings = append(warnings, fmt.Sprintf("home: bytes=%d files=%d domains=%d (hestia per-domain docroot)", hhRes.BytesCopied, hhRes.Files, hhRes.DomainsCopied))
				warnings = append(warnings, hhRes.Skipped...)
			} else if !daHomeHandled {
				hsRes, err := cpanel.ImportHomeSplit(ctx, restoreAgent, p.parsed, job.ID, p.targetUsername)
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
					homeRes, err := cpanel.ImportHome(ctx, restoreAgent, p.parsed, job.ID, p.targetUsername)
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

			domainsRes, err := cpanel.ImportDomains(ctx, domainsRepo, restoreAgent, p.parsed, p.targetUserID, p.targetUsername)
			if err != nil {
				return bytes, warnings, fmt.Errorf("domains: %w", err)
			}
			warnings = append(warnings, fmt.Sprintf("domains: created=%d email_enabled=%d", domainsRes.Created, domainsRes.EmailEnabled))
			warnings = append(warnings, domainsRes.Skipped...)
		} else {
			warnings = append(warnings, "websites: skipped (websites disabled in plan) — no home/docroot or domain import")
		}

		// JAB-28 follow-up: import source DNS records AFTER domain create so they
		// overlay the bootstrapped zone instead of being clobbered by it. Apex
		// NS + apex A/AAAA are filtered in ImportDNS (jabali owns the apex).
		if plan.DNS {
			// #327: pass this server's public IPv4 so ImportDNS can repoint any
			// record that pointed at the source box (mail A, custom subdomains).
			var serverIPv4 string
			if s, serr := repository.NewServerSettingsRepository(sharedDB).Get(ctx); serr == nil && s != nil {
				serverIPv4 = s.PublicIPv4
			}
			dnsRes, derr := cpanel.ImportDNS(ctx, dnsZonesRepo, dnsRecordsRepo, domainsRepo, p.parsed, serverIPv4)
			if derr != nil {
				return bytes, warnings, fmt.Errorf("dns: %w", derr)
			}
			warnings = append(warnings, fmt.Sprintf("dns: zones=%d records=%d", dnsRes.Zones, dnsRes.Records))
			warnings = append(warnings, dnsRes.Skipped...)
		} else {
			warnings = append(warnings, "dns: skipped per migration plan")
		}

		// Cron import runs HERE — after ImportHomeSplit rsynced the
		// docroots + ImportDomains created the domains — so the curl→php
		// self-target rewrite can resolve URL paths to real .php files
		// that exist on the dest disk (rewrite rule 5 "must exist").
		if plan.Cron {
			cronRes, err := cpanel.ImportCron(ctx, cronRepo, p.parsed, p.targetUserID, p.targetUsername)
			if err != nil {
				return bytes, warnings, fmt.Errorf("cron: %w", err)
			}
			warnings = append(warnings, fmt.Sprintf("cron: created=%d", cronRes.Created))
			warnings = append(warnings, cronRes.Skipped...)
		} else {
			warnings = append(warnings, "cron: skipped per migration plan")
		}

		if plan.Mailboxes {
			mailRes, err := cpanel.ImportMailboxes(ctx, p.parsed, restoreAgent, job.ID, mbRepo, domainsRepo, preserve.Credentials)
			if err != nil {
				return bytes, warnings, fmt.Errorf("mailboxes: %w", err)
			}
			warnings = append(warnings, fmt.Sprintf(
				"mailboxes: count=%d messages_found=%d messages_pushed=%d bytes_pushed=%d",
				mailRes.MailboxesCreated, mailRes.MessagesFound, mailRes.MessagesPushed, mailRes.BytesPushed))
			warnings = append(warnings, mailRes.Skipped...)
			// JAB-31: messages found but none pushed means the mailbox import hit
			// an error (e.g. a JMAP failure) — core failure, not a warning.
			if mailFoundNonePushed(mailRes.MessagesFound, mailRes.MessagesPushed) {
				p.criticals = append(p.criticals, fmt.Sprintf(
					"mailboxes: %d message(s) found but 0 pushed", mailRes.MessagesFound))
			}
		} else {
			warnings = append(warnings, "mailboxes: skipped per migration plan")
		}

		// M35.8 P3: per-domain custom SSL certs from apache_tls/.
		if plan.SSL {
			sslRes, err := cpanel.ImportSSL(ctx, restoreAgent, p.parsed, preserve.SSL)
			if err != nil {
				return bytes, warnings, fmt.Errorf("ssl: %w", err)
			}
			warnings = append(warnings, fmt.Sprintf("ssl: installed=%d", sslRes.Installed))
			warnings = append(warnings, sslRes.Skipped...)
		} else {
			warnings = append(warnings, "ssl: skipped per migration plan")
		}

		// M35.8 P2+P5: catch-all + subdomains + forwarders restore.
		// M35.8 P8: rewrite WP/Drupal/Joomla/Magento config files
		// with the new (db_name, db_user, db_pass) triples just
		// created. ImportDatabases captured them in dbsRes.Credentials;
		// the rewriter reads each per-domain docroot under
		// /home/<u>/domains/<dom>/public_html and splices values in.
		// Best-effort: missing app config = silent skip.
		// JAB-32: pass the resolved per-domain docroots so the scanner finds
		// Hestia's web/<dom>/public_html (and any non-default cPanel layout),
		// not just the hardcoded /home/<user>/domains tree.
		appDocroots := make([]string, 0, len(p.parsed.DocRoots))
		for _, dr := range p.parsed.DocRoots {
			appDocroots = append(appDocroots, dr)
		}
		if plan.Websites {
			appRes, aerr := cpanel.ImportAppConfigs(ctx, restoreAgent, p.targetUserID, p.targetUsername, dbsRes.Credentials, appDocroots)
			if aerr != nil {
				warnings = append(warnings, fmt.Sprintf("appconfigs: %v", aerr))
			} else {
				warnings = append(warnings, fmt.Sprintf(
					"appconfigs: wordpress=%d joomla=%d drupal=%d magento=%d caches_cleared=%d php_ini_fixed=%d",
					appRes.WordPress, appRes.Joomla, appRes.Drupal, appRes.Magento, appRes.CachesCleared, appRes.PhpIniFixed))
				warnings = append(warnings, appRes.Skipped...)
			}
		} else {
			warnings = append(warnings, "appconfigs: skipped (websites disabled in plan)")
		}

		extrasRes, err := cpanel.ImportExtras(ctx, domainsRepo, mbRepo, fwdRepo, arRepo, filtersRepo, phpPoolsRepo, restoreAgent, p.parsed, p.targetUserID, p.targetUsername, preserve.MailRouting)
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
			fwdRes, ferr := directadmin.ImportForwarders(ctx, fwdRepo, domainsRepo, p.parsed.ExtractDir, p.parsed.SourceUser, preserve.MailRouting)
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

		// Post-migration WP/site health probe. The vhosts are live
		// (ImportDomains rendered them synchronously); the per-user FPM
		// pool converges just after, so the agent retries 502/503. A
		// migrated app that crashes on jabali (e.g. WP fatal on malformed
		// option data that survived on the source via an object cache)
		// shows up here as a 500 — caught at migration time, in the
		// report, instead of from the customer. Best-effort: never fails
		// the migration.
		if len(p.parsed.DomainNames) > 0 {
			probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Minute)
			if raw, perr := restoreAgent.Call(probeCtx, "migration.http_probe", map[string]any{
				"domains": p.parsed.DomainNames,
			}); perr != nil {
				warnings = append(warnings, fmt.Sprintf("health_probe: %v", perr))
			} else {
				var pr struct {
					Results []struct {
						Domain string `json:"domain"`
						Status int    `json:"status"`
						OK     bool   `json:"ok"`
						Note   string `json:"note"`
					} `json:"results"`
				}
				_ = json.Unmarshal(raw, &pr)
				okCount := 0
				serverErrCount := 0
				for _, r := range pr.Results {
					if r.Status >= 500 {
						serverErrCount++
					}
					if r.OK {
						okCount++
						continue
					}
					warnings = append(warnings, fmt.Sprintf(
						"health_probe: ⚠ %s returned %d%s",
						r.Domain, r.Status,
						func() string {
							if r.Note != "" {
								return " — " + r.Note
							}
							return ""
						}()))
				}
				warnings = append(warnings, fmt.Sprintf("health_probe: %d/%d domains healthy", okCount, len(pr.Results)))
				// JAB-31: 0 healthy domains after a restore that probed some means
				// the account isn't serving — core failure, not a warning.
				// JAB-40: ANY domain not serving after a restore is a core problem
				// — 1/2 must not read as a clean `done`. The probe already retries
				// JAB-42: ONLY a 5xx is a hard degrade — the app is serving but
				// crashing (e.g. Nextcloud missing a PHP driver), which is JAB-40's
				// real case. A `0`/connection-refused is almost always the reconciler
				// not having rebound nginx for the just-created domain yet (the site
				// answers 200 seconds later), so it stays a warning, NOT a degrade —
				// otherwise a perfectly healthy restore false-degrades on probe
				// timing. okCount<total without any 5xx = still `done`.
				if serverErrCount > 0 {
					p.criticals = append(p.criticals, fmt.Sprintf(
						"health: %d/%d domains returned 5xx (crashing app) after restore",
						serverErrCount, len(pr.Results)))
				}
			}
			probeCancel()
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
// migrationSSHUser is the source SSH login for all remote stages (analyze, DA
// pivot, pull, home rsync) — the operator-supplied job.SourceUser (JAB-58/52),
// falling back to root for legacy jobs that never persisted one.
func migrationSSHUser(job *models.MigrationJob) string {
	if job == nil {
		return "root"
	}
	// HestiaCP has no per-account SSH shell, and its provisioning commands
	// (v-list-web-domains, v-backup-user, …) require a root login. Here
	// SourceUser is the account being migrated (e.g. "itflowapp"), NOT an SSH
	// login — dialing as it fails auth with "[none password]" (GH #327). The
	// discovery/pull paths already force root; the import's remote analyze
	// stage went through this helper and inherited the account, so the online
	// Hestia flow broke at `stage analyze: connect:` even though discovery
	// (which runs as root) succeeded.
	//
	// cPanel accounts DO have SSH shells (SourceUser == login), and DirectAdmin
	// pivots SourceUser to its admin login upstream, so only Hestia needs the
	// override.
	if job.SourceKind == models.MigrationSourceHestia {
		return "root"
	}
	if job.SourceUser != "" {
		return job.SourceUser
	}
	return "root"
}

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
		s, err := disc.Connect(ctx, job.SourceHost, migrationSSHUser(job), migrate.SecretRef{Path: secretPath})
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
	s, err := disc.Connect(subctx, job.SourceHost, migrationSSHUser(job), migrate.SecretRef{Path: secretPath})
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

// planAreas is the per-area import selection (GH #665). Zero value = nothing;
// but migrationPlanAreas defaults to all-true when no plan is set, preserving
// the import-everything behavior.
type planAreas struct {
	Websites  bool `json:"websites"`
	Databases bool `json:"databases"`
	Mailboxes bool `json:"mailboxes"`
	DNS       bool `json:"dns"`
	SSL       bool `json:"ssl"`
	Cron      bool `json:"cron"`
}

// migrationPlanAreas reads job.PlanJSON; when absent, every area is included.
// A MALFORMED plan is NOT silently treated as import-all (JAB-56): it fails
// closed via ok=false so the caller aborts instead of copying everything the
// operator may have intended to exclude.
func migrationPlanAreas(job *models.MigrationJob) planAreas {
	pa, _ := migrationPlanAreasChecked(job)
	return pa
}

func migrationPlanAreasChecked(job *models.MigrationJob) (planAreas, bool) {
	all := planAreas{true, true, true, true, true, true}
	if job == nil || job.PlanJSON == nil || *job.PlanJSON == "" {
		return all, true // no plan supplied -> import everything (intended default)
	}
	var wrap struct {
		Areas planAreas `json:"areas"`
	}
	if err := json.Unmarshal([]byte(*job.PlanJSON), &wrap); err != nil {
		return planAreas{}, false // malformed -> fail closed
	}
	return wrap.Areas, true
}

// planPreserve is the per-concern opt-in to keep imported source state ACTIVE
// (JAB-46 and the wider migration-security cluster). Zero value = preserve
// nothing = the secure default: imported forwarders/catchalls/filters/
// autoresponders (and, as later PRs land, credentials/SSL/SSH) arrive INERT and
// the operator re-activates the trusted ones in the panel's existing Mail / SSL
// / SSH pages. Stored as an object even though the CLI exposes a single
// --preserve-source-state flag, so granular per-concern opt-in stays a purely
// additive change with no re-migration.
type planPreserve struct {
	MailRouting bool `json:"mail_routing"`
	Credentials bool `json:"credentials"`
	SSL         bool `json:"ssl"`
	SSH         bool `json:"ssh"`
}

// migrationPlanPreserve reads job.PlanJSON's "preserve" object. Absent or
// malformed => preserve nothing (fail closed = secure). Unlike planAreas
// (which defaults to import-everything), the safe default here is the ZERO
// value, so a missing/old plan never silently re-activates source mail routing.
func migrationPlanPreserve(job *models.MigrationJob) planPreserve {
	if job == nil || job.PlanJSON == nil || *job.PlanJSON == "" {
		return planPreserve{}
	}
	var wrap struct {
		Preserve planPreserve `json:"preserve"`
	}
	if err := json.Unmarshal([]byte(*job.PlanJSON), &wrap); err != nil {
		return planPreserve{}
	}
	return wrap.Preserve
}

// primaryDomainFromManifest extracts the primary domain (is_primary, else the
// first) from a migration_jobs.manifest_json blob. Empty on nil/parse error/no
// domains. Source-agnostic — works for cpanel + hestia manifests (JAB-87).
func primaryDomainFromManifest(manifestJSON *string) string {
	if manifestJSON == nil || *manifestJSON == "" {
		return ""
	}
	var mf struct {
		Domains []struct {
			Name      string `json:"name"`
			IsPrimary bool   `json:"is_primary"`
		} `json:"domains"`
	}
	if err := json.Unmarshal([]byte(*manifestJSON), &mf); err != nil {
		return ""
	}
	for _, d := range mf.Domains {
		if d.IsPrimary && d.Name != "" {
			return d.Name
		}
	}
	if len(mf.Domains) > 0 {
		return mf.Domains[0].Name
	}
	return ""
}

// pleskAuthorizedDocroots connects to the Plesk source and returns the SET
// of docroots the subscription legitimately owns, per the source's psa DB
// (authoritative — not the staged manifest). The import rsync guard rsyncs
// ONLY paths under these. Returns an error (→ fail-closed refuse-all) when
// the source can't be reached/verified or reports zero domains.
func pleskAuthorizedDocroots(ctx context.Context, job *models.MigrationJob, db *gorm.DB) (map[string]bool, error) {
	allowPrivate := false
	if s, err := repository.NewServerSettingsRepository(db).Get(ctx); err == nil && s != nil {
		allowPrivate = s.MigrationAllowPrivateHosts
	}
	d := plesk.New()
	d.AllowPrivate = allowPrivate
	d.Port = srcSSHPort(job)
	secret := migrate.SecretRef{Path: fmt.Sprintf("/etc/jabali-panel/migration-secrets/%s.env", job.ID)}
	sess, err := d.Connect(ctx, job.SourceHost, migrationSSHUser(job), secret)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = d.Close(ctx, sess) }()
	doms, err := d.AuthoritativeDomains(ctx, sess, job.SourceUser)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, dom := range doms {
		if isHostnameLike(dom) { // defence-in-depth on psa output
			out["/var/www/vhosts/"+dom+"/httpdocs"] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source psa reported no domains for subscription %q", job.SourceUser)
	}
	return out, nil
}

// isHostnameLike accepts a DNS-hostname-shaped string (lowercase letters,
// digits, dot, hyphen; at least one dot; no path separators or traversal).
// Used by the Plesk rsync guard to bound a source docroot to its domain.
func isHostnameLike(h string) bool {
	if h == "" || len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-':
		default:
			return false
		}
	}
	return !strings.Contains(h, "..")
}
