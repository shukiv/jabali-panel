package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage panel users",
	}
	cmd.AddCommand(
		newUserListCmd(),
		newUserCreateCmd(),
		newUserDeleteCmd(),
		newUserPasswordCmd(),
		newUserReprovisionCmd(),
		newUser2FAResetCmd(),
		newUserSuspendCmd(),
		newUserUnsuspendCmd(),
	)
	return cmd
}

// ---- list ----

func newUserListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all users (direct DB — M20-safe)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()

			users, err := listUsersDirect(ctx)
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(map[string]interface{}{
					"users": users,
					"total": len(users),
				})
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tEMAIL\tUSERNAME\tNAME\tROLE\tKRATOS\tCREATED")
			for _, u := range users {
				role := "user"
				if u.IsAdmin {
					role = "admin"
				}
				username := "—"
				if u.Username != nil && *u.Username != "" {
					username = *u.Username
				}
				name := strings.TrimSpace(u.NameFirst + " " + u.NameLast)
				if name == "" {
					name = "—"
				}
				kratos := "—"
				if u.KratosIdentityID != nil && *u.KratosIdentityID != "" {
					kratos = "✓"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					u.ID, u.Email, username, name, role, kratos, u.CreatedAt.Format(time.DateOnly))
			}
			return w.Flush()
		},
	}
}

// ---- create ----

