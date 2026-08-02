// `jabali backup account-restore` — operator-facing CLI to restore one
// account's snapshot directly via the agent. Bypasses the panel-api
// HTTP path so it works even when the panel UI is offline. Mirrors
// the parameter shape that POST /admin/backups/restore now sends.
//
// Two modes:
//
//   - Scriptable: every required flag set on the command line.
//   - Interactive: --interactive, OR no flags on a TTY. Prompts the
//     operator through destination → snapshot → user (existing or
//     disaster-recovery) → confirmation. Same dispatch as scriptable.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupmetadata"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// applyPanelMetadata parses the metadata.json bytes from the agent
// reply and runs backupmetadata.Apply with the full set of repos.
// Prints a summary table so the operator sees what was reinstated.
func applyPanelMetadata(ctx context.Context, cmd *cobra.Command, raw json.RawMessage) {
	var meta internalbackup.AccountMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "WARNING: failed to parse metadata bundle: %v\n", err)
		return
	}
	deps := backupmetadata.Deps{
		Users:          repository.NewUserRepository(sharedDB),
		Domains:        repository.NewDomainRepository(sharedDB),
		Databases:      repository.NewDatabaseRepository(sharedDB),
		DatabaseUsers:  repository.NewDatabaseUserRepository(sharedDB),
		DatabaseGrants: repository.NewDatabaseUserGrantRepository(sharedDB),
		AppInstalls:    repository.NewApplicationInstallRepository(sharedDB),
		SSLCerts:       repository.NewSSLCertificateRepository(sharedDB),
		PHPPools:       repository.NewPHPPoolRepository(sharedDB),
		PHPPoolIni:     repository.NewPHPPoolIniOverrideRepository(sharedDB),
		SSHKeys:        repository.NewSSHKeyRepository(sharedDB),
		CronJobs:       repository.NewCronJobRepository(sharedDB),
		Mailboxes:      repository.NewMailboxRepository(sharedDB),
		Forwarders:     repository.NewEmailForwarderRepository(sharedDB),
		Autoresponders: repository.NewEmailAutoresponderRepository(sharedDB),
		MailboxShares:  repository.NewMailboxShareRepository(sharedDB),
		DNSZones:       repository.NewDNSZoneRepository(sharedDB),
		DNSRecords:     repository.NewDNSRecordRepository(sharedDB),
		KratosClient:   kratosclient.NewClient(sharedCfg.Auth.Kratos.PublicURL, sharedCfg.Auth.Kratos.AdminURL),
	}
	r := backupmetadata.Apply(ctx, &meta, deps)
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "✓ panel state reconstructed from metadata bundle:")
	fmt.Fprintf(w, "  user:           %s\n", boolMark(r.UserCreated))
	fmt.Fprintf(w, "  php_pools:      %d (ini overrides: %d)\n", r.PHPPools, r.PHPPoolIni)
	fmt.Fprintf(w, "  domains:        %d (ssl_certs: %d)\n", r.Domains, r.SSLCerts)
	fmt.Fprintf(w, "  mailboxes:      %d (forwarders: %d)\n", r.Mailboxes, r.Forwarders)
	fmt.Fprintf(w, "  databases:      %d (users: %d, grants: %d)\n", r.Databases, r.DatabaseUsers, r.DatabaseGrants)
	fmt.Fprintf(w, "  app_installs:   %d\n", r.AppInstalls)
	fmt.Fprintf(w, "  ssh_keys:       %d\n", r.SSHKeys)
	fmt.Fprintf(w, "  cron_jobs:      %d\n", r.CronJobs)
	fmt.Fprintf(w, "  skipped:        %d (already present)\n", r.Skipped)
	for _, e := range r.Errors {
		fmt.Fprintf(w, "  ERR: %s\n", e)
	}

	// Provision systemd timers for restored cron jobs
	if r.CronJobs > 0 && meta.User.ID != "" {
		if username := getUsernameFromMeta(&meta); username != "" {
			if timerErr := provisionRestoredCronTimers(ctx, meta.User.ID, username, deps); timerErr != nil {
				fmt.Fprintf(w, "  WARNING: cron timer provisioning failed: %v\n", timerErr)
			} else {
				fmt.Fprintf(w, "✓ cron timers provisioned for %d jobs\n", r.CronJobs)
			}
		} else {
			fmt.Fprintln(w, "  WARNING: skipping cron timer provisioning — no username available")
		}
	}

	fmt.Fprintln(w, "Note: mailbox + forwarder ROWS are reconstructed above — accounts")
	fmt.Fprintln(w, "authenticate again via the Stalwart SQL directory. Stored Maildir")
	fmt.Fprintln(w, "MESSAGES are NOT replayed by account-restore yet; migrate old mail")
	fmt.Fprintln(w, "separately. DNSSEC keys are not in this bundle — re-enable with")
	fmt.Fprintln(w, "`jabali pdns dnssec enable`.")
}

