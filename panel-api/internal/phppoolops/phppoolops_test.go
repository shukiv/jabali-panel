package phppoolops_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/phppoolops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ---- ResolveCreateTuning: the one create-side contract matrix both the HTTP
// handler and the CLI create route through (JAB-360 AC 4). ----

func TestResolveCreateTuning_Matrix(t *testing.T) {
	tests := []struct {
		name    string
		isAdmin bool
		in      phppoolops.CreateTuning
		wantOK  bool
		field   string
		// spot-checks on the resolved output (only asserted when wantOK)
		mode        string
		maxChildren uint32
		idle        uint32
	}{
		{
			name: "defaults fill mode/max_children/idle", isAdmin: false,
			in:     phppoolops.CreateTuning{},
			wantOK: true, mode: "ondemand", maxChildren: 20, idle: 60,
		},
		{
			name: "user cap boundary 100 ok", isAdmin: false,
			in:     phppoolops.CreateTuning{PmMaxChildren: 100},
			wantOK: true, mode: "ondemand", maxChildren: 100, idle: 60,
		},
		{
			name: "user cap rejects 101", isAdmin: false,
			in:     phppoolops.CreateTuning{PmMaxChildren: 101},
			wantOK: false, field: "pm_max_children_too_high",
		},
		{
			name: "admin allows 500 (above user cap)", isAdmin: true,
			in:     phppoolops.CreateTuning{PmMaxChildren: 500},
			wantOK: true, mode: "ondemand", maxChildren: 500, idle: 60,
		},
		{
			name: "admin cap rejects 2001", isAdmin: true,
			in:     phppoolops.CreateTuning{PmMaxChildren: 2001},
			wantOK: false, field: "pm_max_children_too_high",
		},
		{
			name: "invalid pm_mode", isAdmin: true,
			in:     phppoolops.CreateTuning{PmMode: "turbo"},
			wantOK: false, field: "invalid_pm_mode",
		},
		{
			name: "idle timeout ceiling exceeded", isAdmin: true,
			in:     phppoolops.CreateTuning{ProcessIdleTimeoutSeconds: phppoolops.MaxIdleTimeoutSec + 1},
			wantOK: false, field: "process_idle_timeout_too_high",
		},
		{
			name: "pm_max_requests ceiling exceeded", isAdmin: true,
			in:     phppoolops.CreateTuning{PmMaxRequests: phppoolops.MaxRequestsCap + 1},
			wantOK: false, field: "pm_tuning_invalid",
		},
		{
			name: "request_terminate ceiling exceeded", isAdmin: true,
			in:     phppoolops.CreateTuning{RequestTerminateTimeoutSeconds: phppoolops.MaxTerminateSec + 1},
			wantOK: false, field: "pm_tuning_invalid",
		},
		{
			name: "dynamic ordering invalid (start > max_spare)", isAdmin: true,
			in:     phppoolops.CreateTuning{PmMode: "dynamic", PmMaxChildren: 2, PmStartServers: 5},
			wantOK: false, field: "pm_tuning_invalid",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, msg, field, ok := phppoolops.ResolveCreateTuning(tc.isAdmin, tc.in)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Empty(t, field)
				assert.Empty(t, msg)
				assert.Equal(t, tc.mode, out.PmMode)
				assert.Equal(t, tc.maxChildren, out.PmMaxChildren)
				assert.Equal(t, tc.idle, out.ProcessIdleTimeoutSeconds)
			} else {
				assert.Equal(t, tc.field, field, "field key")
				assert.NotEmpty(t, msg, "a client detail message")
			}
		})
	}
}

// TestResolveCreateTuning_DynamicFillsDefaults: dynamic mode with only
// max_children set fills start/min-spare/max-spare within the FPM ordering.
func TestResolveCreateTuning_DynamicFillsDefaults(t *testing.T) {
	out, _, _, ok := phppoolops.ResolveCreateTuning(true, phppoolops.CreateTuning{PmMode: "dynamic", PmMaxChildren: 10})
	require.True(t, ok)
	assert.Equal(t, uint32(3), out.PmMaxSpareServers)
	assert.Equal(t, uint32(2), out.PmStartServers)
	assert.Equal(t, uint32(1), out.PmMinSpareServers)
	assert.True(t, out.PmMinSpareServers <= out.PmStartServers &&
		out.PmStartServers <= out.PmMaxSpareServers &&
		out.PmMaxSpareServers <= out.PmMaxChildren)
}

// ---- ReconcileViaAgent: slug/additive + full payload parity ----

type fakeAgent struct {
	command string
	params  map[string]any
	err     error
}

func (f *fakeAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	f.command = command
	if m, ok := params.(map[string]any); ok {
		f.params = m
	}
	return nil, f.err
}

type fakeUsers struct {
	repository.UserRepository
	user *models.User
}

func (f *fakeUsers) FindByID(_ context.Context, _ string) (*models.User, error) { return f.user, nil }