func newUserCreateCmd() *cobra.Command {
	var (
		username  string
		email     string
		password  string
		viaStdin  bool
		nameFirst string
		nameLast  string
		isAdmin   bool
	)

	cmd := &cobra.Command{
		Use:   "create [username]",
		Short: "Create a new user (direct DB + Kratos; bypasses HTTP auth — M20-safe)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A bare positional is the username, matching `user delete <…>`.
			if len(args) == 1 && username == "" {
				username = args[0]
			}
			// 30s timeout covers the worst-case path: Kratos create (loopback,
			// sub-100ms) + agent user.create (adduser + home-dir perms, low
			// seconds). Generous so a cold Kratos doesn't spuriously fail.
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()

			// Resolve password — same precedence as `user password`:
			// explicit --password wins; --password-stdin reads one line
			// from stdin (for automation pipes); both empty falls
			// through to createUserDirect's "required" check.
			if password != "" && viaStdin {
				return fmt.Errorf("--password and --password-stdin are mutually exclusive")
			}
			if viaStdin {
				p, err := readPasswordStdin()
				if err != nil {
					return err
				}
				password = p
			}

			u, warn, err := createUserDirect(ctx, cliUserInput{
				Username:  username,
				Email:     email,
				Password:  password,
				NameFirst: nameFirst,
				NameLast:  nameLast,
				IsAdmin:   isAdmin,
			})
			if err != nil {
				return err
			}

			if jsonOutput {
				out := map[string]interface{}{"user": u}
				if warn != "" {
					out["warning"] = warn
				}
				return printJSON(out)
			}
			fmt.Printf("Created user %s (%s)\n", u.ID, u.Email)
			if u.KratosIdentityID != nil {
				fmt.Printf("Kratos identity: %s\n", *u.KratosIdentityID)
			}
			if warn != "" {
				fmt.Fprintf(os.Stderr, "warning: %s\n", warn)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "login username — the identifier users sign in with (required unless --email is given to derive one)")
	cmd.Flags().StringVar(&email, "email", "", "user email (optional; a placeholder is synthesized from the username if omitted)")
	cmd.Flags().StringVar(&password, "password", "", "user password (required, min 10 chars)")
	cmd.Flags().BoolVar(&viaStdin, "password-stdin", false, "read password from stdin (no prompt, no echo)")
	cmd.Flags().StringVar(&nameFirst, "name-first", "", "first name")
	cmd.Flags().StringVar(&nameLast, "name-last", "", "last name")
	cmd.Flags().BoolVar(&isAdmin, "admin", false, "grant admin role")

	return cmd
}

// ---- delete ----
//
// user edit + user login are intentionally absent. edit is rare + easier
// from the web UI; login was M5b-era (JWT cli_token flow, removed). For
// recovery, use `curl -X POST http://127.0.0.1:4434/admin/recovery/code` —
// runbook documents the full flow.

func newUserDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <email|username|user-id>",
		Short: "Delete a user — destructive: domains, databases, mailboxes, OS account, /home, all related rows.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			lookup := strings.TrimSpace(args[0])
			if lookup == "" {
				return fmt.Errorf("user identifier is required")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			if err := initConfig(); err != nil {
				return err
			}
			if err := initDB(); err != nil {
				return err
			}
			target, err := resolveUser(ctx, lookup)
			if err != nil {
				return err
			}
			userID := target.ID

			if !force {
				home := "/home/" + strOr(target.Username, "<no-home>")
				msg := fmt.Sprintf("Delete user %s (%s)? This will permanently remove %s, all owned domains, databases, mailboxes, cron jobs, and the OS account.",
					target.ID, target.Email, home)
				fmt.Print(msg + " [y/N]: ")
				var confirm string
				fmt.Scanln(&confirm)
				if confirm != "y" && confirm != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			if err := deleteUserDirect(ctx, userID, true); err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(map[string]string{"deleted": userID})
			}
			fmt.Printf("Deleted user %s (%s) — /home + DB + mail + OS account removed.\n", target.ID, target.Email)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation prompt")

	return cmd
}

// ---- password ----
//
// `jabali user password <email|username|user-id>` resets the password on the
// user's Kratos identity. Works for both admins and regular users — the panel
// users table has no separate admin-password path since M20. Safe to run on a
// host with no live user session: this talks to the Kratos admin API directly
// (unix socket via sharedCfg) and doesn't need an HTTP self-service flow.
//
// Password resolution order:
//   --password <pwd>    explicit value (visible in shell history; convenience
//                       for one-shot operator use, mirrors `mailbox create
//                       --password`)
//   --password-stdin    piped password, no prompt — for automation
//   --link              emit a Kratos recovery URL; user picks their own
//   (none of the above) auto-generate a strong password (ULID, 26 chars) and
//                       print it once on stdout — mirrors `mailbox create`
//                       and `mailbox rotate-password` behavior
//
// The legacy TTY twice-prompt was removed — auto-generate is friendlier for
// the common operator case ("reset this user's password and tell me what it
// is") and matches the mailbox CLI affordance the user already knows.

func newUserPasswordCmd() *cobra.Command {
	var (
		password string
		viaStdin bool
		viaLink  bool
		ttl      string
	)

	cmd := &cobra.Command{
		Use:     "password <email|username|user-id>",
		Short:   "Reset a user's password (auto-generates one if --password is omitted)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			lookup := strings.TrimSpace(args[0])
			if lookup == "" {
				return fmt.Errorf("user identifier is required")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()

			target, err := resolveUser(ctx, lookup)
			if err != nil {
				return err
			}
			if target.KratosIdentityID == nil || *target.KratosIdentityID == "" {
				return fmt.Errorf("user %s (%s) has no kratos_identity_id — cannot reset password (run `jabali admin rebuild-kratos` to provision)", target.ID, target.Email)
			}

			kratosCfg := sharedCfg.Auth.Kratos
			if kratosCfg.AdminURL == "" {
				return fmt.Errorf("auth.kratos.admin_url not configured — cannot reset password without admin API access")
			}
			kc := kratosclient.NewClient(kratosCfg.PublicURL, kratosCfg.AdminURL)

			if viaLink {
				rc, err := kc.CreateRecoveryCode(ctx, *target.KratosIdentityID, ttl)
				if err != nil {
					return fmt.Errorf("generate recovery link: %w", err)
				}
				if jsonOutput {
					return printJSON(map[string]string{
						"user_id":       target.ID,
						"email":         target.Email,
						"recovery_link": rc.RecoveryLink,
						"recovery_code": rc.RecoveryCode,
						"expires_at":    rc.ExpiresAt,
					})
				}
				fmt.Printf("Recovery link for %s (%s):\n", target.ID, target.Email)
				fmt.Printf("  URL:        %s\n", rc.RecoveryLink)
				fmt.Printf("  Code:       %s\n", rc.RecoveryCode)
				fmt.Printf("  Expires at: %s\n", rc.ExpiresAt)
				fmt.Println("\nSend this link to the user — they'll pick their own password.")
				return nil
			}

			// Resolve the new password. Explicit --password wins; --password-stdin
			// reads one line; otherwise auto-generate. Keep the conflict check
			// strict — silently ignoring one of two contradictory flags hides
			// scripting bugs.
			if password != "" && viaStdin {
				return fmt.Errorf("--password and --password-stdin are mutually exclusive")
			}

			generated := ""
			newPwd := password
			switch {
			case viaStdin:
				p, err := readPasswordStdin()
				if err != nil {
					return err
				}
				newPwd = p
			case newPwd == "":
				newPwd = ids.NewULID()
				generated = newPwd
			}
			if len(newPwd) < 10 {
				return fmt.Errorf("password must be at least 10 characters")
			}

			hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), 12)
			if err != nil {
				return fmt.Errorf("bcrypt hash: %w", err)
			}
			// Heal M54-schema drift before setting the password. The v2 schema
			// requires traits.username; Kratos re-validates the FULL identity on
			// ANY update, so a password-only JSON-patch 400s ("missing username")
			// on an identity whose username trait was never backfilled. Re-assert
			// it first (idempotent `add`) so the identity is valid, then the
			// password patch passes. Skips admins (no linux username).
			if target.Username != nil && *target.Username != "" {
				if err := kc.UpdateUsernameTrait(ctx, *target.KratosIdentityID, *target.Username); err != nil {
					fmt.Fprintln(os.Stderr, "warning: could not assert Kratos username trait (password set may fail if the trait was missing):", err)
				}
			}
			if err := kc.SetPassword(ctx, *target.KratosIdentityID, string(hash)); err != nil {
				return fmt.Errorf("kratos set password: %w", err)
			}

			// Mirror to the panel DB row so the bcrypt-hash column matches
			// Kratos. Login is via Kratos but several legacy paths
			// (BootstrapAdmin idempotency check, password_hash audit columns)
			// still read the DB hash.
			if err := userRepo().Update(ctx, &models.User{ID: target.ID, Email: target.Email, PasswordHash: string(hash)}); err != nil {
				fmt.Fprintln(os.Stderr, "warning: kratos updated but DB hash sync failed:", err)
			}

			// Sync to the OS account for non-admin users so SSH / SFTP /
			// chage all see the new password too. The agent's user.password
			// command runs `chpasswd <user>:<pass>` as root.
			if !target.IsAdmin && target.Username != nil && *target.Username != "" {
				if sharedAgent != nil {
					if _, agentErr := sharedAgent.Call(ctx, "user.password", map[string]any{
						"username": *target.Username,
						"password": newPwd,
					}); agentErr != nil {
						fmt.Fprintln(os.Stderr, "warning: kratos updated but OS password sync failed:", agentErr)
					}
				} else {
					fmt.Fprintln(os.Stderr, "warning: agent client unavailable, OS password not synced")
				}
			}

			if jsonOutput {
				out := map[string]string{
					"user_id": target.ID,
					"email":   target.Email,
					"kratos":  *target.KratosIdentityID,
					"status":  "password_reset",
				}
				if generated != "" {
					out["password"] = generated
				}
				return printJSON(out)
			}
			if generated != "" {
				fmt.Printf("Password reset for %s (%s)\n", target.ID, target.Email)
				fmt.Printf("New password: %s\n", generated)
				fmt.Fprintln(os.Stderr, "(shown once — record it now)")
			} else {
				fmt.Printf("Password reset for %s (%s)\n", target.ID, target.Email)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&password, "password", "", "explicit new password (omit to auto-generate)")
	cmd.Flags().BoolVar(&viaStdin, "password-stdin", false, "read new password from stdin (no prompt, no echo)")
	cmd.Flags().BoolVar(&viaLink, "link", false, "emit a one-click recovery URL instead of setting the password directly")
	cmd.Flags().StringVar(&ttl, "expires-in", "24h", "TTL for recovery link (only with --link)")
	return cmd
}

// resolveUser accepts email, username, or user UUID — tries each in the order
// most likely to be unambiguous. Email is primary; username is unique per M20;
// UUID is the ID column lookup. The first hit wins.
func resolveUser(ctx context.Context, lookup string) (*models.User, error) {
	users := userRepo()

	if strings.Contains(lookup, "@") {
		u, err := users.FindByEmail(ctx, lookup)
		if err == nil {
			return u, nil
		}
		if !errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("lookup by email: %w", err)
		}
	}
	if u, err := users.FindByUsername(ctx, lookup); err == nil {
		return u, nil
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("lookup by username: %w", err)
	}
	if u, err := users.FindByID(ctx, lookup); err == nil {
		return u, nil
	} else if !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("lookup by id: %w", err)
	}
	return nil, fmt.Errorf("no user found matching %q", lookup)
}

