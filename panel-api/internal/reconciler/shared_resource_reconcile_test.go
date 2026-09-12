package reconciler

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- minimal embedded-interface fakes (only the methods the reconciler uses) ---

type srFake struct {
	repository.SharedResourceRepository
	res        []models.SharedResource
	grants     map[string][]models.SharedResourceGrant
	tombs      []string
	proj       map[string][2]string
	tombKilled []string
}

func (f *srFake) ListAll(context.Context) ([]models.SharedResource, error) { return f.res, nil }
func (f *srFake) ListGrants(_ context.Context, id string) ([]models.SharedResourceGrant, error) {
	return f.grants[id], nil
}
func (f *srFake) UpdateProjection(_ context.Context, id, host, col string) error {
	if f.proj == nil {
		f.proj = map[string][2]string{}
	}
	f.proj[id] = [2]string{host, col}
	return nil
}
func (f *srFake) ListTombstones(context.Context) ([]string, error) { return f.tombs, nil }
func (f *srFake) DeleteTombstone(_ context.Context, e string) error {
	f.tombKilled = append(f.tombKilled, e)
	return nil
}

type mbFake struct {
	repository.MailboxRepository
	byID map[string]*models.Mailbox
	err  error // if set, every FindByID returns this (a data-access failure, not not-found)
}

func (f *mbFake) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	if f.err != nil {
		return nil, f.err
	}
	if m, ok := f.byID[id]; ok {
		return m, nil
	}
	return nil, repository.ErrNotFound // mirror the real repo (not a nil,nil)
}

type mgFake struct {
	repository.MailGroupRepository
	members map[string][]string
	err     error // if set, every ListMemberEmails returns this
}

func (f *mgFake) ListMemberEmails(_ context.Context, id string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.members[id], nil // a missing/empty group is no rows + nil err, like the real repo
}

type domFake struct {
	repository.DomainRepository
	byID map[string]*models.Domain
	err  error // if set, every FindByID returns this (a data-access failure, not not-found)
}

func (f *domFake) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if f.err != nil {
		return nil, f.err
	}
	if d, ok := f.byID[id]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound // mirror the real repo
}

func sptr(s string) *string { return &s }

func newSRReconciler(t *testing.T, ag *fakeAgent, sr *srFake, mb *mbFake, mg *mgFake, dom *domFake) *Reconciler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return New(dom, nil, ag, log, Config{Interval: time.Second}).WithSharedResources(sr, mb, mg)
}

func TestReconcileSharedResources_ProjectsAndShares(t *testing.T) {
	email := "teamcal@example.org"
	sr := &srFake{
		res: []models.SharedResource{{
			ID: "res1", DomainID: "dom1", Kind: "calendar", EmailCached: sptr(email), DisplayName: "Team Cal",
		}},
		grants: map[string][]models.SharedResourceGrant{
			"res1": {
				{ResourceID: "res1", GranteeKind: "mailbox", GranteeID: "mb1", Rights: "readwrite"},
				{ResourceID: "res1", GranteeKind: "group", GranteeID: "grp1", Rights: "read"},
			},
		},
	}
	mb := &mbFake{byID: map[string]*models.Mailbox{"mb1": {ID: "mb1", LocalPart: "alice", DomainID: "dom1"}}}
	mg := &mgFake{members: map[string][]string{"grp1": {"bob@example.org"}}}
	dom := &domFake{byID: map[string]*models.Domain{"dom1": {ID: "dom1", Name: "example.org"}}}
	ag := &fakeAgent{}

	r := newSRReconciler(t, ag, sr, mb, mg, dom)
	r.reconcileSharedResources(context.Background())

	// apply was called + projection cached.
	var applied, shared *fakeCall
	for i := range ag.calls {
		switch ag.calls[i].method {
		case "sharedresource.apply":
			applied = &ag.calls[i]
		case "calendar.share_set":
			shared = &ag.calls[i]
		}
	}
	require.NotNil(t, applied, "sharedresource.apply must be called")
	require.Equal(t, [2]string{"hostAcct1", ""}, sr.proj["res1"], "host_account_id cached")
	require.NotNil(t, shared, "calendar.share_set must be called")

	p := shared.params.(map[string]any)
	require.Equal(t, email, p["owner_email"])
	shares := p["shares"].(map[string]string)
	require.Equal(t, "readwrite", shares["alice@example.org"], "mailbox grantee resolved")
	require.Equal(t, "read", shares["bob@example.org"], "group grantee expanded to members")
}

