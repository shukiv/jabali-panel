package sharedresourceops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeResRepo implements repository.SharedResourceRepository. Only ExistsByEmail
// and Create carry behavior — the rest are unused by Create and return zero
// values. created records every persisted row so a test can assert whether a
// refusal wrote anything.
type fakeResRepo struct {
	exists   bool
	existErr error
	created  []*models.SharedResource
}

func (f *fakeResRepo) ExistsByEmail(_ context.Context, _ string) (bool, error) {
	return f.exists, f.existErr
}
func (f *fakeResRepo) Create(_ context.Context, r *models.SharedResource) error {
	f.created = append(f.created, r)
	return nil
}

func (f *fakeResRepo) FindByID(context.Context, string) (*models.SharedResource, error) {
	return nil, nil
}
func (f *fakeResRepo) ListByDomainID(context.Context, string) ([]models.SharedResource, error) {
	return nil, nil
}
func (f *fakeResRepo) ListByDomainAndKind(context.Context, string, string) ([]models.SharedResource, error) {
	return nil, nil
}
func (f *fakeResRepo) ListAll(context.Context) ([]models.SharedResource, error) { return nil, nil }
func (f *fakeResRepo) UpdateMeta(context.Context, string, string) error         { return nil }
func (f *fakeResRepo) UpdateProjection(context.Context, string, string, string) error {
	return nil
}
func (f *fakeResRepo) Delete(context.Context, string) error { return nil }
func (f *fakeResRepo) ListGrants(context.Context, string) ([]models.SharedResourceGrant, error) {
	return nil, nil
}
func (f *fakeResRepo) ListAllGrants(context.Context) ([]models.SharedResourceGrant, error) {
	return nil, nil
}
func (f *fakeResRepo) ReplaceGrants(context.Context, string, []models.SharedResourceGrant) error {
	return nil
}
func (f *fakeResRepo) PruneGranteeGrants(context.Context, string, string) error { return nil }
func (f *fakeResRepo) AddTombstone(context.Context, string) error               { return nil }
func (f *fakeResRepo) ListTombstones(context.Context) ([]string, error)         { return nil, nil }
func (f *fakeResRepo) DeleteTombstone(context.Context, string) error            { return nil }

// recordingNotify captures the agent apply call so a test can assert the leaf
// fires it with the right params.
type recordingNotify struct {
	fired  bool
	cmd    string
	params map[string]any
}

func (n *recordingNotify) fn(_ context.Context, cmd string, params any) {
	n.fired = true
	n.cmd = cmd
	if m, ok := params.(map[string]any); ok {
		n.params = m
	}
}

func emailDomain() *models.Domain {
	return &models.Domain{ID: "dom1", Name: "example.org", EmailEnabled: true}
}

func TestCreate_RefusesOnDisabledEmail(t *testing.T) {
	repo := &fakeResRepo{}
	dom := &models.Domain{ID: "dom1", Name: "example.org", EmailEnabled: false}
	_, err := Create(context.Background(), Deps{Resources: repo}, CreateInput{
		Domain: dom, Kind: "calendar", Name: "teamcal",
	}, nil)
	if !errors.Is(err, ErrEmailNotEnabled) {
		t.Fatalf("want ErrEmailNotEnabled, got %v", err)
	}
	if len(repo.created) != 0 {
		t.Fatalf("no row must be written on a disabled domain, got %d", len(repo.created))
	}
}

func TestCreate_RefusesInvalidKind(t *testing.T) {
	repo := &fakeResRepo{}
	_, err := Create(context.Background(), Deps{Resources: repo}, CreateInput{
		Domain: emailDomain(), Kind: "carrier-pigeon", Name: "teamcal",
	}, nil)
	if !errors.Is(err, ErrInvalidKind) {
		t.Fatalf("want ErrInvalidKind, got %v", err)
	}
	if len(repo.created) != 0 {
		t.Fatalf("no row must be written for an invalid kind, got %d", len(repo.created))
	}
}

func TestCreate_RefusesInvalidName(t *testing.T) {
	repo := &fakeResRepo{}
	// A local part that cannot canonicalize (a space is not a valid address char).
	_, err := Create(context.Background(), Deps{Resources: repo}, CreateInput{
		Domain: emailDomain(), Kind: "calendar", Name: "bad name",
	}, nil)
	if !errors.Is(err, ErrInvalidName) {
		t.Fatalf("want ErrInvalidName, got %v", err)
	}
	if len(repo.created) != 0 {
		t.Fatalf("no row must be written for an invalid name, got %d", len(repo.created))
	}
}

func TestCreate_RefusesDuplicateAddress(t *testing.T) {
	repo := &fakeResRepo{exists: true}
	_, err := Create(context.Background(), Deps{Resources: repo}, CreateInput{
		Domain: emailDomain(), Kind: "calendar", Name: "teamcal",
	}, nil)
	if !errors.Is(err, ErrAddressTaken) {
		t.Fatalf("want ErrAddressTaken, got %v", err)
	}
	if len(repo.created) != 0 {
		t.Fatalf("no row must be written when the address is taken, got %d", len(repo.created))
	}
}

