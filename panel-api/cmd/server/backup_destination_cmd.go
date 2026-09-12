package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// credsDir is only a hint path for operator-facing warnings (e.g. "remove X
// manually" when Agent cleanup fails). The row's CredentialsRef is the Agent's
// reported path, not a value computed from this const — see
// writeBackupDestinationCreds in backup_destination_ops.go.
const credsDir = "/etc/jabali-panel/restic-remotes"

func backupDestinationRepoFromDB() repository.BackupDestinationRepository {
	return repository.NewBackupDestinationRepository(sharedDB)
}

func newBackupDestinationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "destination",
		Aliases: []string{"dest"},
		Short:   "Manage backup destinations (local, sftp, s3, b2, azure, gcs, rest)",
	}
	cmd.AddCommand(
		newBackupDestinationListCmd(),
		newBackupDestinationGetCmd(),
		newBackupDestinationCreateCmd(),
		newBackupDestinationUpdateCmd(),
		newBackupDestinationRotatePasswordCmd(),
		newBackupDestinationDeleteCmd(),
		newBackupDestinationTestCmd(),
	)
	return cmd
}

func newBackupDestinationListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List backup destinations",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			rows, err := backupDestinationRepoFromDB().List(ctx)
			if err != nil {
				return fmt.Errorf("list destinations: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"destinations": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Println("No backup destinations.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tKIND\tENABLED\tURL")
			for _, d := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.Kind, boolYN(d.Enabled), d.URL)
			}
			return w.Flush()
		},
	}
}

func newBackupDestinationGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <id-or-name>",
		Short:   "Show a backup destination",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			d, err := resolveBackupDestination(ctx, args[0])
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(d)
			}
			fmt.Printf("ID:       %s\n", d.ID)
			fmt.Printf("Name:     %s\n", d.Name)
			fmt.Printf("Kind:     %s\n", d.Kind)
			fmt.Printf("URL:      %s\n", d.URL)
			fmt.Printf("Enabled:  %s\n", boolYN(d.Enabled))
			if d.CredentialsRef != nil {
				fmt.Printf("Creds:    %s\n", *d.CredentialsRef)
			}
			fmt.Printf("Created:  %s\n", d.CreatedAt.Format(time.RFC3339))
			return nil
		},
	}
}

func newBackupDestinationCreateCmd() *cobra.Command {
	var (
		name     string
		kind     string
		url      string
		envKV    []string
		envStdin bool
		disabled bool
	)

	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a backup destination",
		Long:    "Create a backup destination. For sftp, --url should be 'sftp:user@host:/path'. For s3/b2/etc, supply credentials via --env or --env-stdin (one KEY=VALUE per line).",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			if !validDestKind(kind) {
				return fmt.Errorf("invalid --kind %q (allowed: %v)", kind, models.AllBackupDestinationKinds)
			}
			if err := internalbackup.ValidateURLForKind(kind, url); err != nil {
				return err
			}
			if kind == models.BackupDestinationKindLocal {
				if fi, err := os.Stat(url); err != nil {
					return fmt.Errorf("local path %q does not exist or is unreadable: %w (create it with `install -d -o jabali -g jabali -m 0750 %s` first)", url, err, url)
				} else if !fi.IsDir() {
					return fmt.Errorf("local path %q is not a directory", url)
				}
			}
			env, err := collectEnv(envKV, envStdin)
			if err != nil {
				return err
			}
			d := &models.BackupDestination{
				ID:      ids.NewULID(),
				Name:    name,
				Kind:    kind,
				URL:     url,
				Enabled: !disabled,
			}
			// Credential-file write, persist, and compensate-on-any-failure all
			// live in createBackupDestinationDirect so a failed create can never
			// leak an orphaned secrets file (JAB-310 AC2).
			if err := createBackupDestinationDirect(ctx, sharedAgent.Call, backupDestinationRepoFromDB(), d, env); err != nil {
				if errors.Is(err, repository.ErrConflict) {
					return fmt.Errorf("destination name %q already exists", name)
				}
				return err
			}
			if jsonOutput {
				return printJSON(d)
			}
			cliAuditOK(ctx, "backup_destination.create", "backup_destination", d.ID, nil)
			fmt.Printf("Created destination %s (%s, %s)\n", d.ID, d.Name, d.Kind)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "destination name (required, unique)")
	cmd.Flags().StringVar(&kind, "kind", "", "destination kind: local|sftp|s3|b2|azure|gcs|rest (required)")
	cmd.Flags().StringVar(&url, "url", "", "restic repo URL (required)")
	cmd.Flags().StringArrayVar(&envKV, "env", nil, "credential env: KEY=VALUE (repeatable)")
	cmd.Flags().BoolVar(&envStdin, "env-stdin", false, "read additional KEY=VALUE lines from stdin")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "create in disabled state")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("kind")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

