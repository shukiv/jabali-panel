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
	require.NoError(t, CheckMemberCap("distribution", true, MaxRedirectRecipients))
	require.True(t, IsMemberCapErr(CheckMemberCap("distribution", true, MaxRedirectRecipients+1)))
	// Only a distribution list that delivers by redirect has the limit.
	require.NoError(t, CheckMemberCap("distribution", false, 500))
	require.NoError(t, CheckMemberCap("resource", true, 500))
}

// GH #1833: an internal-only list and a list with outside addresses deliver by
// redirect; a list of mailboxes only, open to any sender, does not.
func TestDeliversByRedirect(t *testing.T) {
	require.True(t, DeliversByRedirect(true, 0))
	require.True(t, DeliversByRedirect(false, 1))
	require.False(t, DeliversByRedirect(false, 0))
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

	require.NoError(t, CheckMailboxMemberCap(ctx, fake, g, true, MaxRedirectRecipients-2))
	require.True(t, IsMemberCapErr(CheckMailboxMemberCap(ctx, fake, g, true, MaxRedirectRecipients-1)))
	res := &models.MailGroup{ID: "g2", GroupKind: "resource"}
	require.NoError(t, CheckMailboxMemberCap(ctx, fake, res, true, 500))
}

// GH #1833: a list with outside addresses delivers by redirect even when it is
// open to any sender, so it has the same limit; a list of mailboxes only does
// not.
func TestCheckMailboxMemberCap_OpenListWithOutsideAddresses(t *testing.T) {
	g := &models.MailGroup{ID: "g1", GroupKind: "distribution"}
	ctx := context.Background()

	withOutside := &externalFake{external: []string{"x@gmail.com"}}
	require.NoError(t, CheckMailboxMemberCap(ctx, withOutside, g, false, MaxRedirectRecipients-1))
	require.True(t, IsMemberCapErr(CheckMailboxMemberCap(ctx, withOutside, g, false, MaxRedirectRecipients)))

	mailboxOnly := &externalFake{}
	require.NoError(t, CheckMailboxMemberCap(ctx, mailboxOnly, g, false, 500), "a list of mailboxes only has no limit")
}

