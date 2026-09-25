// Package mailgroupops holds the single shared path that projects a mail group
// to Stalwart, so the HTTP handlers, the CLI and the reconcile pass all push
// the same desired state.
//
// GH #1818 / #1834: a distribution group used to be projected as a Stalwart
// Group account, which parks mail in the group's own inbox where no member sees
// it. The agent's mailgroup.apply now projects a distribution group as a
// mailing list (or, when internal_only is set, a Group account whose Sieve
// redirects to the members), so it needs the group kind and the full set of
// members that can receive mail on every apply. A resource group keeps its
// Group-account projection; its membership still goes through
// mailgroup.members_set.
package mailgroupops

import (
	"context"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// KindDistribution is the group_kind of a mailing-list style group.
const KindDistribution = "distribution"

// MaxInternalOnlyMembers is the largest internal-only distribution list the
// mail server can deliver to. Such a list fans out with one Sieve redirect per
// member, and Stalwart allows 20 redirects per message; members past that
// would silently receive nothing. Mirrors maxInternalOnlyListMembers in the
// agent.
const MaxInternalOnlyMembers = 20

// ErrInternalOnlyTooManyMembers rejects a change that would leave an
// internal-only distribution list with more than MaxInternalOnlyMembers.
var ErrInternalOnlyTooManyMembers = fmt.Errorf(
	"an internal-only distribution list can have at most %d members; remove members or turn off internal-only delivery",
	MaxInternalOnlyMembers)

// CheckMemberCap returns ErrInternalOnlyTooManyMembers when a group of the
// given kind and internal-only setting would have memberCount members over the
// limit. The count is every member, including disabled and send-only ones, so
// re-enabling a member can never push a saved list past the limit.
func CheckMemberCap(kind string, internalOnly bool, memberCount int) error {
	if kind == KindDistribution && internalOnly && memberCount > MaxInternalOnlyMembers {
		return ErrInternalOnlyTooManyMembers
	}
	return nil
}

// IsMemberCapErr reports whether err is the internal-only member cap.
func IsMemberCapErr(err error) bool {
	return errors.Is(err, ErrInternalOnlyTooManyMembers)
}

// BuildApplyParams renders the mailgroup.apply payload for g. memberEmails is
// the group's deliverable member set; it is sent only for a distribution group
// (a resource group's membership is projected by mailgroup.members_set).
func BuildApplyParams(g *models.MailGroup, memberEmails []string) map[string]any {
	p := map[string]any{
		"email":         g.EmailCached,
		"display_name":  g.DisplayName,
		"description":   g.Description,
		"internal_only": g.InternalOnly,
		"has_files":     g.HasFiles,
		"group_kind":    g.GroupKind,
	}
	if g.GroupKind == KindDistribution {
		if memberEmails == nil {
			memberEmails = []string{}
		}
		p["member_emails"] = memberEmails
	}
	return p
}

// ApplyParams loads the deliverable members of g (distribution groups only)
// and renders the mailgroup.apply payload. The DB is truth.
func ApplyParams(ctx context.Context, groups repository.MailGroupRepository, g *models.MailGroup) (map[string]any, error) {
	var members []string
	if g.GroupKind == KindDistribution {
		var err error
		if members, err = groups.ListDeliverableMemberEmails(ctx, g.ID); err != nil {
			return nil, fmt.Errorf("load group members: %w", err)
		}
	}
	return BuildApplyParams(g, members), nil
}

// Apply pushes g's desired projection to Stalwart via mailgroup.apply. A nil
// agent is a no-op.
func Apply(ctx context.Context, ag agent.AgentInterface, groups repository.MailGroupRepository, g *models.MailGroup) error {
	if ag == nil {
		return nil
	}
	params, err := ApplyParams(ctx, groups, g)
	if err != nil {
		return err
	}
	_, err = ag.Call(ctx, "mailgroup.apply", params)
	return err
}
