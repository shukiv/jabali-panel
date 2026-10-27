package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// newAdminBackfillUsernamesCmd implements the M54 username-login backfill
// (Wave A). Username is becoming the Kratos login identifier (required +
// unique), but today admins may have a NULL username. This assigns a unique
// username to every user that lacks one, derived from the email local-part, so
// the later NOT-NULL migration (Wave B) + Kratos re-key (Wave C) have a
// username for every identity.
//
// DB-only and idempotent: it fills NULL/empty usernames and never touches a
// user that already has one. The matching Kratos identity trait is synced by
// `admin relabel-identifiers` (Wave C). Always --dry-run first; --apply writes.
func newAdminBackfillUsernamesCmd() *cobra.Command {
	var apply bool
	cmd := &cobra.Command{
		Use:   "backfill-usernames",
		Short: "Assign a unique username to every user missing one",
		Long: `Assigns a unique username to every panel user whose username is NULL/empty,
derived from the email local-part (lowercased, non [a-z0-9_-] -> _, trimmed to
32, forced to start [a-z_]). Collisions get a -2/-3/... suffix.

Idempotent and DB-only. Dry-run by default; pass --apply to write. The Kratos
identity trait is synced separately by 'admin relabel-identifiers' (Wave C).`,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			// Existing usernames form the reserved set the derivation must avoid.
			var existing []string
			if err := sharedDB.WithContext(ctx).Model(&models.User{}).
				Where("username IS NOT NULL AND username <> ''").
				Pluck("username", &existing).Error; err != nil {
				return fmt.Errorf("load existing usernames: %w", err)
			}
			taken := make(map[string]bool, len(existing))
			for _, u := range existing {
				taken[strings.ToLower(u)] = true
			}

			// Users needing a username.
			var users []models.User
			if err := sharedDB.WithContext(ctx).
				Where("username IS NULL OR username = ''").
				Order("created_at").Find(&users).Error; err != nil {
				return fmt.Errorf("load users missing username: %w", err)
			}
			if len(users) == 0 {
				fmt.Println("All users already have a username. Nothing to backfill.")
				return nil
			}

			type plan struct {
				id, email, username string
			}
			var planned []plan
			for i := range users {
				u := users[i]
				cand := uniqueUsername(deriveUsername(u.Email), taken)
				taken[cand] = true
				planned = append(planned, plan{id: u.ID, email: u.Email, username: cand})
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "USER ID\tEMAIL\t-> USERNAME")
			for _, p := range planned {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", p.id, p.email, p.username)
			}
			tw.Flush()

			if !apply {
				fmt.Printf("\n[dry-run] %d user(s) would get a username. Re-run with --apply to write.\n", len(planned))
				return nil
			}

			for _, p := range planned {
				if err := sharedDB.WithContext(ctx).Model(&models.User{}).
					Where("id = ?", p.id).
					Update("username", p.username).Error; err != nil {
					return fmt.Errorf("set username for %s (%s): %w", p.id, p.email, err)
				}
			}
			fmt.Printf("\nApplied: %d user(s) backfilled. Run 'admin relabel-identifiers' next (Wave C) to sync Kratos.\n", len(planned))
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Write the derived usernames to the DB")
	return cmd
}

var usernameInvalidChars = regexp.MustCompile(`[^a-z0-9_-]`)

// deriveUsername turns an email into a schema-valid username candidate:
// local-part, lowercased, non [a-z0-9_-] -> _, must start [a-z_], max 32.
func deriveUsername(email string) string {
	local := email
	if i := strings.IndexByte(email, '@'); i > 0 {
		local = email[:i]
	}
	local = usernameInvalidChars.ReplaceAllString(strings.ToLower(local), "_")
	local = strings.Trim(local, "-")
	if local == "" {
		local = "user"
	}
	if c := local[0]; !(c == '_' || (c >= 'a' && c <= 'z')) {
		local = "u" + local
	}
	if len(local) > 32 {
		local = local[:32]
	}
	return local
}

// uniqueUsername appends -2/-3/... until the candidate is free in taken,
// keeping within the 32-char cap.
func uniqueUsername(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for n := 2; ; n++ {
		suffix := fmt.Sprintf("-%d", n)
		trunc := base
		if len(trunc)+len(suffix) > 32 {
			trunc = trunc[:32-len(suffix)]
		}
		cand := trunc + suffix
		if !taken[cand] {
			return cand
		}
	}
}
