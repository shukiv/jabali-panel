// `jabali system restore` — operator-facing CLI for bare-metal recovery.
// Walks the disaster-recovery flow end-to-end. Two modes:
//
//   - Scriptable (every flag set): used by install.sh --restore-from
//     and ops automation.
//   - Interactive (flags missing on a TTY, or --interactive): prompts
//     the operator for the remote URL, restic password (hidden),
//     credentials env path (optional), snapshot pick from a numbered
//     list of available system_backup manifests, and the stage set
//     to apply. Confirms before dispatch.
//
// Stops jabali-panel.service before dispatching; agent stays up
// (it serves the dispatch socket). Restarts panel after.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
)

func newSystemRestoreCmd() *cobra.Command {
	var (
		remoteURL    string
		credsRef     string
		sftpPort     int
		sftpAuth     string
		sftpKeyPath  string
		passwordFile string
		passwordCLI  string
		snapshot     string
		applyStages  []string
		includeAccts bool
		apply        bool
		force        bool
		interactive  bool
	)
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a system backup onto this host (CLI; ADR-0080)",
		Long: `Restore a system backup onto a clean OS install. The bare-metal
recovery flow: fresh OS → bash <(install.sh --restore-from=...) →
jabali system restore --snapshot=latest --force.

Drops into INTERACTIVE prompts when --interactive is set, or when
--remote-url is empty AND stdin is a TTY. Asks for the repo URL,
restic password (hidden), credentials env file, snapshot pick, and
stage selection.

Stops jabali-panel.service before dispatching, restarts it after.
jabali-agent.service stays up — its socket is the dispatch target.

Refuses without --force on a live host because the restore overwrites
the running panel.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
			defer cancel()

			useInteractive := interactive || (remoteURL == "" && term.IsTerminal(int(os.Stdin.Fd())))
			if useInteractive {
				if err := runInteractiveRestorePrompts(cmd.OutOrStdout(),
					&remoteURL, &credsRef, &sftpPort, &sftpAuth, &sftpKeyPath, &passwordCLI,
					&snapshot, &applyStages, &includeAccts, &apply, &force); err != nil {
					return err
				}
			}

			if !force {
				return errors.New("system restore requires --force; refusing on a running panel")
			}
			if remoteURL == "" {
				return errors.New("--remote-url is required (or run with --interactive)")
			}
			if snapshot == "" {
				snapshot = "latest"
			}

			// Password handling: --password-cli (interactive or flag)
			// trumps everything; otherwise --password-file or the
			// canonical /etc/jabali-panel/restic-repo.password.
			passwordFileForAgent := passwordFile
			if passwordCLI != "" {
				tmp, terr := writeTempPassword(passwordCLI)
				if terr != nil {
					return fmt.Errorf("write temp password: %w", terr)
				}
				defer os.Remove(tmp)
				passwordFileForAgent = tmp
			}

			// Agent's per-request default is 120s. Even a system-only
			// restore can blow past that on a slow SFTP link, and an
			// include-accounts run trivially does (5 users × 4 stages
			// of restic restore + a multi-MB SQL load each). 4h is
			// generous enough that we never time-out the operator
			// mid-disaster; agent server.go honors req.Deadline above
			// its own PerRequestTimeout.
			ag := agent.NewClient(agent.Config{Timeout: 4 * time.Hour})
			deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 4*time.Hour)
			defer deadlineCancel()

			fmt.Fprintln(cmd.OutOrStdout(), "stopping jabali-panel.service…")
			_ = exec.CommandContext(ctx, "systemctl", "stop", "jabali-panel.service").Run()
			defer func() {
				fmt.Fprintln(cmd.OutOrStdout(), "starting jabali-panel.service…")
				// Retry until is-active returns "active" — single
				// systemctl start can return 0 yet leave the unit in
				// "activating" or transient failure state, leaving
				// nginx upstream 502 on the operator. Loop a few
				// times so we don't return to a bricked panel.
				for i := 0; i < 6; i++ {
					_ = exec.CommandContext(context.Background(),
						"systemctl", "start", "jabali-panel.service").Run()
					out, _ := exec.CommandContext(context.Background(),
						"systemctl", "is-active", "jabali-panel.service").Output()
					if strings.TrimSpace(string(out)) == "active" {
						fmt.Fprintln(cmd.OutOrStdout(), "✓ jabali-panel.service active")
						return
					}
					time.Sleep(2 * time.Second)
				}
				fmt.Fprintln(cmd.OutOrStdout(),
					"⚠ jabali-panel.service did not reach active after 6 attempts — investigate via `journalctl -u jabali-panel.service`")
			}()

			jobID := ids.NewULID()
			params := map[string]any{
				"job_id":               jobID,
				"manifest_snapshot_id": snapshot,
				"include_accounts":     includeAccts,
				"repo_url":             remoteURL,
				"credentials_ref":      credsRef,
				"password_file":        passwordFileForAgent,
				"sftp":                 sftpWireFromURL(remoteURL, sftpPort, sftpAuth, sftpKeyPath),
				"apply":                apply,
				"apply_stages":         applyStages,
			}
			fmt.Fprintf(cmd.OutOrStdout(), "→ agent system.restore job=%s snapshot=%s repo=%s apply=%v include_accounts=%v\n",
				jobID, snapshot, remoteURL, apply, includeAccts)
			raw, err := ag.Call(deadlineCtx, "system.restore", params)
			if err != nil {
				return fmt.Errorf("agent system.restore: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "✓ agent dispatch returned:")
			pretty, _ := json.MarshalIndent(json.RawMessage(raw), "  ", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), "  "+string(pretty))
			fmt.Fprintln(cmd.OutOrStdout(),
				"NOTE: stages restored to /var/lib/jabali-backups/restore-staging/<job-id>/.")
			return nil
		},
	}
	cmd.Flags().StringVar(&remoteURL, "remote-url", "", "restic repo URL or local path (e.g. sftp:user@host:/path)")
	cmd.Flags().StringVar(&credsRef, "credentials-ref", "", "absolute path to env file with backend creds (root:root 0600)")
	// JAB-194: --extra-option used to take a raw restic `-o KEY=VALUE` body and
	// hand it to the agent, which passed it straight into restic's argv. One of
	// those keys, sftp.command, is the command restic EXECUTES to reach an SFTP
	// backend, so the wire field was arbitrary command execution as root. The
	// agent no longer accepts it; these typed flags carry the same capability
	// (non-default port, explicit key, password auth) and the agent builds the
	// option itself.
	cmd.Flags().IntVar(&sftpPort, "sftp-port", 0, "SFTP port when not 22")
	cmd.Flags().StringVar(&sftpAuth, "sftp-auth", "", "SFTP auth mode: key | password (default: ssh config)")
	cmd.Flags().StringVar(&sftpKeyPath, "sftp-key", "", "absolute path to the SSH private key for SFTP")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "restic password file (default: /etc/jabali-panel/restic-repo.password)")
	cmd.Flags().StringVar(&passwordCLI, "password", "", "restic password (literal; overrides --password-file). Avoid in shell history; prefer --interactive")
	cmd.Flags().StringVar(&snapshot, "snapshot", "", "system_manifest snapshot ID, or 'latest' to auto-pick newest")
	cmd.Flags().StringSliceVar(&applyStages, "apply-stage", nil, "restrict apply to named stages (repeatable). Empty = panel_db + panel_config + tls (the safe defaults)")
	cmd.Flags().BoolVar(&includeAccts, "include-accounts", false, "also restore each linked account")
	cmd.Flags().BoolVar(&apply, "apply", true, "after staging, apply selected stages onto live host (default true)")
	cmd.Flags().BoolVar(&force, "force", false, "required — restore overwrites the running panel")
	cmd.Flags().BoolVar(&interactive, "interactive", false, "force interactive prompts even when --remote-url is set")
	return cmd
}

// writeTempPassword stashes the password under a 0600 root-owned tmp
// file the agent can read. install.sh's canonical
// /etc/jabali-panel/restic-repo.password is left untouched so a drill
// run on a live host doesn't clobber the runtime backup creds.
func writeTempPassword(pw string) (string, error) {
	if err := os.MkdirAll("/run/jabali", 0o750); err != nil {
		return "", err
	}
	f, err := os.CreateTemp("/run/jabali", "restore-pw-*.txt")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(pw); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	_ = f.Chmod(0o600)
	_ = f.Close()
	return f.Name(), nil
}

// runInteractiveRestorePrompts walks the operator through the full
// restore picker. Mutates the bound flag pointers in place so the
// caller's existing flag-driven code path keeps working.
func runInteractiveRestorePrompts(
	w io.Writer,
	remoteURL, credsRef *string,
	sftpPort *int,
	sftpAuth *string,
	sftpKeyPath *string,
	passwordCLI, snapshot *string,
	applyStages *[]string,
	includeAccts, apply, force *bool,
) error {
	r := bufio.NewReader(os.Stdin)
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "═══════════════════════════════════════════════")
	fmt.Fprintln(w, "  jabali system restore — interactive disaster recovery")
	fmt.Fprintln(w, "═══════════════════════════════════════════════")
	fmt.Fprintln(w, "")

	// Remote URL
	for *remoteURL == "" {
		val, err := promptLine(w, r, "Remote restic repo URL (e.g. sftp:user@host:/path, /var/lib/jabali-backups/repo): ")
		if err != nil {
			return err
		}
		*remoteURL = strings.TrimSpace(val)
	}

	// Restic password (hidden)
	if *passwordCLI == "" {
		fmt.Fprint(w, "Restic password (hidden): ")
		pw, err := readPasswordHidden()
		fmt.Fprintln(w, "")
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if pw == "" {
			return errors.New("restic password is required")
		}
		*passwordCLI = pw
	}

	// Credentials env file (optional)
	if *credsRef == "" {
		val, err := promptLine(w, r, "Backend credentials env file path (optional, blank to skip): ")
		if err != nil {
			return err
		}
		val = strings.TrimSpace(val)
		if val != "" {
			if _, err := os.Stat(val); err != nil {
				return fmt.Errorf("creds env file not found: %w", err)
			}
			*credsRef = val
		}
	}

	// SFTP transport details, asked only when the repo actually is SFTP.
	// Typed rather than a free-form `-o` line (JAB-194): the agent builds the
	// sftp.command option from these, so nothing the operator types here ever
	// becomes a command the agent executes.
	if strings.HasPrefix(strings.TrimSpace(*remoteURL), "sftp:") && *sftpPort == 0 && *sftpAuth == "" {
		portStr, err := promptLine(w, r, "SFTP port (blank = 22): ")
		if err != nil {
			return err
		}
		if v := strings.TrimSpace(portStr); v != "" {
			n, cerr := strconv.Atoi(v)
			if cerr != nil || n < 1 || n > 65535 {
				return fmt.Errorf("invalid SFTP port %q", v)
			}
			*sftpPort = n
		}
		auth, err := promptLine(w, r, "SFTP auth (key / password, blank = ssh config): ")
		if err != nil {
			return err
		}
		switch v := strings.ToLower(strings.TrimSpace(auth)); v {
		case "", "key", "password":
			*sftpAuth = v
		default:
			return fmt.Errorf("invalid SFTP auth %q — use key or password", v)
		}
		if *sftpAuth == "key" {
			keyPath, err := promptLine(w, r, "SSH private key path (blank = ssh default): ")
			if err != nil {
				return err
			}
			*sftpKeyPath = strings.TrimSpace(keyPath)
		}
	}

	// Snapshot pick: query agent for available manifests.
	if *snapshot == "" {
		ag := agent.NewClient(agent.Config{})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// Same temp-password trick we'll use for the dispatch.
		tmp, terr := writeTempPassword(*passwordCLI)
		if terr != nil {
			return fmt.Errorf("write temp password: %w", terr)
		}
		defer os.Remove(tmp)

		listParams := map[string]any{
			"repo_url":        *remoteURL,
			"credentials_ref": *credsRef,
			"password_file":   tmp,
			"sftp":            sftpWireFromURL(*remoteURL, *sftpPort, *sftpAuth, *sftpKeyPath),
		}
		raw, err := ag.Call(ctx, "system.restore_list_manifests", listParams)
		if err != nil {
			return fmt.Errorf("list manifests: %w", err)
		}
		var resp struct {
			Manifests []struct {
				SnapshotID string    `json:"snapshot_id"`
				Time       time.Time `json:"time"`
				Hostname   string    `json:"hostname"`
				Tags       []string  `json:"tags"`
			} `json:"manifests"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return fmt.Errorf("parse list-manifests: %w", err)
		}
		if resp.Total == 0 {
			return errors.New("no system_backup manifests found in repo — nothing to restore")
		}
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Available system manifests (newest first):")
		for i, m := range resp.Manifests {
			fmt.Fprintf(w, "  [%d]  %s  %s  host=%s\n",
				i+1, m.Time.Local().Format("2006-01-02 15:04:05"), m.SnapshotID[:8], m.Hostname)
		}
		fmt.Fprintln(w, "  [L]  latest (newest)")
		val, err := promptLine(w, r, "Pick number or 'L' for latest: ")
		if err != nil {
			return err
		}
		val = strings.TrimSpace(strings.ToLower(val))
		if val == "" || val == "l" || val == "latest" {
			*snapshot = "latest"
		} else {
			n, perr := strconv.Atoi(val)
			if perr != nil || n < 1 || n > len(resp.Manifests) {
				return fmt.Errorf("invalid pick: %q", val)
			}
			*snapshot = resp.Manifests[n-1].SnapshotID
		}
	}

	// Stage selection.
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Apply phase: which stages should be auto-applied?")
	fmt.Fprintln(w, "  Default (just hit ENTER):  panel_db, panel_config, tls — the safe set.")
	fmt.Fprintln(w, "  Optional, more destructive: mail_state, service_config, security.")
	fmt.Fprintln(w, "  Always manual:             os_users, data_state.")
	val, err := promptLine(w, r, "Stages (comma-separated, or blank for default; 'all' = all auto-recoverable): ")
	if err != nil {
		return err
	}
	val = strings.TrimSpace(strings.ToLower(val))
	switch val {
	case "":
		// default — empty whitelist means agent applies the safe defaults.
	case "all":
		*applyStages = []string{"panel_db", "panel_config", "tls", "mail_state", "service_config", "security"}
	default:
		for _, s := range strings.Split(val, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				*applyStages = append(*applyStages, s)
			}
		}
	}

	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Account data — restore every user's home directory + databases?")
	fmt.Fprintln(w, "  This walks every kind=account_backup snapshot in the repo and")
	fmt.Fprintln(w, "  rsyncs /home/<user> + loads each per-user MariaDB dump on top")
	fmt.Fprintln(w, "  of the system restore. Mailboxes + cron/ssh/php/apps stay")
	fmt.Fprintln(w, "  staged-only (operator review).")
	acctVal, err := promptLine(w, r, "Include accounts? [Y/n]: ")
	if err != nil {
		return err
	}
	switch strings.TrimSpace(strings.ToLower(acctVal)) {
	case "", "y", "yes":
		*includeAccts = true
	default:
		*includeAccts = false
	}

	// Final confirm.
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "─── Summary ───")
	fmt.Fprintf(w, "  remote URL:    %s\n", *remoteURL)
	fmt.Fprintf(w, "  creds file:    %s\n", strOrPlaceholder(*credsRef, "(none)"))
	if *sftpPort != 0 || *sftpAuth != "" {
		fmt.Fprintf(w, "  sftp:          port=%d auth=%s key=%s\n", *sftpPort, *sftpAuth, *sftpKeyPath)
	}
	fmt.Fprintf(w, "  snapshot:      %s\n", *snapshot)
	fmt.Fprintf(w, "  apply stages:  %v\n", *applyStages)
	fmt.Fprintf(w, "  include accts: %v\n", *includeAccts)
	fmt.Fprintln(w, "")
	confirm, err := promptLine(w, r, "Type 'yes' to dispatch (anything else aborts): ")
	if err != nil {
		return err
	}
	if strings.TrimSpace(strings.ToLower(confirm)) != "yes" {
		return errors.New("aborted by operator")
	}
	*apply = true
	*force = true
	return nil
}