// buildReplacedSFTPBlock builds the full-replace SFTP block for a destination
// update (JAB-310 AC5) purely from the supplied flag values. It consults NO
// stored value: an omitted field is cleared, not carried over — the same
// replace-by-contract the REST update handler enforces by rebuilding
// models.SFTPOptions from req.SFTP. validateSFTPOpts then requires the whole
// block (host+user+path+auth), so a partial edit is rejected rather than
// silently merged with stale stored values (the pre-JAB-310 CLI overlay bug).
// Returns the composed restic URL and the marshalled extra_options JSON.
func buildReplacedSFTPBlock(host, user string, port int, path, auth, keyPath string) (string, []byte, error) {
	opts := &models.SFTPOptions{
		Host:    host,
		User:    user,
		Port:    port,
		Path:    path,
		Auth:    auth,
		KeyPath: keyPath,
	}
	if err := validateSFTPOpts(opts); err != nil {
		// A partial edit lands here: the block is built only from the flags, so a
		// missing field is an incomplete block, not a typo. Name the new contract —
		// the bare validator message ("sftp host and user are required") gives no
		// hint that the CLI now needs the WHOLE block passed together, where the
		// pre-JAB-310 overlay used to fill the rest from the stored row.
		return "", nil, fmt.Errorf("--sftp-* flags replace the whole SFTP block; pass --sftp-host, --sftp-user, --sftp-path and --sftp-auth together: %w", err)
	}
	url := internalbackup.ComposeSFTPURL(internalbackup.SFTPInputs{Host: opts.Host, User: opts.User, Path: opts.Path})
	raw, _ := json.Marshal(models.BackupDestinationExtraOptions{SFTP: opts})
	return url, raw, nil
}