type fakeOverrides struct {
	repository.PHPPoolIniOverrideRepository
	list []models.PHPPoolIniOverride
}

func (f *fakeOverrides) ListByPool(_ context.Context, _ string) ([]models.PHPPoolIniOverride, error) {
	return f.list, nil
}

type setStatusCall struct {
	status  string
	lastErr *string
}

type fakePools struct {
	repository.PHPPoolRepository
	list     []models.PHPPool
	statuses []setStatusCall
}

func (f *fakePools) ListByUserID(_ context.Context, _ string) ([]models.PHPPool, error) {
	return f.list, nil
}

// Update is the terminal status write ReconcileViaAgent makes (whole row, as
// the HTTP reconcile it replaces always did) — capture the status + last_error.
func (f *fakePools) Update(_ context.Context, p *models.PHPPool) error {
	f.statuses = append(f.statuses, setStatusCall{status: p.Status, lastErr: p.LastError})
	return nil
}

type fakePackages struct {
	repository.PackageRepository
	pkg *models.HostingPackage
}

func (f *fakePackages) FindByID(_ context.Context, _ string) (*models.HostingPackage, error) {
	return f.pkg, nil
}

func username(s string) *string { return &s }

func pkgID(s string) *string { return &s }

// GH #1422: the settings/version reconcile path must carry the GH #402 exec
// opt-out. An exec-enabled package sends disable_functions=""; anything else
// (flag off, no package, or — the fail-closed case — no Packages wiring) MUST
// leave the key ABSENT so the agent keeps the #401 command-exec lockdown.
func TestReconcileViaAgent_DisableFunctionsByPackage(t *testing.T) {
	pool := models.PHPPool{ID: "P1", UserID: "U1", PHPVersion: "8.3", PmMode: "ondemand"}
	pkgRef := "pkg-1"
	execUser := func() *models.User {
		return &models.User{ID: "U1", Username: username("alice"), PackageID: pkgID(pkgRef)}
	}

	t.Run("exec-enabled package sends empty opt-out", func(t *testing.T) {
		ag := &fakeAgent{}
		err := phppoolops.ReconcileViaAgent(phppoolops.ReconcileDeps{
			Agent:     ag,
			Users:     &fakeUsers{user: execUser()},
			Overrides: &fakeOverrides{},
			Pools:     &fakePools{list: []models.PHPPool{pool}},
			Packages:  &fakePackages{pkg: &models.HostingPackage{ID: pkgRef, PHPExecEnabled: true}},
		}, pool)
		require.NoError(t, err)
		v, ok := ag.params["disable_functions"]
		require.True(t, ok, "exec-enabled package must send the disable_functions key")
		assert.Equal(t, "", v, "exec-enabled package opts out with an empty value")
	})

	t.Run("non-exec package omits the key", func(t *testing.T) {
		ag := &fakeAgent{}
		err := phppoolops.ReconcileViaAgent(phppoolops.ReconcileDeps{
			Agent:     ag,
			Users:     &fakeUsers{user: execUser()},
			Overrides: &fakeOverrides{},
			Pools:     &fakePools{list: []models.PHPPool{pool}},
			Packages:  &fakePackages{pkg: &models.HostingPackage{ID: pkgRef, PHPExecEnabled: false}},
		}, pool)
		require.NoError(t, err)
		_, ok := ag.params["disable_functions"]
		assert.False(t, ok, "a non-exec package must NOT send the key (agent applies its safe default)")
	})

	t.Run("nil Packages is fail-closed", func(t *testing.T) {
		ag := &fakeAgent{}
		err := phppoolops.ReconcileViaAgent(phppoolops.ReconcileDeps{
			Agent:     ag,
			Users:     &fakeUsers{user: execUser()}, // package set, but no repo to resolve it
			Overrides: &fakeOverrides{},
			Pools:     &fakePools{list: []models.PHPPool{pool}},
			// Packages omitted on purpose.
		}, pool)
		require.NoError(t, err)
		_, ok := ag.params["disable_functions"]
		assert.False(t, ok, "missing Packages wiring must never lift the lockdown")
	})
}

