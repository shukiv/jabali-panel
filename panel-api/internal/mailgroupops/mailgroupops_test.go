package mailgroupops

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestCheckMemberCap(t *testing.T) {
	require.NoError(t, CheckMemberCap("distribution", true, MaxInternalOnlyMembers))
	require.True(t, IsMemberCapErr(CheckMemberCap("distribution", true, MaxInternalOnlyMembers+1)))
	// Only internal-only distribution lists fan out through Sieve redirects.
	require.NoError(t, CheckMemberCap("distribution", false, 500))
	require.NoError(t, CheckMemberCap("resource", true, 500))
}

func TestBuildApplyParams(t *testing.T) {
	dist := &models.MailGroup{EmailCached: "sales@example.org", GroupKind: "distribution", InternalOnly: true, DisplayName: "Sales"}
	p := BuildApplyParams(dist, nil, nil)
	require.Equal(t, "distribution", p["group_kind"])
	require.Equal(t, []string{}, p["member_emails"], "an empty list must be sent as [], never null")
	require.Equal(t, []string{}, p["external_emails"], "an empty list must be sent as [], never null")
	require.Equal(t, true, p["internal_only"])

	p = BuildApplyParams(dist, []string{"a@example.org"}, []string{"x@gmail.com"})
	require.Equal(t, []string{"a@example.org"}, p["member_emails"])
	require.Equal(t, []string{"x@gmail.com"}, p["external_emails"], "outside addresses travel apart from member mailboxes")

	res := &models.MailGroup{EmailCached: "team@example.org", GroupKind: "resource"}
	p = BuildApplyParams(res, []string{"a@example.org"}, []string{"x@gmail.com"})
	_, has := p["member_emails"]
	require.False(t, has, "resource membership is projected by mailgroup.members_set, not apply")
	_, has = p["external_emails"]
	require.False(t, has, "a Shared Workspace group has no outside addresses")
}

type externalFake struct {
	repository.MailGroupRepository
	members  []string
	external []string
}

func (f *externalFake) ListDeliverableMemberEmails(context.Context, string) ([]string, error) {
	return f.members, nil
}
func (f *externalFake) ListExternalMembers(context.Context, string) ([]string, error) {
	return f.external, nil
}

func TestApplyParams_LoadsOutsideAddresses(t *testing.T) {
	g := &models.MailGroup{ID: "g1", EmailCached: "sales@example.org", GroupKind: "distribution"}
	p, err := ApplyParams(context.Background(), &externalFake{members: []string{"a@example.org"}, external: []string{"x@gmail.com"}}, g)
	require.NoError(t, err)
	require.Equal(t, []string{"x@gmail.com"}, p["external_emails"])
}

// GH #1833: an internal-only list redirects to its outside addresses too, so
// they count toward the 20-redirect limit.
func TestCheckMailboxMemberCap_CountsOutsideAddresses(t *testing.T) {
	g := &models.MailGroup{ID: "g1", GroupKind: "distribution", InternalOnly: true}
	fake := &externalFake{external: []string{"x@gmail.com", "y@gmail.com"}}
	ctx := context.Background()

	require.NoError(t, CheckMailboxMemberCap(ctx, fake, g, true, MaxInternalOnlyMembers-2))
	require.True(t, IsMemberCapErr(CheckMailboxMemberCap(ctx, fake, g, true, MaxInternalOnlyMembers-1)))
	require.NoError(t, CheckMailboxMemberCap(ctx, fake, g, false, 500), "a list that is not internal-only has no redirect limit")
	res := &models.MailGroup{ID: "g2", GroupKind: "resource"}
	require.NoError(t, CheckMailboxMemberCap(ctx, fake, res, true, 500))
}

func TestNormalizeExternalMembers(t *testing.T) {
	list := &models.MailGroup{EmailCached: "sales@example.org", GroupKind: "distribution"}

	got, err := NormalizeExternalMembers(list, []string{" B@Gmail.com", "a@outlook.com", "b@gmail.com", ""}, 25, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"a@outlook.com", "b@gmail.com"}, got, "canonical, de-duplicated, sorted; blanks dropped")

	// Another domain hosted on this server cannot be a member mailbox of this
	// list, so it is allowed as an outside address.
	got, err = NormalizeExternalMembers(list, []string{"someone@other-tenant.example"}, 25, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"someone@other-tenant.example"}, got)

	got, err = NormalizeExternalMembers(list, nil, 0, nil)
	require.NoError(t, err)
	require.Empty(t, got, "clearing the list is always allowed, even with the cap at 0")
}

func TestNormalizeExternalMembers_Refuses(t *testing.T) {
	list := &models.MailGroup{EmailCached: "sales@example.org", GroupKind: "distribution"}
	code := func(err error) string {
		var e *ExternalMemberError
		require.True(t, errors.As(err, &e), "want *ExternalMemberError, got %v", err)
		return e.Code
	}

	_, err := NormalizeExternalMembers(list, []string{`bad"quote@gmail.com`}, 25, nil)
	require.Equal(t, "invalid_external_member", code(err))
	_, err = NormalizeExternalMembers(list, []string{"sales@example.org"}, 25, nil)
	require.Equal(t, "external_member_on_list_domain", code(err), "the list's own address would loop")
	_, err = NormalizeExternalMembers(list, []string{"bob@Example.org"}, 25, nil)
	require.Equal(t, "external_member_on_list_domain", code(err))
	_, err = NormalizeExternalMembers(list, []string{"a@gmail.com", "b@gmail.com", "c@gmail.com"}, 2, nil)
	require.Equal(t, "too_many_external_members", code(err))
	_, err = NormalizeExternalMembers(list, []string{"a@gmail.com"}, 0, nil)
	require.Equal(t, "too_many_external_members", code(err), "a cap of 0 allows none")

	// An admin lowered the cap under what the list holds: it can still drop an
	// address, but not take a new one.
	held := []string{"a@gmail.com", "b@gmail.com", "c@gmail.com"}
	got, err := NormalizeExternalMembers(list, []string{"a@gmail.com", "b@gmail.com"}, 1, held)
	require.NoError(t, err)
	require.Equal(t, []string{"a@gmail.com", "b@gmail.com"}, got)
	_, err = NormalizeExternalMembers(list, []string{"a@gmail.com", "d@gmail.com"}, 1, held)
	require.Equal(t, "too_many_external_members", code(err))

	ws := &models.MailGroup{EmailCached: "team@example.org", GroupKind: "resource"}
	_, err = NormalizeExternalMembers(ws, []string{"a@gmail.com"}, 25, nil)
	require.Equal(t, "not_a_distribution_list", code(err))
}