func TestCreate_HappyPathTrimsAndFiresApply(t *testing.T) {
	repo := &fakeResRepo{}
	notify := &recordingNotify{}
	sr, err := Create(context.Background(), Deps{Resources: repo}, CreateInput{
		Domain: emailDomain(), Kind: "calendar", Name: "TeamCal", DisplayName: "  Team Calendar  ",
	}, notify.fn)
	if err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("want 1 row written, got %d", len(repo.created))
	}
	if sr.DisplayName != "Team Calendar" {
		t.Fatalf("DisplayName not trimmed: %q", sr.DisplayName)
	}
	if sr.EmailCached == nil || *sr.EmailCached != "teamcal@example.org" {
		t.Fatalf("EmailCached = %v, want teamcal@example.org", sr.EmailCached)
	}
	if sr.LocalPart == nil || *sr.LocalPart != "teamcal" {
		t.Fatalf("LocalPart = %v, want teamcal", sr.LocalPart)
	}
	if !notify.fired || notify.cmd != "sharedresource.apply" {
		t.Fatalf("apply not fired: fired=%v cmd=%q", notify.fired, notify.cmd)
	}
	if notify.params["email"] != "teamcal@example.org" ||
		notify.params["display_name"] != "Team Calendar" ||
		notify.params["kind"] != "calendar" {
		t.Fatalf("apply params wrong: %#v", notify.params)
	}
}

func TestCreate_NilNotifyDoesNotPanic(t *testing.T) {
	repo := &fakeResRepo{}
	if _, err := Create(context.Background(), Deps{Resources: repo}, CreateInput{
		Domain: emailDomain(), Kind: "files", Name: "teamfiles",
	}, nil); err != nil {
		t.Fatalf("nil notify happy path: %v", err)
	}
}

// seqResRepo records the tombstone/delete call order and can force either to
// fail. It embeds the interface so Delete's unused methods are never reached.
type seqResRepo struct {
	repository.SharedResourceRepository
	ops       *[]string
	tombErr   error
	deleteErr error
}

func (r *seqResRepo) AddTombstone(_ context.Context, _ string) error {
	*r.ops = append(*r.ops, "tombstone")
	return r.tombErr
}
func (r *seqResRepo) Delete(_ context.Context, _ string) error {
	*r.ops = append(*r.ops, "delete")
	return r.deleteErr
}

func resWithEmail() *models.SharedResource {
	e := "teamcal@example.org"
	return &models.SharedResource{ID: "res1", EmailCached: &e}
}

// TestDelete_TombstoneFailKeepsRow is the AC5 guarantee: a tombstone that cannot
// be persisted must NOT delete the row (that would orphan the host principal),
// and the destroy must not fire.
func TestDelete_TombstoneFailKeepsRow(t *testing.T) {
	ops := []string{}
	repo := &seqResRepo{ops: &ops, tombErr: errors.New("db down")}
	fired := false
	err := Delete(context.Background(), Deps{Resources: repo}, DeleteInput{Resource: resWithEmail()},
		func(context.Context, string, any) { fired = true })
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("want ErrInternal, got %v", err)
	}
	if len(ops) != 1 || ops[0] != "tombstone" {
		t.Fatalf("row must not be deleted when the tombstone fails; ops=%v", ops)
	}
	if fired {
		t.Fatal("destroy must not fire when the tombstone fails")
	}
}

func TestDelete_HappyPathOrder(t *testing.T) {
	ops := []string{}
	repo := &seqResRepo{ops: &ops}
	var gotCmd string
	var gotParams map[string]any
	notify := func(_ context.Context, cmd string, params any) {
		ops = append(ops, "destroy")
		gotCmd = cmd
		if m, ok := params.(map[string]any); ok {
			gotParams = m
		}
	}
	if err := Delete(context.Background(), Deps{Resources: repo}, DeleteInput{Resource: resWithEmail()}, notify); err != nil {
		t.Fatalf("happy path: %v", err)
	}
	if len(ops) != 3 || ops[0] != "tombstone" || ops[1] != "destroy" || ops[2] != "delete" {
		t.Fatalf("want tombstone,destroy,delete in order; ops=%v", ops)
	}
	if gotCmd != "sharedresource.destroy" || gotParams["email"] != "teamcal@example.org" {
		t.Fatalf("destroy call wrong: cmd=%q params=%#v", gotCmd, gotParams)
	}
}

func TestDelete_NoEmailSkipsTeardown(t *testing.T) {
	ops := []string{}
	repo := &seqResRepo{ops: &ops}
	fired := false
	if err := Delete(context.Background(), Deps{Resources: repo},
		DeleteInput{Resource: &models.SharedResource{ID: "res1"}},
		func(context.Context, string, any) { fired = true }); err != nil {
		t.Fatalf("no-email delete: %v", err)
	}
	if len(ops) != 1 || ops[0] != "delete" {
		t.Fatalf("no cached address means no tombstone/destroy, just delete; ops=%v", ops)
	}
	if fired {
		t.Fatal("destroy must not fire with no cached address")
	}
}

func TestDelete_NilNotifyNoPanic(t *testing.T) {
	ops := []string{}
	repo := &seqResRepo{ops: &ops}
	if err := Delete(context.Background(), Deps{Resources: repo}, DeleteInput{Resource: resWithEmail()}, nil); err != nil {
		t.Fatalf("nil notify: %v", err)
	}
	if len(ops) != 2 || ops[0] != "tombstone" || ops[1] != "delete" {
		t.Fatalf("nil notify: tombstone then delete; ops=%v", ops)
	}
}

func TestDelete_NilDepsReturnsErrDeps(t *testing.T) {
	if err := Delete(context.Background(), Deps{}, DeleteInput{Resource: resWithEmail()}, nil); !errors.Is(err, ErrDeps) {
		t.Fatalf("nil repo: want ErrDeps, got %v", err)
	}
	ops := []string{}
	if err := Delete(context.Background(), Deps{Resources: &seqResRepo{ops: &ops}}, DeleteInput{Resource: nil}, nil); !errors.Is(err, ErrDeps) {
		t.Fatalf("nil resource: want ErrDeps, got %v", err)
	}
}