func boolMark(b bool) string {
	if b {
		return "created"
	}
	return "already present"
}

// accountRestoreUserBlock mirrors the manifest-user JSON shape the
// agent surfaces in backup.restore replies (`user` field). Kept here
// instead of importing internal/backup just to keep this CLI package
// self-contained on the wire side.
type accountRestoreUserBlock struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email,omitempty"`
	UIDAtSource uint32 `json:"uid_at_source,omitempty"`
	IsAdmin     bool   `json:"is_admin"`
}

// ensurePanelUserRow inserts users{ID, Username, Email, IsAdmin,
// LinuxUID, locked PasswordHash} when the row is missing. No-op when
// the row exists. Email defaults to <username>@localhost when the
// manifest carries no email (older snapshots) so the NOT NULL +
// uniqueIndex doesn't reject. Operator must set a real password via
// Kratos recovery — `jabali user password <username> --link`.
func ensurePanelUserRow(ctx context.Context, cmd *cobra.Command, mu accountRestoreUserBlock) error {
	users := repository.NewUserRepository(sharedDB)
	if existing, err := users.FindByID(ctx, mu.ID); err == nil && existing != nil {
		return nil
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("lookup user: %w", err)
	}
	email := mu.Email
	if email == "" {
		email = mu.Username + "@localhost"
	}
	usernamePtr := mu.Username
	now := time.Now().UTC()
	u := &models.User{
		ID:           mu.ID,
		Email:        email,
		Username:     &usernamePtr,
		IsAdmin:      mu.IsAdmin,
		PasswordHash: "!", // locked — Kratos recovery sets the real value
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if mu.UIDAtSource != 0 {
		uid := mu.UIDAtSource
		u.LinuxUID = &uid
	}
	if err := users.Create(ctx, u); err != nil {
		return fmt.Errorf("create user row: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"✓ panel user row reconstructed: id=%s username=%s email=%s is_admin=%t\n"+
			"  Password is LOCKED. Set a new one via:\n"+
			"    jabali user password %s --link\n",
		u.ID, mu.Username, email, mu.IsAdmin, mu.Username)
	return nil
}

// accountManifestRow mirrors the payload returned by
// backup.account_list_manifests.
type accountManifestRow struct {
	SnapshotID string    `json:"snapshot_id"`
	Time       time.Time `json:"time"`
	Hostname   string    `json:"hostname,omitempty"`
	UserID     string    `json:"user_id,omitempty"`
	JobID      string    `json:"job_id,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
}

func newBackupAccountRestoreCmd() *cobra.Command {
	var (
		username       string
		userID         string
		targetUserID   string
		targetUsername string
		snapshotID     string
		destName       string
		applyFlag      bool
		force          bool
		interactive    bool
	)
	cmd := &cobra.Command{
		Use:   "account-restore",
		Short: "Restore one account's backup snapshot via the agent (bypass UI)",
		Long: `End-to-end account restore from the CLI. Resolves the destination
the snapshot lives on, dispatches backup.restore, prints the per-stage
result + applied list + warnings.

Two modes:

  - Scriptable: every flag set on the command line.
  - Interactive: --interactive, OR no flags on a TTY. Walks destination
    → snapshot → user (existing or disaster-recovery) → confirmation.

Examples:
  # Interactive (most common — operator picks from real lists)
  jabali backup account-restore --force

  # Scriptable
  jabali backup account-restore --user shukivaknin --destination test \
      --snapshot <manifest-snapshot-id> --force

  # Recon mode: materialize to staging only, do not apply
  jabali backup account-restore --user shukivaknin --destination test \
      --snapshot <manifest-snapshot-id> --apply=false --force

  # Disaster recovery (system account auto-created if missing)
  jabali backup account-restore \
      --target-user-id <orig-ulid> --target-username shukivaknin \
      --destination test --snapshot <manifest-snapshot-id> --force`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !force {
				return errors.New("account-restore requires --force; refusing on a live host")
			}
			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			useInteractive := interactive ||
				(destName == "" && snapshotID == "" && username == "" &&
					userID == "" && targetUserID == "" && targetUsername == "" &&
					term.IsTerminal(int(os.Stdin.Fd())))

			var picked *models.BackupDestination
			var resolvedID, resolvedName string

			if useInteractive {
				p, ok, err := runAccountRestorePrompts(cmd, ctx,
					&destName, &snapshotID, &username, &userID,
					&targetUserID, &targetUsername, &applyFlag)
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(cmd.OutOrStdout(), "aborted.")
					return nil
				}
				picked = p
			}

			// (Re-)resolve dest from flag if non-interactive, OR
			// interactive returned only IDs.
			if picked == nil {
				if destName == "" {
					return errors.New("--destination required (matches backup_destinations.name)")
				}
				dests := repository.NewBackupDestinationRepository(sharedDB)
				all, err := dests.ListEnabled(ctx)
				if err != nil {
					return fmt.Errorf("list destinations: %w", err)
				}
				for i := range all {
					if all[i].Name == destName {
						picked = &all[i]
						break
					}
				}
				if picked == nil {
					names := make([]string, 0, len(all))
					for _, d := range all {
						names = append(names, d.Name)
					}
					return fmt.Errorf("destination %q not found; available: %v", destName, names)
				}
			}

			if snapshotID == "" {
				return errors.New("--snapshot required (manifest snapshot id)")
			}
			if snapshotID == "latest" {
				return errors.New("--snapshot=latest not yet supported in scriptable mode; use --interactive or pass an explicit manifest snapshot id")
			}

			// Resolve target user (panel-managed OR disaster recovery).
			if resolvedName == "" || resolvedID == "" {
				switch {
				case targetUserID != "" && targetUsername != "":
					resolvedID = targetUserID
					resolvedName = targetUsername
				case username != "" || userID != "":
					users := repository.NewUserRepository(sharedDB)
					if userID != "" {
						u, err := users.FindByID(ctx, userID)
						if err != nil || u == nil {
							return fmt.Errorf("user lookup by id %q: %w (use --target-user-id+--target-username for disaster recovery)", userID, err)
						}
						resolvedID = u.ID
						if u.Username != nil {
							resolvedName = *u.Username
						}
					} else {
						u, err := users.FindByUsername(ctx, username)
						if err != nil || u == nil {
							return fmt.Errorf("user lookup by username %q: %w (use --target-user-id+--target-username for disaster recovery)", username, err)
						}
						resolvedID = u.ID
						if u.Username != nil {
							resolvedName = *u.Username
						}
					}
					if resolvedName == "" {
						return fmt.Errorf("user %q has NULL username (admin user?) — restore needs a Linux account", resolvedID)
					}
				default:
					return errors.New("need --user OR --user-id OR (--target-user-id + --target-username) for disaster recovery")
				}
			}

			jobID := ids.NewULID()
			params := buildRestoreParams(jobID, snapshotID, resolvedID, resolvedName, applyFlag, picked)

			fmt.Fprintf(cmd.OutOrStdout(),
				"→ backup.restore job=%s user=%s snapshot=%s dest=%s(%s) apply=%v\n",
				jobID, resolvedName, snapshotID, picked.Name, picked.ID, applyFlag)

			ag := agent.NewClient(agent.Config{Timeout: 1 * time.Hour})
			callCtx, callCancel := context.WithTimeout(ctx, 1*time.Hour)
			defer callCancel()
			raw, err := ag.Call(callCtx, "backup.restore", params)
			if err != nil {
				return fmt.Errorf("agent backup.restore: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "✓ agent returned:")
			pretty, _ := json.MarshalIndent(json.RawMessage(raw), "  ", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), "  "+string(pretty))

			// Post-restore: walk the meta-stage payload and reinstate
			// every panel-DB row (user, php_pools, domains, ssl_certs,
			// databases, db_users + grants, app_installs, ssh_keys,
			// cron_jobs). Falls back to a manifest-only user-row
			// reconstruction when the snapshot doesn't carry a meta
			// stage (older schema_version=1 snapshots).
			var resp struct {
				User     accountRestoreUserBlock `json:"user"`
				Metadata json.RawMessage         `json:"metadata"`
				Applied  []string                `json:"applied"`
			}
			if jerr := json.Unmarshal(raw, &resp); jerr == nil {
				if len(resp.Metadata) > 0 {
					applyPanelMetadata(ctx, cmd, resp.Metadata)
				} else if resp.User.ID != "" {
					if cerr := ensurePanelUserRow(ctx, cmd, resp.User); cerr != nil {
						fmt.Fprintf(cmd.OutOrStdout(),
							"WARNING: panel user row reconstruction failed: %v\n"+
								"  Run `jabali user create --user-id %s --username %s --email %s` manually.\n",
							cerr, resp.User.ID, resp.User.Username, resp.User.Email)
					}
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&username, "user", "", "username (e.g. shukivaknin) — looks up panel row")
	cmd.Flags().StringVar(&userID, "user-id", "", "user ULID — looks up panel row (alternative to --user)")
	cmd.Flags().StringVar(&targetUserID, "target-user-id", "", "disaster recovery: panel row gone; use this ULID directly (pair with --target-username)")
	cmd.Flags().StringVar(&targetUsername, "target-username", "", "disaster recovery: system account name to chown into (pair with --target-user-id)")
	cmd.Flags().StringVar(&snapshotID, "snapshot", "", "manifest snapshot id (long form preferred)")
	cmd.Flags().StringVar(&destName, "destination", "", "destination name (e.g. 'test', 'b2-prod')")
	cmd.Flags().BoolVar(&applyFlag, "apply", true, "apply home+db onto live system (false = staging-only smoke test)")
	cmd.Flags().BoolVar(&force, "force", false, "required — restore overwrites home tree + reloads databases")
	cmd.Flags().BoolVar(&interactive, "interactive", false, "force interactive prompts even when flags are set")
	return cmd
}

// runAccountRestorePrompts walks: destination pick → manifest list →
// snapshot pick → user pick (panel-managed or disaster recovery) →
// confirmation. Returns (picked, ok, err). ok=false means the operator
// answered no at the final confirmation; caller exits cleanly.
func runAccountRestorePrompts(
	cmd *cobra.Command, ctx context.Context,
	destName, snapshotID, username, userID,
	targetUserID, targetUsername *string,
	applyFlag *bool,
) (*models.BackupDestination, bool, error) {
	w := cmd.OutOrStdout()
	r := bufio.NewReader(os.Stdin)

	// 1. Destination.
	dests := repository.NewBackupDestinationRepository(sharedDB)
	all, err := dests.ListEnabled(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("list destinations: %w", err)
	}
	if len(all) == 0 {
		return nil, false, errors.New("no enabled destinations — create one first via panel UI or `jabali backup destinations …`")
	}
	fmt.Fprintln(w, "\nDestinations:")
	for i, d := range all {
		fmt.Fprintf(w, "  [%d] %s — kind=%s url=%s\n", i+1, d.Name, d.Kind, d.URL)
	}
	pickIdx, err := promptInt(w, r, "Pick destination number: ", 1, len(all))
	if err != nil {
		return nil, false, err
	}
	picked := &all[pickIdx-1]
	*destName = picked.Name

	// 2. List manifests via agent.
	fmt.Fprintln(w, "Listing manifest snapshots (this can take a few seconds for remote dests)…")
	listParams := map[string]any{
		"repo_url": picked.URL,
		"sftp":     backupwrapperhelpers.SFTPWireParams(picked),
	}
	if picked.CredentialsRef != nil {
		listParams["credentials_ref"] = *picked.CredentialsRef
	}
	ag := agent.NewClient(agent.Config{Timeout: 90 * time.Second})
	listCtx, listCancel := context.WithTimeout(ctx, 90*time.Second)
	defer listCancel()
	raw, err := ag.Call(listCtx, "backup.account_list_manifests", listParams)
	if err != nil {
		return nil, false, fmt.Errorf("agent backup.account_list_manifests: %w", err)
	}
	var listResp struct {
		Manifests []accountManifestRow `json:"manifests"`
		Total     int                  `json:"total"`
	}
	if err := json.Unmarshal(raw, &listResp); err != nil {
		return nil, false, fmt.Errorf("decode manifests: %w", err)
	}
	if len(listResp.Manifests) == 0 {
		return nil, false, fmt.Errorf("no account_backup manifests found on destination %q", picked.Name)
	}

	// 3. Group by user-id; render with username if panel knows the row.
	users := repository.NewUserRepository(sharedDB)
	type userBlock struct {
		UserID    string
		Username  string // "(unknown)" when panel row is gone
		Manifests []accountManifestRow
	}
	byUser := map[string]*userBlock{}
	order := []string{}
	for _, m := range listResp.Manifests {
		blk, ok := byUser[m.UserID]
		if !ok {
			blk = &userBlock{UserID: m.UserID, Username: "(unknown)"}
			if u, err := users.FindByID(ctx, m.UserID); err == nil && u != nil && u.Username != nil {
				blk.Username = *u.Username
			}
			byUser[m.UserID] = blk
			order = append(order, m.UserID)
		}
		blk.Manifests = append(blk.Manifests, m)
	}

	fmt.Fprintln(w, "\nUsers with manifests on this destination:")
	for i, uid := range order {
		blk := byUser[uid]
		fmt.Fprintf(w, "  [%d] %s (%s) — %d snapshot(s)\n", i+1, blk.Username, blk.UserID, len(blk.Manifests))
	}
	uIdx, err := promptInt(w, r, "Pick user number: ", 1, len(order))
	if err != nil {
		return nil, false, err
	}
	pickedBlock := byUser[order[uIdx-1]]

	// 4. Snapshot pick.
	fmt.Fprintf(w, "\nSnapshots for %s (%s) — newest first:\n", pickedBlock.Username, pickedBlock.UserID)
	for i, m := range pickedBlock.Manifests {
		fmt.Fprintf(w, "  [%d] %s  job=%s  host=%s\n",
			i+1, m.Time.Format(time.RFC3339), m.JobID, m.Hostname)
	}
	sIdx, err := promptInt(w, r, "Pick snapshot number (or [L] for latest): ", 1, len(pickedBlock.Manifests))
	if err != nil {
		// promptInt returned an error which could be "L" — handle below.
		// Re-prompt with a string parser to support "L".
		val, perr := promptLine(w, r, "Pick snapshot number, or 'L' for latest: ")
		if perr != nil {
			return nil, false, perr
		}
		val = strings.TrimSpace(strings.ToUpper(val))
		if val == "L" {
			sIdx = 1 // newest first; index 0
		} else {
			n, cerr := strconv.Atoi(val)
			if cerr != nil || n < 1 || n > len(pickedBlock.Manifests) {
				return nil, false, fmt.Errorf("invalid snapshot pick %q", val)
			}
			sIdx = n
		}
	}
	pickedSnap := pickedBlock.Manifests[sIdx-1]
	*snapshotID = pickedSnap.SnapshotID

	// 5. Resolve user (panel row OR disaster recovery prompts).
	if pickedBlock.Username == "(unknown)" {
		fmt.Fprintf(w, "\nUser %s has no panel row — disaster-recovery mode.\n", pickedBlock.UserID)
		val, err := promptLine(w, r,
			fmt.Sprintf("Target system username (will be created if missing) [%s]: ", pickedBlock.UserID))
		if err != nil {
			return nil, false, err
		}
		val = strings.TrimSpace(val)
		if val == "" {
			return nil, false, errors.New("disaster recovery needs a username")
		}
		*targetUserID = pickedBlock.UserID
		*targetUsername = val
	} else {
		*username = pickedBlock.Username
	}

	// 6. Apply mode.
	applyVal, err := promptLine(w, r, "Apply onto live system (rsync home + mariadb load)? [Y/n]: ")
	if err != nil {
		return nil, false, err
	}
	applyVal = strings.TrimSpace(strings.ToLower(applyVal))
	*applyFlag = !(applyVal == "n" || applyVal == "no")

	// 7. Final confirmation.
	fmt.Fprintln(w, "\nReady to dispatch:")
	fmt.Fprintf(w, "  destination : %s (%s)\n", picked.Name, picked.URL)
	fmt.Fprintf(w, "  user        : %s (%s)\n", pickedBlock.Username, pickedBlock.UserID)
	fmt.Fprintf(w, "  snapshot    : %s @ %s\n", pickedSnap.SnapshotID[:12], pickedSnap.Time.Format(time.RFC3339))
	fmt.Fprintf(w, "  apply       : %v\n", *applyFlag)
	confirm, err := promptLine(w, r, "Type 'yes' to dispatch (anything else aborts): ")
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(strings.ToLower(confirm)) != "yes" {
		return picked, false, nil
	}
	return picked, true, nil
}

func buildRestoreParams(
	jobID, snapshotID, resolvedID, resolvedName string,
	applyFlag bool,
	picked *models.BackupDestination,
) map[string]any {
	params := map[string]any{
		"job_id":               jobID,
		"manifest_snapshot_id": snapshotID,
		"target_user_id":       resolvedID,
		"target_username":      resolvedName,
		"overwrite":            true,
		"apply_staged":         applyFlag,
		"repo_url":             picked.URL,
		"destination_kind":     picked.Kind,
		"sftp":                 backupwrapperhelpers.SFTPWireParams(picked),
	}
	if picked.CredentialsRef != nil {
		params["credentials_ref"] = *picked.CredentialsRef
	}
	if picked.Kind == models.BackupDestinationKindSFTP {
		if s := picked.ExtraOptionsTyped().SFTP; s != nil {
			params["sftp"] = map[string]any{
				"host":     s.Host,
				"user":     s.User,
				"port":     s.Port,
				"path":     s.Path,
				"auth":     s.Auth,
				"key_path": s.KeyPath,
			}
		}
	}
	return params
}

// promptInt reads a numeric pick from r constrained to [lo, hi].
// Returns an error if the input parses non-numeric (caller can fall
// back to a string-parse, e.g. snapshot pick supports "L").
func promptInt(w interface{ Write([]byte) (int, error) }, r *bufio.Reader, label string, lo, hi int) (int, error) {
	fmt.Fprint(w, label)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return 0, err
	}
	s := strings.TrimSpace(line)
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", s)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("out of range: %d (need %d..%d)", n, lo, hi)
	}
	return n, nil
}

// getUsernameFromMeta extracts username from AccountMetadata with nil checking.
// Returns empty string if username is not available.
func getUsernameFromMeta(meta *internalbackup.AccountMetadata) string {
	if meta == nil || meta.User.Username == nil {
		return ""
	}
	return *meta.User.Username
}

// provisionRestoredCronTimers creates systemd timers for all enabled cron jobs
// in the restored account. Follows the cronApplyOne pattern from reconciler.
func provisionRestoredCronTimers(ctx context.Context, userID, username string, deps backupmetadata.Deps) error {
	if deps.CronJobs == nil {
		return fmt.Errorf("cron job repository not available")
	}

	// Get all cron jobs for the restored user
	jobs, err := deps.CronJobs.ListByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("list cron jobs: %w", err)
	}

	// Filter to enabled jobs only
	enabledJobs := make([]*models.CronJob, 0, len(jobs))
	for i := range jobs {
		if jobs[i].Enabled {
			enabledJobs = append(enabledJobs, jobs[i])
		}
	}

	if len(enabledJobs) == 0 {
		return nil // No enabled jobs to provision
	}

	// Build owned docroots list for security context
	var docroots []string
	if deps.Domains != nil {
		domains, _, err := deps.Domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: 1000})
		if err != nil {
			return fmt.Errorf("list domains for docroots: %w", err)
		}
		docroots = make([]string, 0, len(domains))
		for _, d := range domains {
			if d.DocRoot != "" {
				docroots = append(docroots, d.DocRoot)
			}
		}
	}

	// Create agent client for provisioning
	ag := agent.NewClient(agent.Config{Timeout: 30 * time.Second})

	// Provision each enabled cron job
	for _, job := range enabledJobs {
		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := ag.Call(agentCtx, "cron.apply", map[string]any{
			"user_id":        userID,
			"username":       username,
			"job_id":         job.ID,
			"name":           job.Name,
			"command":        job.Command,
			"schedule":       job.Schedule,
			"owned_docroots": docroots,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("provision cron timer %s (%s): %w", job.ID, job.Name, err)
		}
	}

	return nil
}
