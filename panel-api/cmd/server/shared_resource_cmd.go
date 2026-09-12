// `jabali shared-resource` cobra subcommands — manage M52 (ADR-0133)
// standalone shared resources (calendar / address book / file folder /
// mailbox) and their grants from the CLI. Mirrors `jabali mailbox shares`:
// the CLI writes the shared_resources / shared_resource_grants tables and the
// reconciler converges to Stalwart host principals + per-collection shareWith
// on its next sweep.
//
//	jabali shared-resource list   --domain <domainID>
//	jabali shared-resource create --domain <domainID> --name team --kind calendar --display-name "Team Calendar"
//	jabali shared-resource grants --resource <id>
//	jabali shared-resource grant  --resource <id> --grantee-kind mailbox --grantee <id> --rights readwrite
//	jabali shared-resource revoke --resource <id> --grantee <id>
//	jabali shared-resource remove --resource <id>
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sharedresourceops"
)

func sharedResourceRepoFromDB() repository.SharedResourceRepository {
	return repository.NewSharedResourceRepository(sharedDB)
}

var sharedResourceRightSet = map[string]bool{"read": true, "readwrite": true, "admin": true}

func newSharedResourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shared-resource",
		Short: "Manage shared mail resources — calendars, contacts, files (M52)",
	}
	cmd.AddCommand(
		newSharedResourceListCmd(),
		newSharedResourceCreateCmd(),
		newSharedResourceGrantsCmd(),
		newSharedResourceGrantCmd(),
		newSharedResourceRevokeCmd(),
		newSharedResourceRemoveCmd(),
	)
	return cmd
}