func newBackupDestinationUpdateCmd() *cobra.Command {
	var (
		name        string
		url         string
		enable      bool
		disable     bool
		envKV       []string
		envStdin    bool
		sftpHost    string
		sftpUser    string
		sftpPort    int
		sftpPath    string
		sftpAuth    string
		sftpKeyPath string
		sftpPass    string
		clearCreds  bool
	)
	cmd := &cobra.Command{
		Use:   "update <id-or-name>",
		Short: "Update a backup destination",
		Args:  cobra.ExactArgs(1),
		// Credential writes (--env/--env-stdin/--sftp-password/--clear-creds)
		// talk to the agent; structured SFTP field edits and name/url/enable
		// are DB-only. Require the agent only when a credential flag is set.
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("env") || cmd.Flags().Changed("env-stdin") ||
				cmd.Flags().Changed("sftp-password") || cmd.Flags().Changed("clear-creds") {
				return requireDBAndAgent(cmd, args)
			}
			return requireDB(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			d, err := resolveBackupDestination(ctx, args[0])
			if err != nil {
				return err
			}
			// Capture whether the DB row already had a credential file BEFORE any
			// mutation. It gates the compensating cleanup at persist time: only a
			// file this update newly wrote (row had none) may be removed on
			// failure — a pre-existing file is still referenced by the surviving
			// row (JAB-310).
			origHadCredsFile := d.CredentialsRef != nil
			changed := false
			if cmd.Flags().Changed("name") {
				d.Name = name
				changed = true
			}
			if cmd.Flags().Changed("url") {
				if err := internalbackup.ValidateURLForKind(d.Kind, url); err != nil {
					return err
				}
				if d.Kind == models.BackupDestinationKindLocal {
					if fi, err := os.Stat(url); err != nil {
						return fmt.Errorf("local path %q does not exist: %w", url, err)
					} else if !fi.IsDir() {
						return fmt.Errorf("local path %q is not a directory", url)
					}
				}
				d.URL = url
				changed = true
			}
			if enable {
				d.Enabled = true
				changed = true
			}
			if disable {
				d.Enabled = false
				changed = true
			}
			// Structural SFTP field edits (host/user/port/path/auth/key-path)
			// REPLACE the stored block wholesale — the same full-block-replace
			// contract the REST update handler enforces (JAB-310 AC5). The block
			// is built FRESH from the flags, not overlaid on the stored block, so
			// an omitted field is cleared (a flag left off means "empty here"),
			// exactly as REST rebuilds models.SFTPOptions purely from req.SFTP.
			// validateSFTPOpts then forces the caller to supply the whole block
			// (host+user+path+auth), mirroring the REST validateSFTPInputs gate.
			//
			// --sftp-password is deliberately NOT a structural field: it is a
			// credential (SSHPASS) write, handled independently below, so a
			// password rotation stays a single-flag operation and does not force
			// the operator to resend the entire SFTP block. This is the one
			// intentional CLI affordance beyond the REST handler, whose SSHPASS
			// write is coupled to a present req.SFTP block.
			sftpStructural := false
			for _, f := range []string{"sftp-host", "sftp-user", "sftp-port", "sftp-path", "sftp-auth", "sftp-key-path"} {
				if cmd.Flags().Changed(f) {
					sftpStructural = true
					break
				}
			}
			if sftpStructural {
				if d.Kind != models.BackupDestinationKindSFTP {
					return fmt.Errorf("--sftp-* flags only apply to sftp destinations (kind=%s)", d.Kind)
				}
				// Full-block REPLACE built purely from the flags (buildReplacedSFTPBlock
				// consults no stored value), so an omitted field is cleared, not carried.
				newURL, raw, err := buildReplacedSFTPBlock(sftpHost, sftpUser, sftpPort, sftpPath, sftpAuth, sftpKeyPath)
				if err != nil {
					return err
				}
				d.URL = newURL
				d.ExtraOptions = raw
				changed = true
			}
			// SFTP password (SSHPASS) — an independent credential write, not a
			// structural block edit. Rotates on --sftp-password alone; the block
			// above is left untouched. The destination's effective auth must be
			// "password" — either just set by a full block replace in this same
			// command (d.ExtraOptions was rewritten above) or already stored —
			// so a password cannot be written to a key-auth destination.
			if cmd.Flags().Changed("sftp-password") {
				effAuth := ""
				if s := d.ExtraOptionsTyped().SFTP; s != nil {
					effAuth = s.Auth
				}
				if err := models.SFTPPasswordWriteAllowed(d.Kind, effAuth); err != nil {
					// Append the CLI-specific remedy the shared (flag-agnostic)
					// predicate deliberately omits, so the REST 400 detail stays clean.
					return fmt.Errorf("%w; pass --sftp-auth password with the full sftp block to switch", err)
				}
				path, err := writeBackupDestinationCreds(ctx, sharedAgent.Call, d.ID, map[string]string{"SSHPASS": sftpPass})
				if err != nil {
					return fmt.Errorf("write sftp password: %w", err)
				}
				d.CredentialsRef = &path
				changed = true
			}
			// Clear stored credential env (cloud secrets / sftp SSHPASS). Drop the
			// reference here, but DEFER the on-disk file removal to after the row
			// persists (see updateBackupDestinationDirect): deleting it now, before
			// persist, would leave a surviving row (failed persist) pointing at an
			// already-deleted file — a dangling reference, the reverse of the orphan
			// leak (JAB-310).
			clearedCredsFile := false
			if clearCreds {
				if d.CredentialsRef != nil {
					clearedCredsFile = true
				}
				d.CredentialsRef = nil
				changed = true
			}
			// Rewrite cloud/SFTP credentials (s3/b2/azure/gcs/rest secrets,
			// or sftp key material) via the same agent path create uses.
			if cmd.Flags().Changed("env") || cmd.Flags().Changed("env-stdin") {
				env, err := collectEnv(envKV, envStdin)
				if err != nil {
					return err
				}
				if len(env) > 0 {
					path, err := writeBackupDestinationCreds(ctx, sharedAgent.Call, d.ID, env)
					if err != nil {
						return fmt.Errorf("write credentials: %w", err)
					}
					d.CredentialsRef = &path
					changed = true
				}
			}
			if !changed {
				return fmt.Errorf("no changes specified")
			}
			// Persist, then reconcile the on-disk credential file to the committed
			// row: on failure compensate a file this update newly wrote (orphan),
			// and on success reap a file --clear-creds dropped the reference to
			// (dangling-ref reverse-class) — both handled in the core (JAB-310).
			if err := updateBackupDestinationDirect(ctx, sharedAgent.Call, backupDestinationRepoFromDB(), d, origHadCredsFile, clearedCredsFile); err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(d)
			}
			cliAuditOK(ctx, "backup_destination.update", "backup_destination", d.ID, nil)
			fmt.Printf("Updated destination %s (%s)\n", d.ID, d.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&url, "url", "", "new restic repo URL (validated against existing kind)")
	cmd.Flags().BoolVar(&enable, "enable", false, "mark destination enabled")
	cmd.Flags().BoolVar(&disable, "disable", false, "mark destination disabled")
	cmd.Flags().StringArrayVar(&envKV, "env", nil, "rewrite credential env: KEY=VALUE (repeatable; s3/b2/azure/gcs/rest/sftp secrets)")
	cmd.Flags().BoolVar(&envStdin, "env-stdin", false, "read additional KEY=VALUE credential lines from stdin")
	cmd.Flags().StringVar(&sftpHost, "sftp-host", "", "sftp host (sftp kind)")
	cmd.Flags().StringVar(&sftpUser, "sftp-user", "", "sftp user")
	cmd.Flags().IntVar(&sftpPort, "sftp-port", 0, "sftp port (default 22)")
	cmd.Flags().StringVar(&sftpPath, "sftp-path", "", "sftp remote path")
	cmd.Flags().StringVar(&sftpAuth, "sftp-auth", "", "sftp auth: 'key' or 'password'")
	cmd.Flags().StringVar(&sftpKeyPath, "sftp-key-path", "", "absolute path to private key (auth=key)")
	cmd.Flags().StringVar(&sftpPass, "sftp-password", "", "sftp password (auth=password; stored as SSHPASS)")
	cmd.Flags().BoolVar(&clearCreds, "clear-creds", false, "delete stored credential env for this destination")
	cmd.MarkFlagsMutuallyExclusive("enable", "disable")
	return cmd
}

func newBackupDestinationDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <id-or-name>",
		Short:   "Delete a backup destination",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			d, err := resolveBackupDestination(ctx, args[0])
			if err != nil {
				return err
			}
			if !force {
				fmt.Printf("Delete destination %s (%s)? Schedules referencing it will lose this destination. [y/N]: ", d.ID, d.Name)
				var c string
				fmt.Scanln(&c)
				if c != "y" && c != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			if err := deleteBackupDestinationDirect(ctx, sharedAgent.Call, backupDestinationRepoFromDB(), d, os.Stderr); err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(map[string]string{"deleted": d.ID})
			}
			cliAuditOK(ctx, "backup_destination.delete", "backup_destination", d.ID, nil)
			fmt.Printf("Deleted destination %s (%s)\n", d.ID, d.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation")
	return cmd
}

func newBackupDestinationTestCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "test <id-or-name>",
		Short:   "Test connectivity (auto-inits restic repo if missing)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			d, err := resolveBackupDestination(ctx, args[0])
			if err != nil {
				return err
			}
			if d.Kind == models.BackupDestinationKindLocal {
				if jsonOutput {
					return printJSON(map[string]any{"status": "ok", "detail": "local destination — no remote test"})
				}
				fmt.Println("Status: ok (local destination — no remote test needed)")
				return nil
			}
			params := map[string]any{
				"url":  d.URL,
				"sftp": backupwrapperhelpers.SFTPWireParams(d),
			}
			if d.CredentialsRef != nil {
				params["credentials_ref"] = *d.CredentialsRef
			}
			if d.Kind == models.BackupDestinationKindSFTP {
				if s := d.ExtraOptionsTyped().SFTP; s != nil {
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
			raw, err := sharedAgent.Call(ctx, "backup.dest.test", params)
			if err != nil {
				return fmt.Errorf("test: %w", err)
			}
			var result struct {
				Status        string `json:"status"`
				StdoutPreview string `json:"stdout_preview,omitempty"`
				Stderr        string `json:"stderr,omitempty"`
				Detail        string `json:"detail,omitempty"`
			}
			_ = json.Unmarshal(raw, &result)
			if jsonOutput {
				return printJSON(result)
			}
			fmt.Printf("Status: %s\n", result.Status)
			if result.Detail != "" {
				fmt.Printf("Detail: %s\n", result.Detail)
			}
			if result.StdoutPreview != "" {
				fmt.Printf("Output: %s\n", strings.TrimSpace(result.StdoutPreview))
			}
			if result.Stderr != "" {
				fmt.Printf("Stderr: %s\n", strings.TrimSpace(result.Stderr))
			}
			return nil
		},
	}
}

func resolveBackupDestination(ctx context.Context, lookup string) (*models.BackupDestination, error) {
	repo := backupDestinationRepoFromDB()
	if d, err := repo.Get(ctx, lookup); err == nil {
		return d, nil
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("lookup by id: %w", err)
	}
	if d, err := repo.GetByName(ctx, lookup); err == nil {
		return d, nil
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("lookup by name: %w", err)
	}
	return nil, fmt.Errorf("destination %q not found", lookup)
}

func validDestKind(k string) bool {
	for _, v := range models.AllBackupDestinationKinds {
		if v == k {
			return true
		}
	}
	return false
}

func collectEnv(kv []string, fromStdin bool) (map[string]string, error) {
	env := make(map[string]string, len(kv))
	for _, item := range kv {
		k, v, ok := strings.Cut(item, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --env %q (need KEY=VALUE)", item)
		}
		if strings.ContainsAny(v, "\n\r") {
			return nil, fmt.Errorf("env value for %q contains newline", k)
		}
		env[k] = v
	}
	if fromStdin {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok || k == "" {
				return nil, fmt.Errorf("invalid stdin line %q (need KEY=VALUE)", line)
			}
			env[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
	}
	return env, nil
}
