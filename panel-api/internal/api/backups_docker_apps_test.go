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
	rows []*models.DockerApp
	err  error
}

func (r *stubDockerAppRepo) ListByUserID(_ context.Context, _ string) ([]*models.DockerApp, error) {
	return r.rows, r.err
}

func TestAllUserDockerApps_ReturnsSlugs(t *testing.T) {
	cfg := BackupHandlerConfig{DockerApps: &stubDockerAppRepo{rows: []*models.DockerApp{
		{ID: "app-1", Slug: "nextcloud"},
		{ID: "app-2", Slug: "uptime-kuma"},
	}}}
	got := cfg.allUserDockerApps(context.Background(), "user-1")
	if len(got) != 2 || got[0] != "nextcloud" || got[1] != "uptime-kuma" {
		t.Fatalf("slugs not resolved for the backup call: %#v", got)
	}
}

// A nil repo is legitimate wiring (a deployment without the docker
// surface); it must degrade to "no apps", not panic mid-backup.
func TestAllUserDockerApps_NilRepo(t *testing.T) {
	cfg := BackupHandlerConfig{}
	if got := cfg.allUserDockerApps(context.Background(), "user-1"); len(got) != 0 {
		t.Fatalf("nil repo should yield no slugs, got %#v", got)
	}
}

func TestAllUserDockerApps_ErrorDoesNotPanic(t *testing.T) {
	cfg := BackupHandlerConfig{DockerApps: &stubDockerAppRepo{err: errors.New("db down")}}
	if got := cfg.allUserDockerApps(context.Background(), "user-1"); len(got) != 0 {
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
	got := cfg.allUserDockerApps(context.Background(), "user-1")
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
	got := cfg.allUserDockerApps(context.Background(), "user-1")
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
	got := cfg.allUserDockerApps(context.Background(), "user-1")
	if len(got) != 1 || got[0] != "nextcloud" {
		t.Fatalf("tenant path did not resolve docker apps: %#v", got)
	}
}