func promptLine(w io.Writer, r *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(w, prompt)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func readPasswordHidden() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Non-TTY fallback: read line normally (no masking possible).
		r := bufio.NewReader(os.Stdin)
		line, err := r.ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

func strOrPlaceholder(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// sftpWireFromURL builds the typed `sftp` block for an agent restore request.
//
// JAB-194: the agent no longer accepts a pre-built `-o sftp.command=...` string
// — that option is the command restic EXECUTES to reach the backend, so
// carrying it over the wire handed anything that could reach the agent
// arbitrary command execution as root. The agent builds it from these fields
// instead, so nothing an operator types becomes a command.
//
// Host/user/path come from the repo URL the operator already supplied
// (`sftp:user@host:/path`); port, auth mode and key path are the parts that URL
// cannot express. Returns nil for a non-SFTP repo or one needing no override,
// which the agent reads as "plain ssh defaults".
func sftpWireFromURL(remoteURL string, port int, auth, keyPath string) map[string]any {
	rest, ok := strings.CutPrefix(strings.TrimSpace(remoteURL), "sftp:")
	if !ok {
		return nil
	}
	if port == 0 && auth == "" && keyPath == "" {
		return nil // default port, default key, ssh config decides
	}
	var user, host, path string
	if at := strings.Index(rest, "@"); at >= 0 {
		user, rest = rest[:at], rest[at+1:]
	}
	if colon := strings.Index(rest, ":"); colon >= 0 {
		host, path = rest[:colon], rest[colon+1:]
	} else {
		host = rest
	}
	return map[string]any{
		"host":     host,
		"user":     user,
		"port":     port,
		"path":     path,
		"auth":     auth,
		"key_path": keyPath,
	}
}