// A list left over the limit (saved before it applied) may shrink toward it,
// but not grow or stay put.
func TestCheckMailboxMemberCap_OverLimitListMayShrink(t *testing.T) {
	g := &models.MailGroup{ID: "g1", GroupKind: "distribution"}
	ctx := context.Background()
	held := make([]string, MaxRedirectRecipients)
	fake := &externalFake{members: held, external: []string{"x@gmail.com", "y@gmail.com"}} // 22 recipients

	require.NoError(t, CheckMailboxMemberCap(ctx, fake, g, false, MaxRedirectRecipients-1), "21 of 22 is a step down")
	require.True(t, IsMemberCapErr(CheckMailboxMemberCap(ctx, fake, g, false, MaxRedirectRecipients)), "22 is no step down")
	require.True(t, IsMemberCapErr(CheckMailboxMemberCap(ctx, fake, g, false, MaxRedirectRecipients+1)))

	// A mailbox-only list of 25 cannot become internal-only: same count, now
	// over the limit.
	big := &externalFake{members: make([]string, 25)}
	require.True(t, IsMemberCapErr(CheckMailboxMemberCap(ctx, big, g, true, 25)))
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

func (f *externalFake) ListMemberMailboxIDs(context.Context, string) ([]string, error) {
	return f.members, nil
}
func (f *externalFake) SetExternalMembers(_ context.Context, _ string, addrs []string) error {
	f.external = addrs
	return nil
}

type settingsFake struct {
	repository.ServerSettingsRepository
	row *models.ServerSettings
}

func (f *settingsFake) Get(context.Context) (*models.ServerSettings, error) {
	if f.row == nil {
		return nil, repository.ErrNotFound
	}
	return f.row, nil
}

func TestExternalMemberCap(t *testing.T) {
	ctx := context.Background()
	n, err := ExternalMemberCap(ctx, nil)
	require.NoError(t, err)
	require.Equal(t, DefaultExternalMemberCap, n)
	n, err = ExternalMemberCap(ctx, &settingsFake{})
	require.NoError(t, err)
	require.Equal(t, MaxRedirectRecipients, n, "no settings row yet means the migration default")
	n, err = ExternalMemberCap(ctx, &settingsFake{row: &models.ServerSettings{MailListExternalMemberCap: 0}})
	require.NoError(t, err)
	require.Equal(t, 0, n, "0 is a real setting, not unset")
}

func TestSetExternalMembers_SavesCanonical(t *testing.T) {
	g := &models.MailGroup{ID: "g1", EmailCached: "sales@example.org", GroupKind: "distribution"}
	fake := &externalFake{}
	got, err := SetExternalMembers(context.Background(), fake, nil, g, []string{"B@gmail.com", "a@gmail.com"})
	require.NoError(t, err)
	require.Equal(t, []string{"a@gmail.com", "b@gmail.com"}, got)
	require.Equal(t, got, fake.external)
}

// An internal-only list counts its member mailboxes and outside addresses
// against one redirect limit.
func TestSetExternalMembers_InternalOnlyLimit(t *testing.T) {
	g := &models.MailGroup{ID: "g1", EmailCached: "sales@example.org", GroupKind: "distribution", InternalOnly: true}
	members := make([]string, MaxRedirectRecipients)
	for i := range members {
		members[i] = "mb"
	}
	fake := &externalFake{members: members}
	_, err := SetExternalMembers(context.Background(), fake, nil, g, []string{"a@gmail.com"})
	require.True(t, IsMemberCapErr(err), "got %v", err)
	require.Nil(t, fake.external, "a refused set must not be saved")
}

// GH #1833: the first outside address turns an open list into a redirect list,
// so its mailboxes and outside addresses share the limit too.
func TestSetExternalMembers_OpenListLimit(t *testing.T) {
	g := &models.MailGroup{ID: "g1", EmailCached: "sales@example.org", GroupKind: "distribution"}
	members := make([]string, MaxRedirectRecipients)
	fake := &externalFake{members: members}
	_, err := SetExternalMembers(context.Background(), fake, nil, g, []string{"a@gmail.com"})
	require.True(t, IsMemberCapErr(err), "got %v", err)
	require.Nil(t, fake.external, "a refused set must not be saved")

	fake = &externalFake{members: members[:MaxRedirectRecipients-1]}
	got, err := SetExternalMembers(context.Background(), fake, nil, g, []string{"a@gmail.com"})
	require.NoError(t, err)
	require.Equal(t, []string{"a@gmail.com"}, got)
}

// A list left over the limit may drop outside addresses, and dropping the last
// one makes it a list of mailboxes only, which has no limit.
func TestSetExternalMembers_OverLimitListMayShrink(t *testing.T) {
	g := &models.MailGroup{ID: "g1", EmailCached: "sales@example.org", GroupKind: "distribution"}
	fake := &externalFake{members: make([]string, MaxRedirectRecipients), external: []string{"a@gmail.com", "b@gmail.com"}}
	got, err := SetExternalMembers(context.Background(), fake, nil, g, []string{"a@gmail.com"})
	require.NoError(t, err, "21 of 22 is a step down")
	require.Equal(t, []string{"a@gmail.com"}, got)
	got, err = SetExternalMembers(context.Background(), fake, nil, g, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

// Every outside address costs one redirect, so the admin's cap on them never
// goes past the redirect limit.
func TestExternalMemberCapWithinRedirectLimit(t *testing.T) {
	require.Equal(t, MaxRedirectRecipients, MaxExternalMemberCap)
	require.LessOrEqual(t, DefaultExternalMemberCap, MaxExternalMemberCap)
}
