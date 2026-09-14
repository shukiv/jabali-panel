package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sharedresourceops"
)

// fakeSRRepo implements repository.SharedResourceRepository for the CLI
// testable-core. ExistsByEmail + Create carry the create-path behavior;
// grants (seed) + replaced/replaceCalled carry the grant-path behavior
// (JAB-339 AC3): grants seeds ListGrants, and ReplaceGrants records the set it
// was handed so a test can assert both the upsert shape and that a failed
// validation writes nothing.
type fakeSRRepo struct {
	exists  bool
	created []*models.SharedResource

	grants        []models.SharedResourceGrant // seed for ListGrants
	replaced      []models.SharedResourceGrant // captured ReplaceGrants arg
	replaceCalled bool
}

func (f *fakeSRRepo) ExistsByEmail(context.Context, string) (bool, error) { return f.exists, nil }
func (f *fakeSRRepo) Create(_ context.Context, r *models.SharedResource) error {
	f.created = append(f.created, r)
	return nil
}
func (f *fakeSRRepo) FindByID(context.Context, string) (*models.SharedResource, error) {
	return nil, nil
}
func (f *fakeSRRepo) ListByDomainID(context.Context, string) ([]models.SharedResource, error) {
	return nil, nil
}
func (f *fakeSRRepo) ListByDomainAndKind(context.Context, string, string) ([]models.SharedResource, error) {
	return nil, nil
}
func (f *fakeSRRepo) ListAll(context.Context) ([]models.SharedResource, error) { return nil, nil }
func (f *fakeSRRepo) UpdateMeta(context.Context, string, string) error         { return nil }
func (f *fakeSRRepo) UpdateProjection(context.Context, string, string, string) error {
	return nil
}
func (f *fakeSRRepo) Delete(context.Context, string) error { return nil }
func (f *fakeSRRepo) ListGrants(context.Context, string) ([]models.SharedResourceGrant, error) {
	return f.grants, nil
}
func (f *fakeSRRepo) ListAllGrants(context.Context) ([]models.SharedResourceGrant, error) {
	return nil, nil
}
func (f *fakeSRRepo) ReplaceGrants(_ context.Context, _ string, g []models.SharedResourceGrant) error {
	f.replaceCalled = true
	f.replaced = g
	return nil
}
func (f *fakeSRRepo) PruneGranteeGrants(context.Context, string, string) error { return nil }
func (f *fakeSRRepo) AddTombstone(context.Context, string) error               { return nil }
func (f *fakeSRRepo) ListTombstones(context.Context) ([]string, error)         { return nil, nil }
func (f *fakeSRRepo) DeleteTombstone(context.Context, string) error            { return nil }

// TestCreateSharedResourceDirect_RefusesOnDisabledEmail: the REST handler blocks
// this with email_not_enabled; before this leaf the CLI silently created an
// orphan on a mail-disabled domain. The CLI must now mirror the guard.
func TestCreateSharedResourceDirect_RefusesOnDisabledEmail(t *testing.T) {
	repo := &fakeSRRepo{}
	dom := testDomain("dom1", "example.org", false) // email disabled
	_, err := createSharedResourceDirect(context.Background(), repo, nil, dom, "calendar", "teamcal", "")
	if err == nil {
		t.Fatal("expected an error on a mail-disabled domain")
	}
	if len(repo.created) != 0 {
		t.Fatalf("no row must be written on a disabled domain, got %d", len(repo.created))
	}
}

// TestCreateSharedResourceDirect_RefusesDuplicate: the CLI gained the
// pre-INSERT duplicate check the REST handler already had, so a taken address
// yields a typed error instead of a raw UNIQUE-constraint driver failure.
func TestCreateSharedResourceDirect_RefusesDuplicate(t *testing.T) {
	repo := &fakeSRRepo{exists: true}
	dom := testDomain("dom1", "example.org", true)
	_, err := createSharedResourceDirect(context.Background(), repo, nil, dom, "calendar", "teamcal", "")
	if err == nil {
		t.Fatal("expected an error when the address is already taken")
	}
	if len(repo.created) != 0 {
		t.Fatalf("no row must be written for a duplicate, got %d", len(repo.created))
	}
}

// recordSR captures the best-effort apply so the test can assert the CLI fires
// it — the AC3 parity guarantee (the CLI now projects the same state the REST
// handler does, not just persists).
type recordSR struct {
	fired  bool
	cmd    string
	params map[string]any
}

func (r *recordSR) notify(_ context.Context, cmd string, params any) {
	r.fired = true
	r.cmd = cmd
	if m, ok := params.(map[string]any); ok {
		r.params = m
	}
}