func TestReconcileSharedResources_TombstoneGC(t *testing.T) {
	sr := &srFake{tombs: []string{"gone@example.org"}}
	ag := &fakeAgent{}
	r := newSRReconciler(t, ag, sr, &mbFake{}, &mgFake{}, &domFake{})
	r.reconcileSharedResources(context.Background())

	var destroyed bool
	for _, c := range ag.calls {
		if c.method == "sharedresource.destroy" {
			destroyed = true
			require.Equal(t, "gone@example.org", c.params.(map[string]any)["email"])
		}
	}
	require.True(t, destroyed, "tombstone host destroyed")
	require.Equal(t, []string{"gone@example.org"}, sr.tombKilled, "tombstone cleared on success")
}

func TestReconcileSharedResources_TombstoneRetainedOnAgentFailure(t *testing.T) {
	sr := &srFake{tombs: []string{"gone@example.org"}}
	ag := &fakeAgent{failMethod: "sharedresource.destroy"}
	r := newSRReconciler(t, ag, sr, &mbFake{}, &mgFake{}, &domFake{})
	r.reconcileSharedResources(context.Background())
	require.Empty(t, sr.tombKilled, "tombstone kept when destroy fails (retry next pass)")
}

// --- JAB-339 AC3: grantee-lookup fail-closed (keep last-known shareWith) ---

// calendarShareSet returns the first calendar.share_set push (nil if none).
func calendarShareSet(ag *fakeAgent) *fakeCall {
	for i := range ag.calls {
		if ag.calls[i].method == "calendar.share_set" {
			return &ag.calls[i]
		}
	}
	return nil
}

// shareSetCount counts calendar.share_set pushes across all resources in a pass.
func shareSetCount(ag *fakeAgent) int {
	n := 0
	for _, c := range ag.calls {
		if c.method == "calendar.share_set" {
			n++
		}
	}
	return n
}

func calRes(id, email string) models.SharedResource {
	return models.SharedResource{
		ID: id, DomainID: "dom1", Kind: "calendar", EmailCached: sptr(email),
		DisplayName: "Cal " + id, HostAccountID: "h-" + id, // non-empty: skip the apply step
	}
}

// A genuine not-found mailbox grantee stays inert: it is dropped and the push
// still runs for the remaining grantees (unchanged behavior, pinned).
func TestReconcileSharedResources_MissingMailboxGranteeInert_PushProceeds(t *testing.T) {
	sr := &srFake{
		res: []models.SharedResource{calRes("res1", "teamcal@example.org")},
		grants: map[string][]models.SharedResourceGrant{
			"res1": {
				{ResourceID: "res1", GranteeKind: "mailbox", GranteeID: "ghost", Rights: "read"},
				{ResourceID: "res1", GranteeKind: "mailbox", GranteeID: "mb1", Rights: "readwrite"},
			},
		},
	}
	mb := &mbFake{byID: map[string]*models.Mailbox{"mb1": {ID: "mb1", LocalPart: "alice", DomainID: "dom1"}}}
	dom := &domFake{byID: map[string]*models.Domain{"dom1": {ID: "dom1", Name: "example.org"}}}
	ag := &fakeAgent{}

	r := newSRReconciler(t, ag, sr, mb, &mgFake{}, dom)
	r.reconcileSharedResources(context.Background())

	cs := calendarShareSet(ag)
	require.NotNil(t, cs, "missing grantee is inert — push still runs for the rest")
	shares := cs.params.(map[string]any)["shares"].(map[string]string)
	require.Equal(t, map[string]string{"alice@example.org": "readwrite"}, shares,
		"missing grantee dropped; resolvable grantee still shared")
}

// A mailbox whose DomainID dangles (domain not-found) is likewise inert.
func TestReconcileSharedResources_MissingDomainInert_PushProceeds(t *testing.T) {
	sr := &srFake{
		res: []models.SharedResource{calRes("res1", "teamcal@example.org")},
		grants: map[string][]models.SharedResourceGrant{
			"res1": {
				{ResourceID: "res1", GranteeKind: "mailbox", GranteeID: "mb1", Rights: "read"},
				{ResourceID: "res1", GranteeKind: "group", GranteeID: "grp1", Rights: "read"},
			},
		},
	}
	mb := &mbFake{byID: map[string]*models.Mailbox{"mb1": {ID: "mb1", LocalPart: "alice", DomainID: "dom_gone"}}}
	mg := &mgFake{members: map[string][]string{"grp1": {"bob@example.org"}}}
	dom := &domFake{byID: map[string]*models.Domain{}} // dom_gone absent → ErrNotFound

	ag := &fakeAgent{}
	r := newSRReconciler(t, ag, sr, mb, mg, dom)
	r.reconcileSharedResources(context.Background())

	cs := calendarShareSet(ag)
	require.NotNil(t, cs, "dangling domain is inert — push still runs")
	shares := cs.params.(map[string]any)["shares"].(map[string]string)
	require.Equal(t, map[string]string{"bob@example.org": "read"}, shares,
		"mailbox with dangling domain dropped; group grantee survives")
}