// readPasswordStdin reads a single line from stdin, stripping the trailing
// newline. Used by the --password-stdin path; explicit --password and
// auto-generate paths bypass this.
func readPasswordStdin() (string, error) {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// strOr returns *s when non-nil + non-empty, else fallback. Tiny helper so
// the confirmation prompt doesn't render "<nil>" for admins without a
// username.
func strOr(s *string, fallback string) string {
	if s == nil || *s == "" {
		return fallback
	}
	return *s
}

// ---- 2fa reset ----

// newUser2FAResetCmd is the operator break-glass for a user whose
// authenticator is lost AND recovery codes are exhausted. Strips
// totp + lookup_secret credentials from their Kratos identity. The
// password stays intact; on next sign-in the user is back at aal1
// and can re-enrol from the profile page.
//
// Mirrors the panel-side admin REST endpoint
// (POST /admin/users/:id/2fa/reset) but works without a logged-in
// admin session — the use case is "every admin is locked out, ssh
// to host, run this".
func newUser2FAResetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "2fa-reset <email|username|user-id>",
		Short:   "Strip TOTP + recovery codes from a user (CLI escape hatch when locked out)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			lookup := strings.TrimSpace(args[0])
			if lookup == "" {
				return fmt.Errorf("user identifier is required")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			target, err := resolveUser(ctx, lookup)
			if err != nil {
				return err
			}
			if target.KratosIdentityID == nil || *target.KratosIdentityID == "" {
				return fmt.Errorf("user %s (%s) has no kratos_identity_id — run `jabali admin rebuild-kratos` to provision one before retrying", target.ID, target.Email)
			}

			kratosCfg := sharedCfg.Auth.Kratos
			if kratosCfg.AdminURL == "" {
				return fmt.Errorf("auth.kratos.admin_url not configured — cannot reach Kratos admin API")
			}
			kc := kratosclient.NewClient(kratosCfg.PublicURL, kratosCfg.AdminURL)

			if err := kc.RemoveSecondFactor(ctx, *target.KratosIdentityID); err != nil {
				if errors.Is(err, kratosclient.ErrIdentityNotFound) {
					return fmt.Errorf("kratos identity %s not found — DB row may be orphaned", *target.KratosIdentityID)
				}
				return fmt.Errorf("kratos admin patch failed: %w", err)
			}

			fmt.Printf("Two-factor authentication reset for %s (%s).\n", target.ID, target.Email)
			fmt.Printf("Password unchanged. User can sign in normally and re-enrol from /profile.\n")
			return nil
		},
	}
}