func TestReconcileViaAgent_DefaultPool_UsesBareUsernameSlug(t *testing.T) {
	ag := &fakeAgent{}
	pool := models.PHPPool{
		ID: "P1", UserID: "U1", PHPVersion: "8.3", PmMode: "ondemand",
		PmMaxChildren: 25, ProcessIdleTimeoutSeconds: 60, PmStartServers: 2,
		PmMinSpareServers: 1, PmMaxSpareServers: 3, PmMaxRequests: 500,
		RequestTerminateTimeoutSeconds: 30, SlowlogTimeoutSeconds: 10,
		ExtraExtensions: models.StringList{"redis"}, XdebugEnabled: true,
	}
	pools := &fakePools{list: []models.PHPPool{pool}} // pool is the user's only/first → default

	err := phppoolops.ReconcileViaAgent(phppoolops.ReconcileDeps{
		Agent:     ag,
		Users:     &fakeUsers{user: &models.User{ID: "U1", Username: username("alice")}},
		Overrides: &fakeOverrides{},
		Pools:     pools,
	}, pool)
	require.NoError(t, err)

	assert.Equal(t, "php.pool.apply", ag.command)
	assert.Equal(t, "alice", ag.params["slug"], "default pool keeps the bare username slug")
	assert.Equal(t, false, ag.params["additive"])
	// The full pm.* + slowlog + extensions + xdebug model must be on the wire —
	// the exact fields the CLI's old hand-copied reconcile dropped.
	assert.Equal(t, uint32(2), ag.params["pm_start_servers"])
	assert.Equal(t, uint32(1), ag.params["pm_min_spare_servers"])
	assert.Equal(t, uint32(3), ag.params["pm_max_spare_servers"])
	assert.Equal(t, uint32(500), ag.params["pm_max_requests"])
	assert.Equal(t, uint32(30), ag.params["request_terminate_timeout_seconds"])
	assert.Equal(t, uint32(10), ag.params["slowlog_timeout_seconds"])
	assert.Equal(t, []string{"redis"}, ag.params["extra_extensions"])
	assert.Equal(t, true, ag.params["xdebug_enabled"])
	require.Len(t, pools.statuses, 1)
	assert.Equal(t, "ready", pools.statuses[0].status)
	assert.Nil(t, pools.statuses[0].lastErr)
}

// TestReconcileViaAgent_NonDefaultPool_UsesVersionedSlug is the load-bearing
// bug JAB-360 fixes: a mutation on a per-version (non-default) pool must apply
// to that pool's own versioned socket, not the user's default one. The CLI's
// old reconcile sent no slug at all.
func TestReconcileViaAgent_NonDefaultPool_UsesVersionedSlug(t *testing.T) {
	ag := &fakeAgent{}
	def := models.PHPPool{ID: "P1", UserID: "U1", PHPVersion: "8.1"}
	target := models.PHPPool{ID: "P2", UserID: "U1", PHPVersion: "8.2", PmMode: "ondemand"}
	pools := &fakePools{list: []models.PHPPool{def, target}} // first (created_at ASC) is P1 → target is NOT default

	err := phppoolops.ReconcileViaAgent(phppoolops.ReconcileDeps{
		Agent:     ag,
		Users:     &fakeUsers{user: &models.User{ID: "U1", Username: username("alice")}},
		Overrides: &fakeOverrides{},
		Pools:     pools,
	}, target)
	require.NoError(t, err)
	assert.Equal(t, "alice-php8.2", ag.params["slug"], "per-version pool applies to its own socket")
	assert.Equal(t, true, ag.params["additive"])
}

// TestReconcileViaAgent_OverridesSplitByKind: value vs flag overrides land in
// the right agent bucket.
func TestReconcileViaAgent_OverridesSplitByKind(t *testing.T) {
	ag := &fakeAgent{}
	pool := models.PHPPool{ID: "P1", UserID: "U1", PHPVersion: "8.3", PmMode: "ondemand"}
	err := phppoolops.ReconcileViaAgent(phppoolops.ReconcileDeps{
		Agent: ag,
		Users: &fakeUsers{user: &models.User{ID: "U1", Username: username("alice")}},
		Overrides: &fakeOverrides{list: []models.PHPPoolIniOverride{
			{Directive: "memory_limit", Value: "256M", Kind: "value"},
			{Directive: "display_errors", Value: "off", Kind: "flag"},
		}},
		Pools: &fakePools{list: []models.PHPPool{pool}},
	}, pool)
	require.NoError(t, err)
	assert.Equal(t, []map[string]string{{"name": "memory_limit", "value": "256M"}}, ag.params["admin_values"])
	assert.Equal(t, []map[string]string{{"name": "display_errors", "value": "off"}}, ag.params["admin_flags"])
}

// TestReconcileViaAgent_AgentError_SetsErrorStatus: a failed apply returns the
// agent error and records status=error so the caller (CLI) can surface it.
func TestReconcileViaAgent_AgentError_SetsErrorStatus(t *testing.T) {
	agentErr := errors.New("socket down")
	ag := &fakeAgent{err: agentErr}
	pool := models.PHPPool{ID: "P1", UserID: "U1", PHPVersion: "8.3", PmMode: "ondemand"}
	pools := &fakePools{list: []models.PHPPool{pool}}
	err := phppoolops.ReconcileViaAgent(phppoolops.ReconcileDeps{
		Agent:     ag,
		Users:     &fakeUsers{user: &models.User{ID: "U1", Username: username("alice")}},
		Overrides: &fakeOverrides{},
		Pools:     pools,
	}, pool)
	require.ErrorIs(t, err, agentErr)
	require.Len(t, pools.statuses, 1)
	assert.Equal(t, "error", pools.statuses[0].status)
	require.NotNil(t, pools.statuses[0].lastErr)
	assert.Contains(t, *pools.statuses[0].lastErr, "socket down")
}