func TestCreateSharedResourceDirect_TrimsAndFiresApply(t *testing.T) {
	repo := &fakeSRRepo{}
	rec := &recordSR{}
	dom := testDomain("dom1", "example.org", true)
	sr, err := createSharedResourceDirect(context.Background(), repo, rec.notify, dom,
		"calendar", "TeamCal", "  Team Calendar  ")
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("want 1 row persisted, got %d", len(repo.created))
	}
	if sr.DisplayName != "Team Calendar" {
		t.Fatalf("DisplayName not trimmed: %q", sr.DisplayName)
	}
	if sr.EmailCached == nil || *sr.EmailCached != "teamcal@example.org" {
		t.Fatalf("EmailCached = %v, want teamcal@example.org", sr.EmailCached)
	}
	// The load-bearing AC3 assertion: the CLI fires the same apply the REST
	// handler does. Passing a nil notifier from the RunE reddens this.
	if !rec.fired || rec.cmd != "sharedresource.apply" {
		t.Fatalf("apply not fired: fired=%v cmd=%q", rec.fired, rec.cmd)
	}
	if rec.params["email"] != "teamcal@example.org" ||
		rec.params["display_name"] != "Team Calendar" ||
		rec.params["kind"] != "calendar" {
		t.Fatalf("apply params wrong: %#v", rec.params)
	}
}

// TestSharedResourceAdapters_RouteThroughLeaf source-pins that neither adapter's
// create body hand-builds the row or fires the apply verb by string literal —
// both must route through sharedresourceops so the policy has one owner.
func TestSharedResourceAdapters_RouteThroughLeaf(t *testing.T) {
	cli := mustRead(t, "shared_resource_cmd.go")
	if !strings.Contains(cli, "createSharedResourceDirect(") {
		t.Error("CLI create must call createSharedResourceDirect")
	}
	if strings.Contains(cli, "sharedResourceRepoFromDB().Create(ctx, sr)") {
		t.Error("CLI must not hand-build + persist the row itself")
	}
	if strings.Contains(cli, `"sharedresource.apply"`) {
		t.Error("CLI must not fire the apply verb by string literal")
	}
	// AC3: the create RunE must hand the production notifier to the leaf, or the
	// apply silently no-ops and the CLI only persists. The behavioral tests pass
	// a fake notifier, so this is the only thing pinning the real wiring.
	if !strings.Contains(cli, "notifyAgentSharedResource, dom,") {
		t.Error("CLI create must pass notifyAgentSharedResource so the apply fires (AC3)")
	}
}

// TestSharedResourceRemove_RoutesThroughLeafAndArmsAgent source-pins the remove
// subcommand. Its behavior is not cheaply testable — sharedAgent / sharedDB are
// concrete package globals the RunE dials directly — so the wiring is pinned at
// the source level (same rationale as the create routing pin). Scoped to the
// remove block so the create subcommand's own requireDBAndAgent doesn't satisfy
// the check.
func TestSharedResourceRemove_RoutesThroughLeafAndArmsAgent(t *testing.T) {
	cli := mustRead(t, "shared_resource_cmd.go")
	marker := `Use:   "remove"`
	i := strings.Index(cli, marker)
	if i < 0 {
		t.Fatal("remove subcommand not found")
	}
	block := cli[i:]
	if j := strings.Index(block[len(marker):], "Use:"); j >= 0 {
		block = block[:len(marker)+j]
	}
	if !strings.Contains(block, "deleteSharedResourceDirect(") {
		t.Error("CLI remove must route the teardown through deleteSharedResourceDirect")
	}
	// Pin the field assignment, not the bare word: the block's own comment
	// mentions requireDBAndAgent, so a Contains on the word alone would still
	// pass if PreRunE silently reverted to requireDB.
	if !strings.Contains(block, "PreRunE: requireDBAndAgent") {
		t.Error("CLI remove must use requireDBAndAgent, or the instant destroy never fires (sharedAgent stays nil)")
	}
	if !strings.Contains(block, "warning: host teardown failed") {
		t.Error("CLI remove must surface a teardown failure on stderr, not swallow it")
	}
	// The old bypass loaded with `FindByID(...); err == nil && ...`, skipping
	// teardown on a load error yet still deleting the row. The leaf now loads and
	// returns the error, so this shape must be gone.
	if strings.Contains(block, "err == nil &&") {
		t.Error("CLI remove must not skip teardown on a FindByID error and delete the row anyway")
	}
}

// TestSharedResourceGrant_RoutesFullSetThroughCore source-pins JAB-339 AC3 in the
// CLI grant subcommand: the RunE must route through grantSharedResourceDirect —
// which lists, upserts, validates the FULL resulting set, then replaces — and it
// must hand the real resource owner (dom.UserID) as the owner arg. Pinning the
// owner arg guards the #1696 scar: an empty owner is a wiring bug ValidateGrants
// rejects, and a hardcoded one would defeat the same-owner policy. The old inline
// single-delta ValidateGrants call must be gone (validating only the new grant,
// not the set, was the AC3 gap). Full behavior is covered by the two
// grantSharedResourceDirect tests; this pins the wiring the RunE can't test
// cheaply (sharedDB/domainRepoFromDB are concrete globals). Scoped to the grant
// block so the revoke subcommand can't satisfy it.
func TestSharedResourceGrant_RoutesFullSetThroughCore(t *testing.T) {
	cli := mustRead(t, "shared_resource_cmd.go")
	marker := `Use:     "grant"`
	i := strings.Index(cli, marker)
	if i < 0 {
		t.Fatal("grant subcommand not found")
	}
	block := cli[i:]
	if j := strings.Index(block[len(marker):], "Use:"); j >= 0 {
		block = block[:len(marker)+j]
	}
	if !strings.Contains(block, "grantSharedResourceDirect(ctx, repo, grantDeps, dom.UserID,") {
		t.Error("CLI grant must route through grantSharedResourceDirect with dom.UserID as the owner (JAB-339 AC3)")
	}
	// The old RunE validated only the single new delta inline; the full-set
	// validation now lives in the core, so no inline ValidateGrants may remain
	// (it would re-introduce the delta-only gap).
	if strings.Contains(block, "sharedresourceops.ValidateGrants(") {
		t.Error("CLI grant must not validate a single-grant delta inline; grantSharedResourceDirect validates the full set")
	}
}

