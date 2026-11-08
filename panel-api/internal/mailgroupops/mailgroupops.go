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
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
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
	"an internal-only distribution list can have at most %d members, outside addresses included; remove members or turn off internal-only delivery",
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

// CheckMailboxMemberCap is CheckMemberCap for a change that leaves g with
// mailboxCount member mailboxes: an internal-only list redirects to its
// outside addresses too, so they count toward the limit (GH #1833). An error
// that is not IsMemberCapErr is a failed read.
func CheckMailboxMemberCap(ctx context.Context, groups repository.MailGroupRepository, g *models.MailGroup, internalOnly bool, mailboxCount int) error {
	if g.GroupKind != KindDistribution || !internalOnly {
		return nil
	}
	external, err := groups.ListExternalMembers(ctx, g.ID)
	if err != nil {
		return fmt.Errorf("load outside addresses: %w", err)
	}
	return CheckMemberCap(g.GroupKind, internalOnly, mailboxCount+len(external))
}

// MaxExternalMemberCap bounds the admin's per-list cap on outside addresses
// (server_settings.mail_list_external_member_cap): a list stays a list, not a
// bulk mailer.
const MaxExternalMemberCap = 1000

// ExternalMemberError is a refused outside address list. Code is the API
// error code; Address names the offending entry when there is one.
type ExternalMemberError struct {
	Code    string
	Address string
	Detail  string
}

func (e *ExternalMemberError) Error() string { return e.Detail }

// NormalizeExternalMembers validates the outside addresses wanted on the
// distribution list g and returns them canonical, de-duplicated and sorted.
// capN is the admin's per-list cap; current is what the list holds now. A
// list left over the cap by an admin lowering it may still drop addresses,
// just not take a new one. An address on the list's own domain is refused: it
// is a mailbox there, so it belongs in the member mailboxes (and the list's
// own address would loop). Any other domain is allowed, including one hosted
// on this server, since such an address cannot be a member mailbox of this
// list.
func NormalizeExternalMembers(g *models.MailGroup, raw []string, capN int, current []string) ([]string, error) {
	if g.GroupKind != KindDistribution {
		return nil, &ExternalMemberError{Code: "not_a_distribution_list",
			Detail: "only a distribution list can deliver to outside addresses"}
	}
	if len(raw) > MaxExternalMemberCap {
		return nil, tooManyExternal(capN)
	}
	listDomain := g.EmailCached[strings.LastIndex(g.EmailCached, "@")+1:]
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if strings.TrimSpace(r) == "" {
			continue
		}
		addr, err := mailaddr.CanonicaliseExternal(r)
		if err != nil {
			return nil, &ExternalMemberError{Code: "invalid_external_member", Address: r,
				Detail: fmt.Sprintf("%q is not a valid email address", r)}
		}
		if addr[strings.LastIndex(addr, "@")+1:] == listDomain {
			return nil, &ExternalMemberError{Code: "external_member_on_list_domain", Address: addr,
				Detail: fmt.Sprintf("%s is on this list's own domain; add the mailbox as a member instead", addr)}
		}
		if !seen[addr] {
			seen[addr] = true
			out = append(out, addr)
		}
	}
	if len(out) > capN && !allIn(out, current) {
		return nil, tooManyExternal(capN)
	}
	sort.Strings(out)
	return out, nil
}

func allIn(addrs, set []string) bool {
	in := make(map[string]bool, len(set))
	for _, s := range set {
		in[s] = true
	}
	for _, a := range addrs {
		if !in[a] {
			return false
		}
	}
	return true
}

// DefaultExternalMemberCap is mail_list_external_member_cap's migration
// default, used before the settings row exists.
const DefaultExternalMemberCap = 25

// ExternalMemberCap reads the admin's cap on a list's outside addresses. A nil
// repository, or no settings row yet, gives the default.
func ExternalMemberCap(ctx context.Context, settings repository.ServerSettingsRepository) (int, error) {
	if settings == nil {
		return DefaultExternalMemberCap, nil
	}
	s, err := settings.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		return DefaultExternalMemberCap, nil
	}
	if err != nil {
		return 0, err
	}
	return int(s.MailListExternalMemberCap), nil
}

// SetExternalMembers replaces the outside addresses of the distribution list g
// with raw after checking them against the admin's cap and, for an
// internal-only list, the redirect limit shared with its member mailboxes. It
// saves but does not project the list. A refused set is an
// *ExternalMemberError or IsMemberCapErr; anything else is a failed read or
// write.
func SetExternalMembers(ctx context.Context, groups repository.MailGroupRepository, settings repository.ServerSettingsRepository, g *models.MailGroup, raw []string) ([]string, error) {
	capN, err := ExternalMemberCap(ctx, settings)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	current, err := groups.ListExternalMembers(ctx, g.ID)
	if err != nil {
		return nil, fmt.Errorf("load outside addresses: %w", err)
	}
	addrs, err := NormalizeExternalMembers(g, raw, capN, current)
	if err != nil {
		return nil, err
	}
	if g.InternalOnly {
		ids, err := groups.ListMemberMailboxIDs(ctx, g.ID)
		if err != nil {
			return nil, fmt.Errorf("load group members: %w", err)
		}
		if err := CheckMemberCap(g.GroupKind, true, len(ids)+len(addrs)); err != nil {
			return nil, err
		}
	}
	if err := groups.SetExternalMembers(ctx, g.ID, addrs); err != nil {
		return nil, fmt.Errorf("save outside addresses: %w", err)
	}
	return addrs, nil
}

func tooManyExternal(capN int) *ExternalMemberError {
	detail := "the administrator does not allow outside addresses on lists"
	if capN > 0 {
		detail = fmt.Sprintf("a list can hold at most %d outside addresses", capN)
	}
	return &ExternalMemberError{Code: "too_many_external_members", Detail: detail}
}

// BuildApplyParams renders the mailgroup.apply payload for g. memberEmails is
// the group's deliverable member set and externalEmails its outside addresses
// (GH #1833); both are sent only for a distribution group (a resource group's
// membership is projected by mailgroup.members_set). The agent fans the list
// out to both but drains and unlinks member mailboxes only, so they travel
// apart.
func BuildApplyParams(g *models.MailGroup, memberEmails, externalEmails []string) map[string]any {
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
		if externalEmails == nil {
			externalEmails = []string{}
		}
		p["member_emails"] = memberEmails
		p["external_emails"] = externalEmails
	}
	return p
}

// ApplyParams loads the deliverable members and outside addresses of g
// (distribution groups only) and renders the mailgroup.apply payload. The DB
// is truth.
func ApplyParams(ctx context.Context, groups repository.MailGroupRepository, g *models.MailGroup) (map[string]any, error) {
	var members, external []string
	if g.GroupKind == KindDistribution {
		var err error
		if members, err = groups.ListDeliverableMemberEmails(ctx, g.ID); err != nil {
			return nil, fmt.Errorf("load group members: %w", err)
		}
		if external, err = groups.ListExternalMembers(ctx, g.ID); err != nil {
			return nil, fmt.Errorf("load outside addresses: %w", err)
		}
	}
	return BuildApplyParams(g, members, external), nil
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
