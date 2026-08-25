// `jabali user reprovision <email|username|user-id>` (JAB-133) — CLI
// equivalent of the admin action POST /users/:id/reprovision. Re-creates the
// user's OS account via the agent (adduser + home dir), then syncs the panel
// password hash so login matches. Non-admin users only. Mirrors the HTTP
// handler exactly: agent-first + synchronous, so a failed OS side leaves the
// DB password untouched.
//
// Password resolution matches `user password`:
//
//	--password <pwd>    explicit value
//	--password-stdin    piped, no echo (automation)
//	(omitted)           auto-generate a strong password + print it once
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

func newUserReprovisionCmd() *cobra.Command {
	var (
		password string
		viaStdin bool
	)
	cmd := &cobra.Command{
		Use:   "reprovision <email|username|user-id>",
		Short: "Re-run OS provisioning for a user + sync the panel password",
		Long: `Re-creates the user's OS account via the agent (adduser + home dir),
then syncs the panel password hash so login matches. Non-admin users only —
admins have no OS account. Effects match the GUI "reprovision" action.

Password: --password <pwd> | --password-stdin | (omitted → auto-generate + print once).`,
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			lookup := strings.TrimSpace(args[0])
			if lookup == "" {
				return fmt.Errorf("user identifier is required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()

			target, err := resolveUser(ctx, lookup)
			if err != nil {
				return err
			}
			if target.IsAdmin {
				return fmt.Errorf("user %s (%s) is an admin — admins have no OS account to reprovision", target.ID, target.Email)
			}
			if target.Username == nil || *target.Username == "" {
				return fmt.Errorf("user %s (%s) has no username — cannot derive an OS account", target.ID, target.Email)
			}
			username := *target.Username

			if password != "" && viaStdin {
				return fmt.Errorf("--password and --password-stdin are mutually exclusive")
			}
			generated := ""
			newPwd := password
			switch {
			case viaStdin:
				p, perr := readPasswordStdin()
				if perr != nil {
					return perr
				}
				newPwd = p
			case newPwd == "":
				newPwd = ids.NewULID()
				generated = newPwd
			}
			if len(newPwd) < 10 {
				return fmt.Errorf("password must be at least 10 characters")
			}

			// Agent-first + synchronous (like the HTTP handler): if the OS
			// side fails, the DB password is left untouched.
			agentCtx, cancel2 := context.WithTimeout(ctx, 30*time.Second)
			defer cancel2()
			raw, aerr := sharedAgent.Call(agentCtx, "user.create", map[string]any{
				"username": username,
				"home_dir": "/home/" + username,
				"shell":    "/usr/local/bin/jabali-ssh-shell",
				"password": newPwd,
			})
			if aerr != nil {
				var ae *agent.AgentError
				if errors.As(aerr, &ae) && ae.Code == agent.CodeAlreadyExists {
					return fmt.Errorf("OS user %q already exists — sync the password only with `jabali user password %s` instead of reprovisioning", username, target.ID)
				}
				return fmt.Errorf("agent user.create failed: %w", aerr)
			}

			// OS converged — sync the panel bcrypt hash (cost 12, matching
			// `user password`).
			hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), 12)
			if err != nil {
				return fmt.Errorf("bcrypt hash: %w", err)
			}
			target.PasswordHash = string(hash)
			// JAB-287: persist the freshly-allocated uid (parity with the REST
			// handler) so users.linux_uid tracks the real OS uid after useradd.
			if uid := userops.ParseReprovisionedUID(raw); uid != nil {
				target.LinuxUID = uid
			}
			if err := repository.NewUserRepository(sharedDB).Update(ctx, target); err != nil {
				return fmt.Errorf("update panel password hash: %w", err)
			}

			// JAB-287: re-evaluate maldet inotify watches (the reprovision may have
			// recreated the home dir) — the REST handler does this; the CLI must too,
			// or CLI-reprovisioned homes go unwatched until the next full reconcile.
			// Best-effort, matching the REST path's fire-and-forget.
			monCtx, monCancel := context.WithTimeout(ctx, 10*time.Second)
			if _, mErr := sharedAgent.Call(monCtx, "security.malware.monitor.reload", map[string]any{}); mErr != nil {
				slog.Debug("malware monitor reload skipped", "err", mErr)
			}
			monCancel()

			if jsonOutput {
				out := map[string]any{"user_id": target.ID, "username": username, "reprovisioned": true}
				if generated != "" {
					out["password"] = generated
				}
				return printJSON(out)
			}
			fmt.Printf("Reprovisioned user %s (%s): OS account created + panel password synced\n", target.ID, username)
			if generated != "" {
				fmt.Printf("Generated password: %s\n", generated)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&password, "password", "", "new password (min 10 chars; omit to auto-generate)")
	cmd.Flags().BoolVar(&viaStdin, "password-stdin", false, "read password from stdin (no prompt, no echo)")
	return cmd
}