func newSharedResourceListCmd() *cobra.Command {
	var domainID string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List shared resources in a domain",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if domainID == "" {
				return errors.New("--domain <domainID> required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			rows, err := sharedResourceRepoFromDB().ListByDomainID(ctx, domainID)
			if err != nil {
				return fmt.Errorf("list: %w", err)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKIND\tEMAIL\tDISPLAY\tHOST_ACCT")
			for _, r := range rows {
				email := ""
				if r.EmailCached != nil {
					email = *r.EmailCached
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Kind, email, r.DisplayName, r.HostAccountID)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&domainID, "domain", "", "domain ID (ULID)")
	return cmd
}

func newSharedResourceCreateCmd() *cobra.Command {
	var domainID, name, kind, displayName string
	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a shared resource",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if domainID == "" || name == "" || kind == "" {
				return errors.New("--domain, --name, --kind required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			dom, err := domainRepoFromDB().FindByID(ctx, domainID)
			if err != nil {
				return fmt.Errorf("find domain %q: %w", domainID, err)
			}
			// The email-enabled gate, kind allowlist, canonicalisation,
			// duplicate check, trimmed display name, persist, and best-effort
			// apply are all in sharedresourceops — the same policy the REST
			// handler runs, so the two adapters project identical state.
			sr, err := createSharedResourceDirect(ctx, sharedResourceRepoFromDB(),
				notifyAgentSharedResource, dom, kind, name, displayName)
			if err != nil {
				return err
			}
			cliAuditOK(ctx, "shared_resource.create", "shared_resource", sr.ID, nil)
			email := ""
			if sr.EmailCached != nil {
				email = *sr.EmailCached
			}
			fmt.Printf("created %s (%s) %s — converges on next reconcile pass\n", sr.ID, kind, email)
			return nil
		},
	}
	cmd.Flags().StringVar(&domainID, "domain", "", "domain ID (ULID)")
	cmd.Flags().StringVar(&name, "name", "", "host address local part")
	cmd.Flags().StringVar(&kind, "kind", "", "mailbox|calendar|addressbook|files")
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name")
	return cmd
}

func newSharedResourceGrantsCmd() *cobra.Command {
	var resourceID string
	cmd := &cobra.Command{
		Use:     "grants",
		Short:   "List grants on a shared resource",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if resourceID == "" {
				return errors.New("--resource <id> required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			grants, err := sharedResourceRepoFromDB().ListGrants(ctx, resourceID)
			if err != nil {
				return fmt.Errorf("list grants: %w", err)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "GRANTEE_KIND\tGRANTEE_ID\tRIGHTS")
			for _, g := range grants {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", g.GranteeKind, g.GranteeID, g.Rights)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&resourceID, "resource", "", "shared resource ID")
	return cmd
}

func newSharedResourceGrantCmd() *cobra.Command {
	var resourceID, granteeKind, granteeID, rights string
	cmd := &cobra.Command{
		Use:     "grant",
		Short:   "Add or update a grant (upsert into the grant set)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if resourceID == "" || granteeID == "" {
				return errors.New("--resource and --grantee required")
			}
			if granteeKind != "mailbox" && granteeKind != "group" {
				return errors.New("--grantee-kind must be mailbox|group")
			}
			if !sharedResourceRightSet[rights] {
				return errors.New("--rights must be read|readwrite|admin")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			// Load the resource and its domain first: the domain's UserID is the
			// resource owner the same-owner domain policy compares against (a bad
			// --resource now fails "shared resource not found" instead of an
			// empty ListGrants later).
			repo := sharedResourceRepoFromDB()
			sr, err := repo.FindByID(ctx, resourceID)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("shared resource not found: %s", resourceID)
				}
				return fmt.Errorf("load shared resource: %w", err)
			}
			dom, err := domainRepoFromDB().FindByID(ctx, sr.DomainID)
			if err != nil {
				return fmt.Errorf("load resource domain: %w", err)
			}
			// JAB-339 AC4: reject a grant to a non-existent grantee, and enforce
			// the same-owner domain policy (the grantee must belong to the
			// resource owner, dom.UserID), before any write — the shared owner
			// with the REST handler. The inline flag checks above already caught
			// a bad kind / empty id with a flag-specific message, so only the
			// existence + owner-scope checks fire here.
			grantDeps := sharedresourceops.Deps{
				Mailboxes:  mailboxRepoFromDB(),
				MailGroups: repository.NewMailGroupRepository(sharedDB),
				Domains:    domainRepoFromDB(),
			}
			if err := sharedresourceops.ValidateGrants(ctx, grantDeps, dom.UserID, []models.SharedResourceGrant{
				{ResourceID: resourceID, GranteeKind: granteeKind, GranteeID: granteeID, Rights: rights},
			}); err != nil {
				if errors.Is(err, sharedresourceops.ErrGranteeNotFound) {
					return fmt.Errorf("grantee not found: %s %s", granteeKind, granteeID)
				}
				return fmt.Errorf("validate grantee: %w", err)
			}
			grants, err := repo.ListGrants(ctx, resourceID)
			if err != nil {
				return fmt.Errorf("list grants: %w", err)
			}
			next := make([]models.SharedResourceGrant, 0, len(grants)+1)
			for _, g := range grants {
				if g.GranteeKind == granteeKind && g.GranteeID == granteeID {
					continue // replaced below
				}
				next = append(next, g)
			}
			next = append(next, models.SharedResourceGrant{
				ResourceID: resourceID, GranteeKind: granteeKind, GranteeID: granteeID, Rights: rights,
			})
			if err := repo.ReplaceGrants(ctx, resourceID, next); err != nil {
				return fmt.Errorf("replace grants: %w", err)
			}
			fmt.Printf("granted %s %s = %s on %s\n", granteeKind, granteeID, rights, resourceID)
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceID, "resource", "", "shared resource ID")
	cmd.Flags().StringVar(&granteeKind, "grantee-kind", "mailbox", "mailbox|group")
	cmd.Flags().StringVar(&granteeID, "grantee", "", "grantee mailbox/group ID")
	cmd.Flags().StringVar(&rights, "rights", "read", "read|readwrite|admin")
	return cmd
}

func newSharedResourceRevokeCmd() *cobra.Command {
	var resourceID, granteeID string
	cmd := &cobra.Command{
		Use:     "revoke",
		Short:   "Remove a grantee's grant from a shared resource",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if resourceID == "" || granteeID == "" {
				return errors.New("--resource and --grantee required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := sharedResourceRepoFromDB()
			grants, err := repo.ListGrants(ctx, resourceID)
			if err != nil {
				return fmt.Errorf("list grants: %w", err)
			}
			next := make([]models.SharedResourceGrant, 0, len(grants))
			removed := false
			for _, g := range grants {
				if g.GranteeID == granteeID {
					removed = true
					continue
				}
				next = append(next, g)
			}
			if !removed {
				return fmt.Errorf("no grant for grantee %q", granteeID)
			}
			if err := repo.ReplaceGrants(ctx, resourceID, next); err != nil {
				return fmt.Errorf("replace grants: %w", err)
			}
			cliAuditOK(ctx, "shared_resource.revoke", "shared_resource", resourceID, nil)
			fmt.Printf("revoked %s on %s\n", granteeID, resourceID)
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceID, "resource", "", "shared resource ID")
	cmd.Flags().StringVar(&granteeID, "grantee", "", "grantee ID to revoke")
	return cmd
}

func newSharedResourceRemoveCmd() *cobra.Command {
	var resourceID string
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Delete a shared resource (reconciler tears down the host principal)",
		// requireDBAndAgent, not requireDB: requireDB never calls initAgent, so
		// sharedAgent stays nil and the instant host destroy never fires (the
		// reconciler still converges from the tombstone, just not instantly).
		// initAgent only constructs the client — a down socket does not fail the
		// command — so this is safe. Same fix the create subcommand carries.
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if resourceID == "" {
				return errors.New("--resource <id> required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			// Best-effort host teardown that SURFACES a failure — not the shared
			// notifyAgentSharedResource, which swallows. An operator running a
			// manual remove should see a destroy that did not land; the
			// reconciler GC still retries from the durable tombstone.
			notify := func(nctx context.Context, agentCmd string, params any) {
				if sharedAgent == nil {
					return
				}
				agentCtx, acancel := context.WithTimeout(nctx, cliSharedResourceAgentTimeout)
				defer acancel()
				if _, derr := sharedAgent.Call(agentCtx, agentCmd, params); derr != nil {
					fmt.Fprintf(os.Stderr, "warning: host teardown failed (%v); reconciler will retry\n", derr)
				}
			}
			if err := deleteSharedResourceDirect(ctx, sharedResourceRepoFromDB(), notify, resourceID); err != nil {
				return fmt.Errorf("delete: %w", err)
			}
			cliAuditOK(ctx, "shared_resource.remove", "shared_resource", resourceID, nil)
			fmt.Printf("removed %s\n", resourceID)
			return nil
		},
	}
	cmd.Flags().StringVar(&resourceID, "resource", "", "shared resource ID")
	return cmd
}
