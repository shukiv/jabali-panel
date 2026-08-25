// Mail-group lifecycle + membership CLI (Gitea #591). Mirrors the REST
// mailgroups handler (api/mailgroups.go) faithfully:
//
//   - create: same mailaddr.Canonicalise, same group/mailbox address-collision
//     checks, same normalizeGroupKind, same `mailgroup.apply` projection.
//   - set-members / add / remove: resolve mailboxes, enforce same-domain, and
//     project via `mailgroup.members_set` ONLY for resource groups (distribution
//     groups fan out through the SQL directory, not memberGroupIds).
//   - delete: agent-FIRST (`mailgroup.delete` strips the Stalwart principal)
//     then the DB delete, matching the handler's ordering so rows never drift
//     ahead of Stalwart.
//
// Mail groups are push-projected (no reconciler tick converges them — see the
// mailgroup.apply note in shared_resource_reconcile.go), so the agent calls are
// load-bearing, not an immediacy optimization. Mutations CLI-audited (#537).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const cliMailGroupAgentTimeout = 30 * time.Second

func mailGroupRepoFromDB() repository.MailGroupRepository {
	return repository.NewMailGroupRepository(sharedDB)
}

// normalizeGroupKind mirrors the api package helper: blank/unknown → "resource",
// only "distribution" and "resource" are valid.
func normalizeGroupKind(k string) string {
	if strings.TrimSpace(k) == "distribution" {
		return "distribution"
	}
	return "resource"
}

func newMailGroupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mail-group",
		Short: "Manage mail groups (distribution lists + shared-resource groups) and members",
	}
	cmd.AddCommand(
		newMailGroupListCmd(),
		newMailGroupShowCmd(),
		newMailGroupCreateCmd(),
		newMailGroupSetResourcesCmd(),
		newMailGroupSetMembersCmd(),
		newMailGroupAddMemberCmd(),
		newMailGroupRemoveMemberCmd(),
		newMailGroupDeleteCmd(),
	)
	return cmd
}

// resolveMailGroupCLI finds a group by ULID, then by <local>@<domain> email.
func resolveMailGroupCLI(ctx context.Context, ref string) (*models.MailGroup, error) {
	repo := mailGroupRepoFromDB()
	if g, err := repo.FindByID(ctx, ref); err == nil && g != nil {
		return g, nil
	}
	// Email form: resolve the domain, then match local part.
	at := strings.LastIndex(ref, "@")
	if at <= 0 {
		return nil, fmt.Errorf("no mail group with id or email %q", ref)
	}
	canonLocal, _, err := mailaddr.Canonicalise(ref)
	if err != nil {
		return nil, fmt.Errorf("invalid group address %q: %w", ref, err)
	}
	dom, err := resolveDomainCLI(ctx, ref[at+1:])
	if err != nil {
		return nil, err
	}
	rows, err := repo.ListByDomainID(ctx, dom.ID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].LocalPart == canonLocal {
			g := rows[i].MailGroup
			return &g, nil
		}
	}
	return nil, fmt.Errorf("no mail group %q in %s", canonLocal, dom.Name)
}

func newMailGroupListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list [domain-name|domain-id]",
		Short:   "List mail groups (all, or scoped to one domain)",
		Args:    cobra.MaximumNArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			repo := mailGroupRepoFromDB()
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if len(args) == 1 {
				dom, err := resolveDomainCLI(ctx, args[0])
				if err != nil {
					return err
				}
				rows, err := repo.ListByDomainID(ctx, dom.ID)
				if err != nil {
					return fmt.Errorf("list mail groups: %w", err)
				}
				if jsonOutput {
					return printJSON(rows)
				}
				fmt.Fprintln(w, "EMAIL\tKIND\tDISPLAY_NAME\tMEMBERS")
				for _, r := range rows {
					fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", r.EmailCached, r.GroupKind, r.DisplayName, r.MemberCount)
				}
				return w.Flush()
			}
			rows, err := repo.ListAllWithDomain(ctx)
			if err != nil {
				return fmt.Errorf("list mail groups: %w", err)
			}
			if jsonOutput {
				return printJSON(rows)
			}
			fmt.Fprintln(w, "EMAIL\tKIND\tDOMAIN\tDISPLAY_NAME\tMEMBERS")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", r.EmailCached, r.GroupKind, r.DomainName, r.DisplayName, r.MemberCount)
			}
			return w.Flush()
		},
	}
}

func newMailGroupShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <group-email|group-id>",
		Short:   "Show a mail group and its members",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			g, err := resolveMailGroupCLI(ctx, args[0])
			if err != nil {
				return err
			}
			emails, err := mailGroupRepoFromDB().ListMemberEmails(ctx, g.ID)
			if err != nil {
				return fmt.Errorf("list members: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"group": g, "members": emails})
			}
			fmt.Printf("email:        %s\nkind:         %s\ndisplay_name: %s\ndescription:  %s\nresources:    mailbox=%v calendar=%v addressbook=%v files=%v\nmembers (%d):\n",
				g.EmailCached, g.GroupKind, g.DisplayName, g.Description,
				g.HasMailbox, g.HasCalendar, g.HasAddressbook, g.HasFiles, len(emails))
			for _, e := range emails {
				fmt.Printf("  - %s\n", e)
			}
			return nil
		},
	}
}

func newMailGroupCreateCmd() *cobra.Command {
	var displayName, description, kind string
	var internalOnly bool
	cmd := &cobra.Command{
		Use:     "create <domain-name|domain-id> <local-part>",
		Short:   "Create a mail group on a domain",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), cliMailGroupAgentTimeout)
			defer cancel()
			dom, err := resolveDomainCLI(ctx, args[0])
			if err != nil {
				return err
			}
			if !dom.EmailEnabled {
				return fmt.Errorf("email is not enabled on %s — enable it before creating groups", dom.Name)
			}
			canonLocal, _, err := mailaddr.Canonicalise(args[1] + "@" + dom.Name)
			if err != nil {
				return fmt.Errorf("invalid local part: %w", err)
			}
			grepo := mailGroupRepoFromDB()
			mrepo := mailboxRepoFromDB()
			if exists, err := grepo.ExistsByDomainAndLocalPart(ctx, dom.ID, canonLocal); err != nil {
				return err
			} else if exists {
				return fmt.Errorf("a group with this address already exists")
			}
			if exists, err := mrepo.ExistsByDomainAndLocalPart(ctx, dom.ID, canonLocal); err != nil {
				return err
			} else if exists {
				return fmt.Errorf("a mailbox already uses this address")
			}
			now := time.Now().UTC()
			g := &models.MailGroup{
				ID:             ids.NewULID(),
				DomainID:       dom.ID,
				LocalPart:      canonLocal,
				DisplayName:    strings.TrimSpace(displayName),
				Description:    strings.TrimSpace(description),
				GroupKind:      normalizeGroupKind(kind),
				HasMailbox:     true,
				HasCalendar:    true,
				HasAddressbook: true,
				HasFiles:       true,
				InternalOnly:   internalOnly,
				CreatedAt:      now,
				UpdatedAt:      now,
			}
			if err := grepo.Create(ctx, g); err != nil {
				cliAuditErr(ctx, "mail_group.create", "mail_group", g.ID, nil)
				return fmt.Errorf("create mail group: %w", err)
			}
			email := canonLocal + "@" + dom.Name
			g.EmailCached = email
			notifyAgentMailGroup(ctx, "mailgroup.apply", map[string]any{
				"email":         email,
				"display_name":  g.DisplayName,
				"description":   g.Description,
				"internal_only": g.InternalOnly,
				"has_files":     g.HasFiles,
			})
			cliAuditOK(ctx, "mail_group.create", "mail_group", g.ID, nil)
			fmt.Printf("created %s mail group %s\n", g.GroupKind, email)
			return nil
		},
	}
	cmd.Flags().StringVar(&displayName, "display-name", "", "display name (the From name)")
	cmd.Flags().StringVar(&description, "description", "", "description")
	cmd.Flags().StringVar(&kind, "kind", "resource", "group kind: resource | distribution")
	cmd.Flags().BoolVar(&internalOnly, "internal-only", false, "reject mail from senders outside the group's domain (GH #348)")
	return cmd
}

