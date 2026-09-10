package sharedresourceops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
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
