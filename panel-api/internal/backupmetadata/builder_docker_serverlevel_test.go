package backupmetadata

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// stubDockerRepo implements just the three methods Build touches; the rest of
// the interface is embedded (nil) and would panic if reached — a guard that
// Build stays on the expected read path.
type stubDockerRepo struct {
	repository.DockerAppRepository
	byUser []*models.DockerApp
	all    []*models.DockerApp
}

func (s *stubDockerRepo) ListByUserID(context.Context, string) ([]*models.DockerApp, error) {
	return s.byUser, nil
}
func (s *stubDockerRepo) ListAll(context.Context) ([]*models.DockerApp, error) { return s.all, nil }
func (s *stubDockerRepo) ListPortsForApp(context.Context, string) ([]*models.DockerAppPublishedPort, error) {
	return nil, nil
}

func ptr(s string) *string { return &s }

// GH #1360: an admin account's metadata bundle folds in the live server-level
// docker apps (UserID NULL) marked ServerLevel, so restore rebuilds their
// rows. Tenant-owned rows in ListAll and deleted tombstones are excluded, and
// the admin's own tenant apps stay ServerLevel=false.
func TestBuild_AdminFoldsServerLevelDockerApps(t *testing.T) {
	repo := &stubDockerRepo{
		byUser: []*models.DockerApp{{ID: "own-1", Slug: "gitea"}},
		all: []*models.DockerApp{
			{ID: "srv-1", Slug: "jabali-sounder", UserID: nil},
			{ID: "ten-1", Slug: "nextcloud", UserID: ptr("someone")},
			{ID: "del-1", Slug: "ghost", UserID: nil, Status: models.DockerAppStatusDeleted},
		},
	}
	m := Build(context.Background(), &models.User{ID: "admin-1", IsAdmin: true}, Deps{DockerApps: repo})

	byID := map[string]bool{}
	for _, a := range m.DockerApps {
		byID[a.ID] = a.ServerLevel
	}
	if len(m.DockerApps) != 2 {
		t.Fatalf("want 2 docker apps (own + one server-level), got %d: %#v", len(m.DockerApps), m.DockerApps)
	}
	if sl, ok := byID["own-1"]; !ok || sl {
		t.Errorf("own-1 must be present with ServerLevel=false, got ok=%v serverLevel=%v", ok, sl)
	}
	if sl, ok := byID["srv-1"]; !ok || !sl {
		t.Errorf("srv-1 must be present with ServerLevel=true, got ok=%v serverLevel=%v", ok, sl)
	}
	if _, ok := byID["ten-1"]; ok {
		t.Error("a tenant-owned app from ListAll must NOT be folded into the admin backup")
	}
	if _, ok := byID["del-1"]; ok {
		t.Error("a deleted server-level tombstone must NOT be folded in")
	}
}

// A non-admin account never folds in server-level apps.
func TestBuild_NonAdminOmitsServerLevelDockerApps(t *testing.T) {
	repo := &stubDockerRepo{
		byUser: []*models.DockerApp{{ID: "own-1", Slug: "nextcloud"}},
		all:    []*models.DockerApp{{ID: "srv-1", Slug: "jabali-sounder", UserID: nil}},
	}
	m := Build(context.Background(), &models.User{ID: "user-1", IsAdmin: false}, Deps{DockerApps: repo})
	if len(m.DockerApps) != 1 || m.DockerApps[0].ID != "own-1" || m.DockerApps[0].ServerLevel {
		t.Fatalf("non-admin must carry only its own tenant apps, got %#v", m.DockerApps)
	}
}
