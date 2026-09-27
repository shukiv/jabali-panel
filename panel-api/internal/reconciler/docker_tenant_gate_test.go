package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type gateAgent struct {
	calls map[string][]map[string]any
}

func (a *gateAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if a.calls == nil {
		a.calls = map[string][]map[string]any{}
	}
	if m, ok := params.(map[string]any); ok {
		a.calls[cmd] = append(a.calls[cmd], m)
	}
	if cmd == "docker_app.check_update" {
		return json.RawMessage(`{"update_available":true,"available_digest":"sha256:0123456789abcdef"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

type gateUserRepo struct {
	repository.UserRepository
	user *models.User
	err  error
}

func (f *gateUserRepo) FindByID(context.Context, string) (*models.User, error) { return f.user, f.err }

type gateDockerRepo struct {
	repository.DockerAppRepository
	status map[string]string
}

func (f *gateDockerRepo) UpdateStatus(_ context.Context, id, status string, _ *string) error {
	if f.status == nil {
		f.status = map[string]string{}
	}
	f.status[id] = status
	return nil
}
func (f *gateDockerRepo) UpdateAvailableDigest(context.Context, string, string) error { return nil }
func (f *gateDockerRepo) MarkChecked(context.Context, string) error                   { return nil }

func gateReconciler(t *testing.T, users *gateUserRepo) (*Reconciler, *gateAgent, *gateDockerRepo) {
	t.Helper()
	ag := &gateAgent{}
	repo := &gateDockerRepo{}
	r := &Reconciler{agent: ag, dockerApps: repo, dockerCatalog: loadReconcileTestCatalog(t), log: slog.Default()}
	if users != nil {
		r.users = users
	}
	return r, ag, repo
}

func alice() *gateUserRepo {
	name := "alice"
	return &gateUserRepo{user: &models.User{ID: "u1", Username: &name}}
}

func assertTenantGate(t *testing.T, door string, p map[string]any) {
	t.Helper()
	if p["tenant_validate"] != true {
		t.Errorf("%s: tenant_validate = %v, want true", door, p["tenant_validate"])
	}
	if p["tenant_cgroup"] != "jabali-user-alice.slice" {
		t.Errorf("%s: tenant_cgroup = %v, want the exact owner slice", door, p["tenant_cgroup"])
	}
	if _, ok := p["tenant_caps"]; !ok {
		t.Errorf("%s: tenant_caps missing", door)
	}
}

// GH #1903: the reconciler's recovery install brought a tenant app's on-disk
// compose up with no tenant gate at all.
func TestDispatchInstall_TenantRecoveryCarriesTheGate(t *testing.T) {
	r, ag, _ := gateReconciler(t, alice())
	uid := "u1"
	r.dispatchInstall(context.Background(), &models.DockerApp{ID: "a1", Slug: "memos", UserID: &uid})
	calls := ag.calls["docker_app.install"]
	if len(calls) != 1 {
		t.Fatalf("want one recovery install, got %d", len(calls))
	}
	if calls[0]["compose_yml"] != "RECOVERY" {
		t.Fatalf("recovery dispatch lost its sentinel: %v", calls[0])
	}
	assertTenantGate(t, "recovery install", calls[0])
}

func TestDispatchInstall_AdminRecoveryHasNoGate(t *testing.T) {
	r, ag, _ := gateReconciler(t, nil)
	r.dispatchInstall(context.Background(), &models.DockerApp{ID: "a1", Slug: "memos"})
	calls := ag.calls["docker_app.install"]
	if len(calls) != 1 {
		t.Fatalf("want one recovery install, got %d", len(calls))
	}
	if _, ok := calls[0]["tenant_validate"]; ok {
		t.Fatalf("an admin app must not carry the tenant gate: %v", calls[0])
	}
}

// When the owner slice cannot be resolved, the recovery install is refused
// and the row is marked failed instead of retried unvalidated.
func TestDispatchInstall_TenantFailsClosedWithoutOwner(t *testing.T) {
	r, ag, repo := gateReconciler(t, &gateUserRepo{err: errors.New("db down")})
	uid := "u1"
	r.dispatchInstall(context.Background(), &models.DockerApp{ID: "a1", Slug: "memos", UserID: &uid})
	if n := len(ag.calls["docker_app.install"]); n != 0 {
		t.Fatalf("recovery install dispatched without the tenant gate (%d calls)", n)
	}
	if repo.status["a1"] != models.DockerAppStatusFailed {
		t.Fatalf("row status = %q, want failed", repo.status["a1"])
	}
}

func autoApp(uid *string) *models.DockerApp {
	return &models.DockerApp{
		ID: "a1", Slug: "memos", UserID: uid,
		UpdateMode: models.DockerAppUpdateModeAuto, Status: models.DockerAppStatusRunning,
	}
}

// The auto-update gated tenant apps without the exact owner slice, so a
// compose declaring another tenant's slice passed the generic pattern.
func TestAutoUpdate_TenantCarriesTheExactOwnerSlice(t *testing.T) {
	r, ag, _ := gateReconciler(t, alice())
	uid := "u1"
	r.pollImageUpdate(context.Background(), autoApp(&uid))
	calls := ag.calls["docker_app.update"]
	if len(calls) != 1 {
		t.Fatalf("want one auto-update, got %d", len(calls))
	}
	assertTenantGate(t, "auto-update", calls[0])
}

func TestAutoUpdate_TenantSkippedWithoutOwner(t *testing.T) {
	r, ag, repo := gateReconciler(t, &gateUserRepo{err: errors.New("db down")})
	uid := "u1"
	r.pollImageUpdate(context.Background(), autoApp(&uid))
	if n := len(ag.calls["docker_app.update"]); n != 0 {
		t.Fatalf("auto-update dispatched without the tenant gate (%d calls)", n)
	}
	if _, touched := repo.status["a1"]; touched {
		t.Fatalf("a skipped auto-update must leave the running app's status alone, got %q", repo.status["a1"])
	}
}
