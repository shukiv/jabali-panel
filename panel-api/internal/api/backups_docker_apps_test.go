package api

// The agent cannot work out which docker apps belong to an account — panel
// side resolves the slugs and ships them in backup.create. If this resolver
// returns nothing, the backup completes "successfully" with the app data
// missing, which is exactly how the gap in GH #954 stayed invisible.

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type stubDockerAppRepo struct {
	repository.DockerAppRepository
	rows    []*models.DockerApp // ListByUserID result (a tenant's apps)
	allRows []*models.DockerApp // ListAll result (used for server-level resolution)
	err     error
}

func (r *stubDockerAppRepo) ListByUserID(_ context.Context, _ string) ([]*models.DockerApp, error) {
	return r.rows, r.err
}

func (r *stubDockerAppRepo) ListAll(_ context.Context) ([]*models.DockerApp, error) {
	return r.allRows, r.err
}

func TestAllUserDockerApps_ReturnsSlugs(t *testing.T) {
	cfg := BackupHandlerConfig{DockerApps: &stubDockerAppRepo{rows: []*models.DockerApp{
		{ID: "app-1", Slug: "nextcloud"},
		{ID: "app-2", Slug: "uptime-kuma"},
	}}}
	got := cfg.allUserDockerApps(context.Background(), "user-1", false)
	if len(got) != 2 || got[0] != "nextcloud" || got[1] != "uptime-kuma" {
		t.Fatalf("slugs not resolved for the backup call: %#v", got)
	}
}

// A nil repo is legitimate wiring (a deployment without the docker
// surface); it must degrade to "no apps", not panic mid-backup.
func TestAllUserDockerApps_NilRepo(t *testing.T) {
	cfg := BackupHandlerConfig{}
	if got := cfg.allUserDockerApps(context.Background(), "user-1", false); len(got) != 0 {
		t.Fatalf("nil repo should yield no slugs, got %#v", got)
	}
}

func TestAllUserDockerApps_ErrorDoesNotPanic(t *testing.T) {
	cfg := BackupHandlerConfig{DockerApps: &stubDockerAppRepo{err: errors.New("db down")}}
	if got := cfg.allUserDockerApps(context.Background(), "user-1", false); len(got) != 0 {
		t.Fatalf("a repo error should yield no slugs, got %#v", got)
	}
}

// The data tree is /var/lib/jabali/docker-apps/<EffectiveSlug>. A second
// instance of a catalog app carries an InstanceSlug that differs from the
// catalog Slug, so resolving on Slug backs up the wrong directory — or the
// first instance twice, silently, while the second is never covered.
//
// Found on a real box: the installed app's row had slug=uptime-kuma and the
// directory matched EffectiveSlug, not the install name.
func TestAllUserDockerApps_UsesEffectiveSlug(t *testing.T) {
	cfg := BackupHandlerConfig{DockerApps: &stubDockerAppRepo{rows: []*models.DockerApp{
		{ID: "app-1", Slug: "uptime-kuma", InstanceSlug: "uptime-kuma-2"},
		{ID: "app-2", Slug: "gitea"},
	}}}
	got := cfg.allUserDockerApps(context.Background(), "user-1", false)
	if len(got) != 2 || got[0] != "uptime-kuma-2" {
		t.Fatalf("second instance must resolve to its InstanceSlug, got %#v", got)
	}
	if got[1] != "gitea" {
		t.Errorf("an app without an InstanceSlug should fall back to Slug, got %q", got[1])
	}
}

// A row with an empty slug would become /var/lib/jabali/docker-apps/ —
// the whole root — if it ever reached the agent's path join.
func TestAllUserDockerApps_SkipsEmptySlug(t *testing.T) {
	cfg := BackupHandlerConfig{DockerApps: &stubDockerAppRepo{rows: []*models.DockerApp{
		{ID: "app-1", Slug: ""},
		{ID: "app-2", Slug: "nextcloud"},
	}}}
	got := cfg.allUserDockerApps(context.Background(), "user-1", false)
	if len(got) != 1 || got[0] != "nextcloud" {
		t.Fatalf("empty slug must be dropped, got %#v", got)
	}
}

// The tenant's own backup path has its own config struct; it must resolve
// docker apps too or a self-service restore comes back without them.
func TestMeAllUserDockerApps_ReturnsSlugs(t *testing.T) {
	cfg := MeBackupsHandlerConfig{DockerApps: &stubDockerAppRepo{rows: []*models.DockerApp{
		{ID: "app-1", Slug: "nextcloud"},
	}}}
	got := cfg.allUserDockerApps(context.Background(), "user-1", false)
	if len(got) != 1 || got[0] != "nextcloud" {
		t.Fatalf("tenant path did not resolve docker apps: %#v", got)
	}
}

func strp(s string) *string { return &s }

// GH #1360: admin/server-level docker apps (UserID NULL) have no tenant
// account, so account backups never carried them — the app's data came back
// missing on restore. An ADMIN account backup now folds in the live
// server-level apps (from ListAll), while tenant-owned rows in ListAll and
// deleted tombstones are excluded.
func TestAllUserDockerApps_AdminIncludesServerLevel(t *testing.T) {
	repo := &stubDockerAppRepo{
		rows: []*models.DockerApp{{ID: "own-1", Slug: "gitea"}}, // the admin's own tenant apps (usually none)
		allRows: []*models.DockerApp{
			{ID: "srv-1", Slug: "jabali-sounder", InstanceSlug: "jabali-sounder-test", UserID: nil},
			{ID: "srv-2", Slug: "vaultwarden", UserID: nil},
			{ID: "ten-1", Slug: "nextcloud", UserID: strp("someone")},                                 // tenant-owned — not server-level
			{ID: "del-1", Slug: "ghost", UserID: nil, Status: models.DockerAppStatusDeleted},          // deleted tombstone
		},
	}
	cfg := BackupHandlerConfig{DockerApps: repo}
	got := cfg.allUserDockerApps(context.Background(), "admin-1", true)
	want := map[string]bool{"gitea": true, "jabali-sounder-test": true, "vaultwarden": true}
	if len(got) != len(want) {
		t.Fatalf("admin backup slug set = %#v, want %v", got, want)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected slug %q (tenant-owned or deleted apps must not be included)", s)
		}
	}
}

// A NON-admin account never carries server-level apps, even if some exist:
// they are not the tenant's, and ListAll must not even be consulted for the
// slug set beyond the tenant's own rows.
func TestAllUserDockerApps_NonAdminExcludesServerLevel(t *testing.T) {
	repo := &stubDockerAppRepo{
		rows:    []*models.DockerApp{{ID: "own-1", Slug: "nextcloud"}},
		allRows: []*models.DockerApp{{ID: "srv-1", Slug: "jabali-sounder", UserID: nil}},
	}
	cfg := BackupHandlerConfig{DockerApps: repo}
	got := cfg.allUserDockerApps(context.Background(), "user-1", false)
	if len(got) != 1 || got[0] != "nextcloud" {
		t.Fatalf("non-admin must see only its own apps, got %#v", got)
	}
}