// Narrow lookup fakes for sharedresourceops.ValidateGrants — each satisfies one
// consumer-side interface (MailboxLookup / MailGroupLookup / DomainLookup) with a
// map keyed by id, returning repository.ErrNotFound for a miss so ValidateGrants
// exercises its not-found → ErrGranteeNotFound arm.
type fakeMailboxLookup struct{ byID map[string]*models.Mailbox }

func (f fakeMailboxLookup) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	if mb, ok := f.byID[id]; ok {
		return mb, nil
	}
	return nil, repository.ErrNotFound
}

type fakeMailGroupLookup struct{ byID map[string]*models.MailGroup }

func (f fakeMailGroupLookup) FindByID(_ context.Context, id string) (*models.MailGroup, error) {
	if mg, ok := f.byID[id]; ok {
		return mg, nil
	}
	return nil, repository.ErrNotFound
}

type fakeDomainLookup struct{ byID map[string]*models.Domain }

func (f fakeDomainLookup) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if d, ok := f.byID[id]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

// grantLookups wires an in-scope mailbox "mb-good" owned by "u-owner" so a happy
// grant passes; a test seeds fakeSRRepo.grants with whatever legacy set it needs.
func grantLookups(owner string) sharedresourceops.Deps {
	return sharedresourceops.Deps{
		Mailboxes: fakeMailboxLookup{byID: map[string]*models.Mailbox{
			"mb-good": {ID: "mb-good", DomainID: "d1"},
			"mb1":     {ID: "mb1", DomainID: "d1"},
		}},
		MailGroups: fakeMailGroupLookup{byID: map[string]*models.MailGroup{}},
		Domains: fakeDomainLookup{byID: map[string]*models.Domain{
			"d1": {ID: "d1", UserID: owner},
		}},
	}
}

// TestGrantSharedResourceDirect_FailsLoudOnLegacyInvalidGrant is the JAB-339 AC3
// guard. A resource already carrying a legacy grant to a now-missing mailbox must
// reject a grant of an otherwise-valid grantee — the same rejection the REST
// setGrants door gives for the equivalent full-set PUT — instead of upserting the
// valid grantee and silently writing the bad legacy grant back. Under the old
// delta-only validation (validate just the new grant) this passed and persisted;
// that is the discriminator this test relies on.
func TestGrantSharedResourceDirect_FailsLoudOnLegacyInvalidGrant(t *testing.T) {
	const owner = "u-owner"
	repo := &fakeSRRepo{grants: []models.SharedResourceGrant{
		{ResourceID: "sr1", GranteeKind: "mailbox", GranteeID: "mb-legacy-gone", Rights: "read"},
	}}
	err := grantSharedResourceDirect(context.Background(), repo, grantLookups(owner),
		owner, "sr1", "mailbox", "mb-good", "readwrite")
	if !errors.Is(err, sharedresourceops.ErrGranteeNotFound) {
		t.Fatalf("want ErrGranteeNotFound for the legacy missing grantee, got %v", err)
	}
	if repo.replaceCalled {
		t.Fatal("ReplaceGrants must not run when the resulting set fails validation")
	}
}

// TestGrantSharedResourceDirect_UpsertsAndReplacesFullSet: an in-scope grantee
// added to a clean set validates and writes the upserted set back — an existing
// grant for the same grantee is replaced (rights updated), not duplicated.
func TestGrantSharedResourceDirect_UpsertsAndReplacesFullSet(t *testing.T) {
	const owner = "u-owner"
	repo := &fakeSRRepo{grants: []models.SharedResourceGrant{
		{ResourceID: "sr1", GranteeKind: "mailbox", GranteeID: "mb1", Rights: "read"},
	}}
	if err := grantSharedResourceDirect(context.Background(), repo, grantLookups(owner),
		owner, "sr1", "mailbox", "mb1", "readwrite"); err != nil {
		t.Fatalf("happy upsert: %v", err)
	}
	if !repo.replaceCalled {
		t.Fatal("ReplaceGrants must run on a valid set")
	}
	if len(repo.replaced) != 1 {
		t.Fatalf("upsert must not duplicate mb1: want 1 grant, got %d", len(repo.replaced))
	}
	if g := repo.replaced[0]; g.GranteeID != "mb1" || g.Rights != "readwrite" {
		t.Fatalf("upsert must update rights in place: got %+v", g)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