func newMailGroupSetResourcesCmd() *cobra.Command {
	var mailbox, calendar, addressbook, files string
	cmd := &cobra.Command{
		Use:     "set-resources <group-email|group-id>",
		Short:   "Toggle a resource group's shared collections (mailbox/calendar/addressbook/files)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			g, err := resolveMailGroupCLI(ctx, args[0])
			if err != nil {
				return err
			}
			mbx := boolOrFlag(mailbox, g.HasMailbox)
			cal := boolOrFlag(calendar, g.HasCalendar)
			ab := boolOrFlag(addressbook, g.HasAddressbook)
			fl := boolOrFlag(files, g.HasFiles)
			if err := mailGroupRepoFromDB().UpdateResources(ctx, g.ID, mbx, cal, ab, fl); err != nil {
				cliAuditErr(ctx, "mail_group.set_resources", "mail_group", g.ID, nil)
				return fmt.Errorf("update resources: %w", err)
			}
			g.HasMailbox, g.HasCalendar, g.HasAddressbook, g.HasFiles = mbx, cal, ab, fl
			// JAB-321: dispatch the SAME mailgroup.apply the REST update handler
			// runs after a resource change (internal/api/mailgroups.go), so a
			// CLI-enabled files/calendar/addressbook node is actually projected
			// into Stalwart. Previously set-resources wrote only the DB flags and
			// no reconciler re-asserts a group's resource nodes (apply is
			// push-only, per shared_resource_reconcile.go), so the CLI could
			// report resources that never existed on the mail server.
			notifyAgentMailGroup(ctx, "mailgroup.apply", map[string]any{
				"email":         g.EmailCached,
				"display_name":  g.DisplayName,
				"description":   g.Description,
				"internal_only": g.InternalOnly,
				"has_files":     g.HasFiles,
			})
			cliAuditOK(ctx, "mail_group.set_resources", "mail_group", g.ID, nil)
			fmt.Printf("%s resources: mailbox=%v calendar=%v addressbook=%v files=%v\n", g.EmailCached, mbx, cal, ab, fl)
			return nil
		},
	}
	cmd.Flags().StringVar(&mailbox, "mailbox", "", "shared mailbox (true|false)")
	cmd.Flags().StringVar(&calendar, "calendar", "", "shared calendar (true|false)")
	cmd.Flags().StringVar(&addressbook, "addressbook", "", "shared addressbook (true|false)")
	cmd.Flags().StringVar(&files, "files", "", "shared files (true|false)")
	return cmd
}

// boolOrFlag parses an optional bool flag string, defaulting to cur when empty.
func boolOrFlag(raw string, cur bool) bool {
	if raw == "" {
		return cur
	}
	v, err := parseBool(raw)
	if err != nil {
		return cur
	}
	return v
}

// resolveMembers turns member refs (mailbox email or ULID) into ids + emails,
// enforcing same-domain membership exactly like the REST setMembers handler.
func resolveMembers(ctx context.Context, domainID string, refs []string) (ids []string, emails []string, err error) {
	mrepo := mailboxRepoFromDB()
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		var mb *models.Mailbox
		if m, e := mrepo.FindByID(ctx, ref); e == nil && m != nil {
			mb = m
		} else if m, e := mrepo.FindByEmail(ctx, ref); e == nil && m != nil {
			mb = m
		}
		if mb == nil {
			return nil, nil, fmt.Errorf("mailbox %q not found", ref)
		}
		if mb.DomainID != domainID {
			return nil, nil, fmt.Errorf("mailbox %s is not in this group's domain", mb.EmailCached)
		}
		ids = append(ids, mb.ID)
		emails = append(emails, mb.EmailCached)
	}
	return ids, emails, nil
}

// projectMembers replaces the member set and projects to Stalwart, mirroring
// the handler: distribution groups fan out via the SQL directory (no
// memberGroupIds projection), resource groups push members_set.
func projectMembers(ctx context.Context, g *models.MailGroup, desiredIDs, desiredEmails []string) error {
	repo := mailGroupRepoFromDB()
	prevEmails, err := repo.ListMemberEmails(ctx, g.ID)
	if err != nil {
		return fmt.Errorf("load current members: %w", err)
	}
	desiredSet := map[string]bool{}
	for _, e := range desiredEmails {
		desiredSet[e] = true
	}
	removed := make([]string, 0)
	for _, e := range prevEmails {
		if !desiredSet[e] {
			removed = append(removed, e)
		}
	}
	if err := repo.SetMembers(ctx, g.ID, desiredIDs); err != nil {
		return fmt.Errorf("set members: %w", err)
	}
	if g.GroupKind != "distribution" {
		notifyAgentMailGroup(ctx, "mailgroup.members_set", map[string]any{
			"group_email":   g.EmailCached,
			"member_emails": desiredEmails,
			"remove_emails": removed,
		})
	}
	return nil
}

func newMailGroupSetMembersCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "set-members <group-email|group-id> [member-email-or-id ...]",
		Short:   "Replace the full member set of a mail group",
		Args:    cobra.MinimumNArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), cliMailGroupAgentTimeout)
			defer cancel()
			g, err := resolveMailGroupCLI(ctx, args[0])
			if err != nil {
				return err
			}
			desiredIDs, desiredEmails, err := resolveMembers(ctx, g.DomainID, args[1:])
			if err != nil {
				return err
			}
			if err := projectMembers(ctx, g, desiredIDs, desiredEmails); err != nil {
				cliAuditErr(ctx, "mail_group.set_members", "mail_group", g.ID, nil)
				return err
			}
			cliAuditOK(ctx, "mail_group.set_members", "mail_group", g.ID, nil)
			fmt.Printf("%s now has %d member(s)\n", g.EmailCached, len(desiredEmails))
			return nil
		},
	}
}

func newMailGroupAddMemberCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "add-member <group-email|group-id> <member-email-or-id>",
		Short:   "Add one member to a mail group",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), cliMailGroupAgentTimeout)
			defer cancel()
			g, err := resolveMailGroupCLI(ctx, args[0])
			if err != nil {
				return err
			}
			// Compute the new full set (current + new) and re-project, so the
			// distribution-vs-resource projection rule stays identical.
			cur, err := mailGroupRepoFromDB().ListMemberEmails(ctx, g.ID)
			if err != nil {
				return fmt.Errorf("load current members: %w", err)
			}
			refs := append(append([]string{}, cur...), args[1])
			desiredIDs, desiredEmails, err := resolveMembers(ctx, g.DomainID, refs)
			if err != nil {
				return err
			}
			if err := projectMembers(ctx, g, desiredIDs, desiredEmails); err != nil {
				cliAuditErr(ctx, "mail_group.add_member", "mail_group", g.ID, nil)
				return err
			}
			cliAuditOK(ctx, "mail_group.add_member", "mail_group", g.ID, nil)
			fmt.Printf("added member to %s (now %d)\n", g.EmailCached, len(desiredEmails))
			return nil
		},
	}
}

func newMailGroupRemoveMemberCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove-member <group-email|group-id> <member-email-or-id>",
		Short:   "Remove one member from a mail group",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), cliMailGroupAgentTimeout)
			defer cancel()
			g, err := resolveMailGroupCLI(ctx, args[0])
			if err != nil {
				return err
			}
			// Resolve the target to its canonical email so we drop the right one.
			_, targetEmails, err := resolveMembers(ctx, g.DomainID, []string{args[1]})
			if err != nil {
				return err
			}
			target := targetEmails[0]
			cur, err := mailGroupRepoFromDB().ListMemberEmails(ctx, g.ID)
			if err != nil {
				return fmt.Errorf("load current members: %w", err)
			}
			keep := make([]string, 0, len(cur))
			for _, e := range cur {
				if e != target {
					keep = append(keep, e)
				}
			}
			desiredIDs, desiredEmails, err := resolveMembers(ctx, g.DomainID, keep)
			if err != nil {
				return err
			}
			if err := projectMembers(ctx, g, desiredIDs, desiredEmails); err != nil {
				cliAuditErr(ctx, "mail_group.remove_member", "mail_group", g.ID, nil)
				return err
			}
			cliAuditOK(ctx, "mail_group.remove_member", "mail_group", g.ID, nil)
			fmt.Printf("removed %s from %s (now %d)\n", target, g.EmailCached, len(desiredEmails))
			return nil
		},
	}
}

func newMailGroupDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <group-email|group-id>",
		Short:   "Delete a mail group (strips the Stalwart principal first)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), cliMailGroupAgentTimeout)
			defer cancel()
			g, err := resolveMailGroupCLI(ctx, args[0])
			if err != nil {
				return err
			}
			if !force {
				return fmt.Errorf("refusing to delete %s without --force", g.EmailCached)
			}
			repo := mailGroupRepoFromDB()
			memberEmails, err := repo.ListMemberEmails(ctx, g.ID)
			if err != nil {
				return fmt.Errorf("list members: %w", err)
			}
			// Agent FIRST (mirror the handler): destroy the principal before the
			// DB delete so rows never drift ahead of Stalwart.
			if _, err := sharedAgent.Call(ctx, "mailgroup.delete", map[string]any{
				"group_email":   g.EmailCached,
				"member_emails": memberEmails,
			}); err != nil {
				cliAuditErr(ctx, "mail_group.delete", "mail_group", g.ID, nil)
				return fmt.Errorf("agent mailgroup.delete: %w", err)
			}
			if err := repo.Delete(ctx, g.ID); err != nil {
				cliAuditErr(ctx, "mail_group.delete", "mail_group", g.ID, nil)
				return fmt.Errorf("delete mail group: %w", err)
			}
			cliAuditOK(ctx, "mail_group.delete", "mail_group", g.ID, nil)
			fmt.Printf("deleted mail group %s\n", g.EmailCached)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

func notifyAgentMailGroup(ctx context.Context, cmd string, params any) {
	if sharedAgent == nil {
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, cliMailGroupAgentTimeout)
	defer cancel()
	_, _ = sharedAgent.Call(agentCtx, cmd, params)
}
