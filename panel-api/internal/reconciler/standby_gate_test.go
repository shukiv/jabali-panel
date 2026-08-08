package reconciler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// buildGateReconciler wires a reconciler with one enabled domain and a
// server_settings repo carrying the given role, so a ReconcileAll on a PRIMARY
// reaches the agent (domain.list) but on a STANDBY must short-circuit.
func buildGateReconciler(t *testing.T, role string) (*Reconciler, *fakeAgent) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}

	now := time.Now().UTC()
	username := "alice"
	userRepo.users["user-1"] = &models.User{ID: "user-1", Email: "a@example.com", Username: &username}
	domainRepo.domains["domain-1"] = &models.Domain{
		ID: "domain-1", UserID: "user-1", Name: "warm.example.com",
		DocRoot:   "/home/alice/domains/warm.example.com/public_html",
		IsEnabled: true, CreatedAt: now, UpdatedAt: now,
	}

	srv := &fakeServerSettingsRepo{settings: &models.ServerSettings{ServerRole: role}}
	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).
		WithDNSRepos(nil, nil, srv)
	return r, agent
}

func TestReconcileAll_StandbyIsDormant(t *testing.T) {
	r, agent := buildGateReconciler(t, models.ServerRoleStandby)
	require.NoError(t, r.ReconcileAll(context.Background()))
	agent.mu.Lock()
	defer agent.mu.Unlock()
	require.Empty(t, agent.calls, "a DR standby must make no agent calls in ReconcileAll (builds no serving config)")
}

func TestReconcileAll_PrimaryConverges(t *testing.T) {
	r, agent := buildGateReconciler(t, models.ServerRolePrimary)
	require.NoError(t, r.ReconcileAll(context.Background()))
	agent.mu.Lock()
	defer agent.mu.Unlock()
	require.NotEmpty(t, agent.calls, "a primary must still converge (reach the agent)")
	require.Equal(t, "domain.list", filterCallsByPrefix(agent.calls, "domain.")[0].method)
}

func TestReconcileOne_StandbyIsDormant(t *testing.T) {
	r, agent := buildGateReconciler(t, models.ServerRoleStandby)
	require.NoError(t, r.ReconcileOne(context.Background(), "domain-1"))
	agent.mu.Lock()
	defer agent.mu.Unlock()
	require.Empty(t, agent.calls, "a DR standby must not converge a single domain either")
}

// A missing settings row (or nil repo) must NOT freeze a live box: isStandby
// fails toward active primary, so ReconcileAll runs.
func TestReconcileAll_UnseededSettingsRunsAsPrimary(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	now := time.Now().UTC()
	domainRepo.domains["d1"] = &models.Domain{ID: "d1", UserID: "u1", Name: "x.example.com", IsEnabled: true, CreatedAt: now, UpdatedAt: now}
	// serverSettings repo with nil row → Get returns ErrNotFound.
	srv := &fakeServerSettingsRepo{}
	r := New(domainRepo, userRepo, agent, log, Config{Interval: 1 * time.Second}).WithDNSRepos(nil, nil, srv)
	require.NoError(t, r.ReconcileAll(context.Background()))
	agent.mu.Lock()
	defer agent.mu.Unlock()
	require.NotEmpty(t, agent.calls, "unseeded settings must be treated as primary, not park the box")
}
