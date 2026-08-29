package backupscheduler

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type stubUsers struct {
	repository.UserRepository
	byID    map[string]*models.User
	listOut []models.User
	lastOpt repository.ListOptions
}

func (s *stubUsers) List(_ context.Context, opts repository.ListOptions) ([]models.User, int64, error) {
	s.lastOpt = opts
	return s.listOut, int64(len(s.listOut)), nil
}
func (s *stubUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := s.byID[id]; ok {
		return u, nil
	}
	return nil, errors.New("not found")
}

// No explicit users → the default sweep lists tenants only (IsAdmin=false),
// admins are never touched by a blanket schedule.
func TestResolveAccountTargets_DefaultSweepExcludesAdmins(t *testing.T) {
	repo := &stubUsers{listOut: []models.User{{ID: "t1"}, {ID: "t2"}}}
	got, err := resolveAccountTargets(context.Background(), repo, "sched-1", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if repo.lastOpt.IsAdmin == nil || *repo.lastOpt.IsAdmin != false {
		t.Fatalf("default sweep must filter IsAdmin=false, got %#v", repo.lastOpt.IsAdmin)
	}
	if len(got) != 2 {
		t.Fatalf("want the two tenants, got %#v", got)
	}
}

// GH #1360: an explicitly named admin IS honored (so their server-level docker
// apps get a scheduled account backup); a normal tenant too.
func TestResolveAccountTargets_ExplicitAdminHonored(t *testing.T) {
	repo := &stubUsers{byID: map[string]*models.User{
		"admin-1": {ID: "admin-1", IsAdmin: true},
		"ten-1":   {ID: "ten-1", IsAdmin: false},
	}}
	got, err := resolveAccountTargets(context.Background(), repo, "sched-1", []string{"admin-1", "ten-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("both the named admin and tenant must be honored, got %#v", got)
	}
	seen := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !seen["admin-1"] || !seen["ten-1"] {
		t.Errorf("expected admin-1 + ten-1, got %#v", got)
	}
}

// A named user that no longer exists is warn-skipped, not fatal.
func TestResolveAccountTargets_MissingUserSkipped(t *testing.T) {
	repo := &stubUsers{byID: map[string]*models.User{"ten-1": {ID: "ten-1"}}}
	got, err := resolveAccountTargets(context.Background(), repo, "sched-1", []string{"gone", "ten-1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "ten-1" {
		t.Fatalf("missing user must be skipped, leaving only ten-1, got %#v", got)
	}
}