// A data-access error (NOT not-found) resolving a mailbox grantee must skip the
// whole push, so Stalwart keeps last-known shareWith rather than losing the
// grantee on a transient DB blip.
func TestReconcileSharedResources_MailboxLookupDBError_SkipsPush(t *testing.T) {
	sr := &srFake{
		res: []models.SharedResource{calRes("res1", "teamcal@example.org")},
		grants: map[string][]models.SharedResourceGrant{
			"res1": {{ResourceID: "res1", GranteeKind: "mailbox", GranteeID: "mb1", Rights: "read"}},
		},
	}
	ag := &fakeAgent{}
	r := newSRReconciler(t, ag, sr, &mbFake{err: errors.New("db down")}, &mgFake{}, &domFake{})
	r.reconcileSharedResources(context.Background())

	require.Nil(t, calendarShareSet(ag),
		"mailbox lookup DB error must skip the push (keep last-known state)")
}

func TestReconcileSharedResources_DomainLookupDBError_SkipsPush(t *testing.T) {
	sr := &srFake{
		res: []models.SharedResource{calRes("res1", "teamcal@example.org")},
		grants: map[string][]models.SharedResourceGrant{
			"res1": {{ResourceID: "res1", GranteeKind: "mailbox", GranteeID: "mb1", Rights: "read"}},
		},
	}
	mb := &mbFake{byID: map[string]*models.Mailbox{"mb1": {ID: "mb1", LocalPart: "alice", DomainID: "dom1"}}}
	ag := &fakeAgent{}
	r := newSRReconciler(t, ag, sr, mb, &mgFake{}, &domFake{err: errors.New("db down")})
	r.reconcileSharedResources(context.Background())

	require.Nil(t, calendarShareSet(ag),
		"domain lookup DB error must skip the push (keep last-known state)")
}

func TestReconcileSharedResources_GroupLookupDBError_SkipsPush(t *testing.T) {
	sr := &srFake{
		res: []models.SharedResource{calRes("res1", "teamcal@example.org")},
		grants: map[string][]models.SharedResourceGrant{
			"res1": {{ResourceID: "res1", GranteeKind: "group", GranteeID: "grp1", Rights: "read"}},
		},
	}
	ag := &fakeAgent{}
	r := newSRReconciler(t, ag, sr, &mbFake{}, &mgFake{err: errors.New("db down")}, &domFake{})
	r.reconcileSharedResources(context.Background())

	require.Nil(t, calendarShareSet(ag),
		"group lookup DB error must skip the push (keep last-known state)")
}

// A DB error on one resource must not stop a sibling resource in the same pass
// from converging — the skip is per-resource.
func TestReconcileSharedResources_DBError_OtherResourceStillPushed(t *testing.T) {
	sr := &srFake{
		res: []models.SharedResource{
			calRes("res1", "a@example.org"), // mailbox grantee → hits the failing lookup
			calRes("res2", "b@example.org"), // group grantee → resolves
		},
		grants: map[string][]models.SharedResourceGrant{
			"res1": {{ResourceID: "res1", GranteeKind: "mailbox", GranteeID: "mb1", Rights: "read"}},
			"res2": {{ResourceID: "res2", GranteeKind: "group", GranteeID: "grp1", Rights: "read"}},
		},
	}
	mb := &mbFake{err: errors.New("db down")} // fails res1's mailbox lookup only
	mg := &mgFake{members: map[string][]string{"grp1": {"bob@example.org"}}}

	ag := &fakeAgent{}
	r := newSRReconciler(t, ag, sr, mb, mg, &domFake{})
	r.reconcileSharedResources(context.Background())

	require.Equal(t, 1, shareSetCount(ag), "res1 push skipped on DB error; res2 still pushed")
	require.Equal(t, "b@example.org", calendarShareSet(ag).params.(map[string]any)["owner_email"],
		"the surviving push is the healthy sibling res2")
}
