package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// cpmoveUserRe extracts <user> from a cPanel cpmove / WHM pkgacct
// tarball filename. cPanel emits `cpmove-<user>.tar.gz`; pkgacct emits
// `cpmove-<user>.tar.gz` or `backup-<...>_<user>.tar.gz`. We only
// auto-derive from the canonical cpmove- form; anything else → operator
// passes --source-user explicitly.
var cpmoveUserRe = regexp.MustCompile(`^cpmove-(.+)\.tar\.gz$`)

// cpmoveSourceUser returns the cPanel account from a cpmove path, or ""
// if the basename isn't the canonical cpmove-<user>.tar.gz shape.
func cpmoveSourceUser(path string) string {
	m := cpmoveUserRe.FindStringSubmatch(filepath.Base(path))
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// hestiaUserRe extracts <user> from a HestiaCP v-backup-user filename
// `<user>.<YYYY-MM-DD_HH-MM-SS>.tar[.gz]` (e.g. itflowapp.2026-07-05_16-10-38.tar).
// A renamed archive without the timestamp component (hestia-sample.tar) does not
// match, so the CLI requires an explicit --source-user for it (JAB-25).
var hestiaUserRe = regexp.MustCompile(`^(.+)\.\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}\.tar(?:\.gz)?$`)

func hestiaSourceUser(path string) string {
	m := hestiaUserRe.FindStringSubmatch(filepath.Base(path))
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// stageTarball places src at dst (the path `jabali migrate import`
// expects: /var/lib/jabali-migrations/<job>/cpmove-<user>.tar.gz).
// Hardlink first — instant + zero extra disk when src and the staging
// dir share a filesystem (the common case: both on / ). Falls back to
// a streaming copy across devices. Never double-buffers.
func stageTarball(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil // already staged (idempotent re-run)
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// newMigrateRestoreCmd is the one-shot offline restore: create the
// migration_jobs row, stage the cpmove tarball where the importer
// expects it, then run the existing four-stage import pipeline — no
// UI, no manual /var/lib steps, no separate job-id.
func newMigrateRestoreCmd() *cobra.Command {
	var cpanel bool
	var hestia bool
	var file, restoreFile, sourceUser, sourceHost string
	var targetUser, targetEmail, targetPassword, targetPackageID string
	var keepStaging bool
	var preserveSourceState bool
	var retryFromScratch bool

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "One-shot offline restore from a cpmove tarball (create job + stage + import)",
		Long: `Restore a cPanel cpmove / WHM pkgacct or HestiaCP v-backup-user
tarball you already have on this server, in one command:

  jabali migrate restore --cpanel   --file /path/cpmove-<user>.tar.gz
  jabali migrate restore --hestiacp --file /path/<user>.<ts>.tar --source-user <user>

It creates the migration_jobs row, copies the tarball to the path the
importer expects, and runs the full analyze → fix_perms → validate →
restore pipeline (home, MySQL, mail, DNS/domains).

Source account is read from the filename — cpmove-<user>.tar.gz for
cPanel, <user>.<YYYY-MM-DD_HH-MM-SS>.tar[.gz] for HestiaCP. When the
filename has no recognizable user (e.g. a renamed hestia-sample.tar),
pass --source-user explicitly. Destination jabali user defaults to the
source account; pass --target-email + --target-password to auto-create
it (else pre-create via the admin UI / jabali user CLI). Re-run the
same command to resume a failed job.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			// JAB-25: offline restore supports HestiaCP as a first-class source
			// kind alongside cPanel. Exactly one source kind must be selected.
			if cpanel == hestia {
				return errors.New("exactly one source kind is required: pass --cpanel OR --hestiacp")
			}
			kind := models.MigrationSourceCpanel
			if hestia {
				kind = models.MigrationSourceHestia
			}

			if file == "" {
				file = restoreFile
			}
			if file == "" {
				return errors.New("--file is required (path to the cpmove tarball)")
			}
			abs, err := filepath.Abs(file)
			if err != nil {
				return err
			}
			fi, err := os.Stat(abs)
			if err != nil {
				return fmt.Errorf("--file: %w", err)
			}
			if fi.IsDir() {
				return fmt.Errorf("--file %q is a directory", abs)
			}
			// The disk preflight used to run here, before the job row existed.
			// It now runs just below, after find-or-create and before staging —
			// see the JAB-219 note there for why. Ensure the parent staging dir
			// exists either way so the check has something to statfs.
			_ = os.MkdirAll("/var/lib/jabali-migrations", 0o750)

			su := sourceUser
			if su == "" {
				if hestia {
					su = hestiaSourceUser(abs)
				} else {
					su = cpmoveSourceUser(abs)
				}
			}
			if su == "" {
				hint := "expected cpmove-<user>.tar.gz"
				if hestia {
					hint = "expected <user>.<YYYY-MM-DD_HH-MM-SS>.tar[.gz]"
				}
				return fmt.Errorf("cannot derive source user from %q (%s) — pass --source-user",
					filepath.Base(abs), hint)
			}

			jobsRepo := repository.NewMigrationJobRepository(sharedDB)
			var jobID string
			if ex, _ := jobsRepo.FindBySource(ctx, kind, sourceHost, su); ex != nil {
				jobID = ex.ID
				if retryFromScratch {
					// Full retry: wipe stage rows + reset the job to pending so
					// the runner re-seeds and re-runs analyze → fix_perms →
					// validate → restore from clean state (re-creating the target
					// user if it was deleted, replacing stale manifest/status).
					// The default (no flag) stays a gentle resume that keeps
					// already-done stages.
					fmt.Printf("  → FULL RETRY (--retry-from-scratch): resetting job %s (was state=%s) — re-running the entire pipeline, NOT a resume\n",
						ex.ID, ex.State)
					if err := jobsRepo.DeleteStagesForJob(ctx, jobID); err != nil {
						return fmt.Errorf("retry-from-scratch: delete stages: %w", err)
					}
					if err := jobsRepo.UpdateManifest(ctx, jobID, "{}"); err != nil {
						return fmt.Errorf("retry-from-scratch: clear manifest: %w", err)
					}
					if err := jobsRepo.UpdateState(ctx, jobID, models.MigrationStatePending, nil); err != nil {
						return fmt.Errorf("retry-from-scratch: reset job state: %w", err)
					}
				} else {
					fmt.Printf("  → reusing existing job %s (state=%s) for %s/%s (resume — pass --retry-from-scratch for a full re-run)\n",
						ex.ID, ex.State, kind, su)
				}
			} else {
				row := &models.MigrationJob{
					ID:         ids.NewULID(),
					SourceKind: kind,
					SourceHost: sourceHost,
					SourceUser: su,
					State:      models.MigrationStatePending,
				}
				if err := jobsRepo.Create(ctx, row); err != nil {
					return fmt.Errorf("create migration job: %w", err)
				}
				jobID = row.ID
				fmt.Printf("  → created migration job %s (%s/%s)\n", jobID, kind, su)
			}

			// Disk preflight. Position is deliberate and satisfies two issues that
			// pull in opposite directions:
			//
			//   JAB-41 wanted the refusal BEFORE anything is provisioned, so a
			//   low-disk host does not half-extract and strand a tarball or a
			//   half-created target user. Staging begins on the next line, so that
			//   still holds — nothing has been written yet.
			//
			//   JAB-219 wanted the refusal to leave something RESUMABLE. Running
			//   before the job row existed meant migration_jobs had no row at all,
			//   so the operator could not free space and continue; they had to
			//   restart the whole command and hope. The job row is metadata, not
			//   provisioning, so creating it first costs nothing and makes the
			//   refusal recoverable.
			//
			// The job is marked failed with the measured numbers, so `migrate list`
			// shows why rather than leaving a pending job with no explanation.
			// JAB-216: memory, same position and same contract as the disk check
			// below — after the job row exists so a refusal is resumable, before
			// anything is staged so nothing is half-provisioned.
			if mwarn, merr := migrate.CheckRestoreMemory(); merr != nil {
				msg := merr.Error()
				if uerr := jobsRepo.UpdateState(ctx, jobID, models.MigrationStateFailed, &msg); uerr != nil {
					fmt.Printf("  ! could not record the failure on job %s: %v\n", jobID, uerr)
				}
				return fmt.Errorf("%w\n  job %s left resumable — free memory, then: jabali migrate import --job-id %s",
					merr, jobID, jobID)
			} else if mwarn != "" {
				fmt.Printf("  ! %s\n", mwarn)
			}

			if derr := migrate.CheckExtractDiskSpace(abs, "/var/lib/jabali-migrations"); derr != nil {
				msg := derr.Error()
				if uerr := jobsRepo.UpdateState(ctx, jobID, models.MigrationStateFailed, &msg); uerr != nil {
					fmt.Printf("  ! could not record the failure on job %s: %v\n", jobID, uerr)
				}
				return fmt.Errorf("%w\n  job %s left resumable — free space, then: jabali migrate import --job-id %s",
					derr, jobID, jobID)
			}

			staging := filepath.Join("/var/lib/jabali-migrations", jobID)
			if err := os.MkdirAll(staging, 0o750); err != nil {
				return fmt.Errorf("mkdir staging dir: %w", err)
			}
			dst := filepath.Join(staging, fmt.Sprintf("cpmove-%s.tar.gz", su))
			if hestia {
				// JAB-25: the Hestia importer looks for user.<user>.tar.gz (or the
				// first *.tar) in the staging dir. ParseHestiaTarball auto-detects
				// gzip by magic bytes, so this canonical name works for both plain
				// and gzipped Hestia archives regardless of the source extension.
				dst = filepath.Join(staging, fmt.Sprintf("user.%s.tar.gz", su))
			}
			if err := stageTarball(abs, dst); err != nil {
				return fmt.Errorf("stage tarball: %w", err)
			}
			fmt.Printf("  → staged %s\n", dst)

			// Reuse the entire import pipeline verbatim — zero
			// duplication. newMigrateImportCmd is a standalone cobra
			// command; SetArgs + Execute runs its PreRunE
			// (requireDBAndAgent, idempotent) + RunE.
			imp := newMigrateImportCmd()
			impArgs := []string{"--job-id", jobID}
			if targetUser != "" {
				impArgs = append(impArgs, "--target-user", targetUser)
			}
			if targetEmail != "" {
				impArgs = append(impArgs, "--target-email", targetEmail)
			}
			if targetPassword != "" {
				impArgs = append(impArgs, "--target-password", targetPassword)
			}
			if targetPackageID != "" {
				impArgs = append(impArgs, "--target-package-id", targetPackageID)
			}
			if keepStaging {
				impArgs = append(impArgs, "--keep-staging")
			}
			if preserveSourceState {
				impArgs = append(impArgs, "--preserve-source-state")
			}
			imp.SetArgs(impArgs)
			imp.SetContext(ctx)
			fmt.Printf("  → running import pipeline (job %s)\n", jobID)
			return imp.Execute()
		},
	}

	cmd.Flags().BoolVar(&cpanel, "cpanel", false, "source is a cPanel cpmove / WHM pkgacct tarball")
	cmd.Flags().BoolVar(&hestia, "hestiacp", false, "source is a HestiaCP v-backup-user tarball (<user>.<YYYY-MM-DD_HH-MM-SS>.tar[.gz])")
	cmd.Flags().StringVar(&file, "file", "", "path to the cpmove tarball (cpmove-<user>.tar.gz) — required")
	cmd.Flags().StringVar(&restoreFile, "restore-file", "", "alias of --file")
	cmd.Flags().StringVar(&sourceUser, "source-user", "", "cPanel account (default: derived from the cpmove filename)")
	cmd.Flags().StringVar(&sourceHost, "source-host", "", "informational source host (offline restore leaves this empty)")
	cmd.Flags().StringVar(&targetUser, "target-user", "", "destination jabali username (default: the source account)")
	cmd.Flags().StringVar(&targetEmail, "target-email", "", "destination email (only used when auto-creating the user)")
	cmd.Flags().StringVar(&targetPassword, "target-password", "", "destination password (auto-create only; ≥10 chars)")
	cmd.Flags().StringVar(&targetPackageID, "target-package-id", "", "hosting package ULID (auto-create only)")
	cmd.Flags().BoolVar(&keepStaging, "keep-staging", false, "keep /var/lib/jabali-migrations/<job-id>/ after the run (debug)")
	cmd.Flags().BoolVar(&retryFromScratch, "retry-from-scratch", false, "reuse the source + options but wipe the existing job's stages and re-run the whole pipeline from analyze (recreates the target user, replaces stale manifest); default is a gentle resume")
	cmd.Flags().BoolVar(&retryFromScratch, "fresh", false, "alias of --retry-from-scratch")
	cmd.Flags().BoolVar(&preserveSourceState, "preserve-source-state", false, "keep imported source state ACTIVE + carry source credentials where safe: preserves the mailbox password (Stalwart-verifiable bcrypt only), keeps mail forwarders/catchalls/filters/autoresponders active, restores DB user creds + source SSL. Default OFF (secure): mail gets a fresh password (tenant must reset), routing artifacts land inert. Parity with `jabali migrate import`. Only use for a trusted same-owner migration.")
	return cmd
}
