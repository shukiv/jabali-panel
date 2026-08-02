// Admin impersonation grant CLI (Gitea #572). Operator-side mirror of the
// /admin/impersonation REST surface (api/impersonation.go): list active grants,
// create a grant (admin acts-as a target user), and end a grant.
//
// Faithful to the handler's grant model + ADR-0128 rules: same 60-minute
// server-side TTL, same validation (target must exist, can't be self, can't be
// an admin), same "active = ended_at IS NULL AND expires_at > now" semantics.
// Pure DB row — no agent, no cookie (this only mints the server-side grant the
// browser act-as flow consumes; it does NOT swap any Kratos session). Mutations
// CLI-audited with the impersonated target as subject user (#537), so CLI-source
// grants are distinguishable from browser-source ones.
//
// User-approved (2026-06-30) operator parity for an existing audited endpoint —
// NOT the rejected root-shell-mints-its-own-panel-session pattern.
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// impersonationGrantTTL mirrors the REST handler constant (ADR-0128).
const cliImpersonationGrantTTL = 60 * time.Minute

func impersonationRepoFromDB() repository.ImpersonationGrantRepository {
	return repository.NewImpersonationGrantRepository(sharedDB)
}

func newImpersonationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "impersonation",
		Aliases: []string{"impersonate"},
		Short:   "Manage admin act-as impersonation grants (list / create / end)",
	}
	cmd.AddCommand(newImpersonationListCmd(), newImpersonationCreateCmd(), newImpersonationEndCmd())
	return cmd
}

// userLabel renders a user as email (username) for grant output.
func userLabel(u *models.User) string {
	if u == nil {
		return "(unknown)"
	}
	if u.Username != nil && *u.Username != "" {
		return fmt.Sprintf("%s (%s)", u.Email, *u.Username)
	}
	return u.Email
}

func newImpersonationListCmd() *cobra.Command {
	var admin string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List active impersonation grants (all, or for one admin)",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			repo := impersonationRepoFromDB()
			now := time.Now().UTC()
			var grants []models.ImpersonationGrant
			var err error
			if admin != "" {
				au, aerr := resolveUser(ctx, admin)
				if aerr != nil {
					return aerr
				}
				grants, err = repo.ListActiveByAdmin(ctx, au.ID, now)
			} else {
				grants, err = repo.ListAllActive(ctx, now)
			}
			if err != nil {
				return fmt.Errorf("list grants: %w", err)
			}
			if jsonOutput {
				return printJSON(grants)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "GRANT_ID\tADMIN\tTARGET\tEXPIRES\tTTL_LEFT")
			for i := range grants {
				g := &grants[i]
				admU, _ := userRepo().FindByID(ctx, g.AdminUserID)
				tgtU, _ := userRepo().FindByID(ctx, g.TargetUserID)
				left := time.Until(g.ExpiresAt).Round(time.Second)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", g.ID, userLabel(admU), userLabel(tgtU), g.ExpiresAt.UTC().Format("2006-01-02 15:04Z"), left)
			}
			if len(grants) == 0 {
				fmt.Fprintln(w, "(no active grants)")
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&admin, "admin", "", "scope to one admin (email|username|id); default = all admins")
	return cmd
}

func newImpersonationCreateCmd() *cobra.Command {
	var admin, target string
	cmd := &cobra.Command{
		Use:     "create --admin <admin> --target <user>",
		Short:   "Create a 60-minute impersonation grant for an admin to act as a target user",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if admin == "" || target == "" {
				return fmt.Errorf("--admin and --target are both required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			au, err := resolveUser(ctx, admin)
			if err != nil {
				return fmt.Errorf("admin: %w", err)
			}
			if !au.IsAdmin {
				return fmt.Errorf("%s is not an admin — only admins can hold a grant", userLabel(au))
			}
			tu, err := resolveUser(ctx, target)
			if err != nil {
				return fmt.Errorf("target: %w", err)
			}
			if tu.ID == au.ID {
				return fmt.Errorf("cannot impersonate yourself")
			}
			if tu.IsAdmin {
				return fmt.Errorf("cannot impersonate an admin")
			}
			grant := &models.ImpersonationGrant{
				ID:           ids.NewULID(),
				AdminUserID:  au.ID,
				TargetUserID: tu.ID,
				ExpiresAt:    time.Now().UTC().Add(cliImpersonationGrantTTL),
			}
			if err := impersonationRepoFromDB().Create(ctx, grant); err != nil {
				cliAuditErr(ctx, "impersonation.create", "impersonation_grant", grant.ID, &tu.ID)
				return fmt.Errorf("create grant: %w", err)
			}
			cliAuditOK(ctx, "impersonation.create", "impersonation_grant", grant.ID, &tu.ID)
			if jsonOutput {
				return printJSON(grant)
			}
			fmt.Printf("Created grant %s\n  admin:   %s\n  target:  %s\n  expires: %s (60m)\n",
				grant.ID, userLabel(au), userLabel(tu), grant.ExpiresAt.UTC().Format("2006-01-02 15:04Z"))
			return nil
		},
	}
	cmd.Flags().StringVar(&admin, "admin", "", "admin who will act-as (email|username|id, required)")
	cmd.Flags().StringVar(&target, "target", "", "target user to impersonate (email|username|id, required)")
	return cmd
}

func newImpersonationEndCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "end <grant-id>",
		Short:   "End an active impersonation grant",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			repo := impersonationRepoFromDB()
			now := time.Now().UTC()
			grant, err := repo.FindActive(ctx, args[0], now)
			if err != nil {
				// Idempotent: already ended/expired/absent.
				fmt.Printf("grant %s is not active (already ended or expired)\n", args[0])
				return nil
			}
			if err := repo.End(ctx, grant.ID, now); err != nil {
				cliAuditErr(ctx, "impersonation.end", "impersonation_grant", grant.ID, &grant.TargetUserID)
				return fmt.Errorf("end grant: %w", err)
			}
			cliAuditOK(ctx, "impersonation.end", "impersonation_grant", grant.ID, &grant.TargetUserID)
			fmt.Printf("ended grant %s\n", grant.ID)
			return nil
		},
	}
}
