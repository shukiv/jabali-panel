package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newTestLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// mockWordPressInstallRepo is a minimal mock for testing.
type mockWordPressInstallRepo struct {
	installs    []models.WordPressInstall
	ready       []models.WordPressInstall // returned by ListReadyByUpdatedAtAsc
	updateCalls []struct {
		id      string
		status  string
		lastErr *string
		version *string
	}
}

func (m *mockWordPressInstallRepo) Create(ctx context.Context, install *models.WordPressInstall) error {
	return nil
}

func (m *mockWordPressInstallRepo) FindByID(ctx context.Context, id string) (*models.WordPressInstall, error) {
	return nil, repository.ErrNotFound
}

func (m *mockWordPressInstallRepo) FindByIDAndUserID(ctx context.Context, id, userID string) (*models.WordPressInstall, error) {
	return nil, repository.ErrNotFound
}

func (m *mockWordPressInstallRepo) FindByDomainID(ctx context.Context, domainID string) (*models.WordPressInstall, error) {
	return nil, repository.ErrNotFound
}

// FindByDomainAndSubdirectory — added for multi-install-per-domain
// (migration 000045). Reconciler tests don't exercise subdir lookups;
// return the same not-found signal as the other Find stubs.
func (m *mockWordPressInstallRepo) FindByDomainAndSubdirectory(ctx context.Context, domainID, subdirectory string) (*models.WordPressInstall, error) {
	return nil, repository.ErrNotFound
}

// FindByDomainAndSubdirectoryAndAppType — added for the M19 generalisation
// (migration 000046). Reconciler tests don't exercise per-app lookups
// either; same not-found stub.
func (m *mockWordPressInstallRepo) FindByDomainAndSubdirectoryAndAppType(ctx context.Context, domainID, subdirectory, appType string) (*models.WordPressInstall, error) {
	return nil, repository.ErrNotFound
}

func (m *mockWordPressInstallRepo) FindByDBID(ctx context.Context, dbID string) (*models.WordPressInstall, error) {
	return nil, repository.ErrNotFound
}

func (m *mockWordPressInstallRepo) ListByUserID(ctx context.Context, userID string, opts repository.ListOptions) ([]models.WordPressInstall, int64, error) {
	return nil, 0, nil
}

func (m *mockWordPressInstallRepo) List(ctx context.Context, opts repository.ListOptions) ([]models.WordPressInstall, int64, error) {
	return m.installs, int64(len(m.installs)), nil
}

func (m *mockWordPressInstallRepo) ListReadyByUpdatedAtAsc(_ context.Context, _ int) ([]models.WordPressInstall, error) {
	return m.ready, nil
}

func (m *mockWordPressInstallRepo) CountCacheEnabledByUserID(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}

func (m *mockWordPressInstallRepo) CountCacheEnabledByDomainID(_ context.Context, _, _ string) (int64, error) {
	return 0, nil
}

func (m *mockWordPressInstallRepo) UpdateCacheEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}

func (m *mockWordPressInstallRepo) UpdateCacheSettings(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}

func (m *mockWordPressInstallRepo) ListCacheEnabledByDomainID(_ context.Context, _ string) ([]models.ApplicationInstall, error) {
	return nil, nil
}

func (m *mockWordPressInstallRepo) ListAllCacheEnabled(_ context.Context) ([]models.ApplicationInstall, error) {
	return nil, nil
}

func (m *mockWordPressInstallRepo) UpdateStatus(ctx context.Context, id, status string, lastError *string, version *string) error {
	m.updateCalls = append(m.updateCalls, struct {
		id      string
		status  string
		lastErr *string
		version *string
	}{id, status, lastError, version})
	return nil
}

func (m *mockWordPressInstallRepo) Delete(ctx context.Context, id string) error {
	return nil
}

func TestReconcileWordPressInstalls_StuckInstallingTimeout(t *testing.T) {
	now := time.Now()

	installs := []models.WordPressInstall{
		{
			ID:        "install1",
			Status:    "installing",
			UpdatedAt: now.Add(-15 * time.Minute), // 15m old, > 10m timeout
		},
	}

	mock := &mockWordPressInstallRepo{installs: installs}

	r := &Reconciler{
		wordPressInstalls: mock,
		cfg: &config.Config{
			WordPress: config.WordPressConfig{
				InstallTimeout: 10 * time.Minute,
				CloneTimeout:   30 * time.Minute,
				DeleteTimeout:  5 * time.Minute,
				ProbeBatch:     100,
			},
		},
		log: newTestLogger(t),
	}

	ctx := context.Background()
	r.reconcileWordPressInstalls(ctx)

	if len(mock.updateCalls) != 1 {
		t.Fatalf("expected 1 update call, got %d", len(mock.updateCalls))
	}

	call := mock.updateCalls[0]
	if call.id != "install1" {
		t.Errorf("expected id install1, got %s", call.id)
	}
	if call.status != "failed" {
		t.Errorf("expected status failed, got %s", call.status)
	}
	if call.lastErr == nil {
		t.Error("expected lastErr to be set")
	}
}

func TestReconcileWordPressInstalls_StuckCloningTimeout(t *testing.T) {
	now := time.Now()

	installs := []models.WordPressInstall{
		{
			ID:        "clone1",
			Status:    "cloning",
			UpdatedAt: now.Add(-35 * time.Minute), // 35m old, > 30m timeout
		},
	}

	mock := &mockWordPressInstallRepo{installs: installs}

	r := &Reconciler{
		wordPressInstalls: mock,
		cfg: &config.Config{
			WordPress: config.WordPressConfig{
				InstallTimeout: 10 * time.Minute,
				CloneTimeout:   30 * time.Minute,
				DeleteTimeout:  5 * time.Minute,
				ProbeBatch:     100,
			},
		},
		log: newTestLogger(t),
	}

	ctx := context.Background()
	r.reconcileWordPressInstalls(ctx)

	if len(mock.updateCalls) != 1 {
		t.Fatalf("expected 1 update call, got %d", len(mock.updateCalls))
	}

	call := mock.updateCalls[0]
	if call.status != "failed" {
		t.Errorf("expected status failed, got %s", call.status)
	}
}

func TestReconcileWordPressInstalls_StuckDeletingTimeout(t *testing.T) {
	now := time.Now()

	installs := []models.WordPressInstall{
		{
			ID:        "delete1",
			Status:    "deleting",
			UpdatedAt: now.Add(-6 * time.Minute), // 6m old, > 5m timeout
		},
	}

	mock := &mockWordPressInstallRepo{installs: installs}

	r := &Reconciler{
		wordPressInstalls: mock,
		cfg: &config.Config{
			WordPress: config.WordPressConfig{
				InstallTimeout: 10 * time.Minute,
				CloneTimeout:   30 * time.Minute,
				DeleteTimeout:  5 * time.Minute,
				ProbeBatch:     100,
			},
		},
		log: newTestLogger(t),
	}

	ctx := context.Background()
	r.reconcileWordPressInstalls(ctx)

	if len(mock.updateCalls) != 1 {
		t.Fatalf("expected 1 update call, got %d", len(mock.updateCalls))
	}

	call := mock.updateCalls[0]
	if call.status != "failed" {
		t.Errorf("expected status failed, got %s", call.status)
	}
}

func TestReconcileWordPressInstalls_WithinTimeout(t *testing.T) {
	now := time.Now()

	installs := []models.WordPressInstall{
		{
			ID:        "install2",
			Status:    "installing",
			UpdatedAt: now.Add(-5 * time.Minute), // 5m old, < 10m timeout
		},
	}

	mock := &mockWordPressInstallRepo{installs: installs}

	r := &Reconciler{
		wordPressInstalls: mock,
		cfg: &config.Config{
			WordPress: config.WordPressConfig{
				InstallTimeout: 10 * time.Minute,
				CloneTimeout:   30 * time.Minute,
				DeleteTimeout:  5 * time.Minute,
				ProbeBatch:     100,
			},
		},
		log: newTestLogger(t),
	}

	ctx := context.Background()
	r.reconcileWordPressInstalls(ctx)

	if len(mock.updateCalls) != 0 {
		t.Fatalf("expected 0 update calls, got %d", len(mock.updateCalls))
	}
}

func TestReconcileWordPressInstalls_ReadyNoOpWithoutProbe(t *testing.T) {
	// Ready installs should not be touched until probing is implemented.
	now := time.Now()

	installs := []models.WordPressInstall{
		{
			ID:        "ready1",
			Status:    "ready",
			Version:   stringPtr("6.2.0"),
			UpdatedAt: now.Add(-1 * time.Hour),
		},
	}

	mock := &mockWordPressInstallRepo{installs: installs}

	r := &Reconciler{
		wordPressInstalls: mock,
		cfg: &config.Config{
			WordPress: config.WordPressConfig{
				InstallTimeout: 10 * time.Minute,
				CloneTimeout:   30 * time.Minute,
				DeleteTimeout:  5 * time.Minute,
				ProbeBatch:     100,
			},
		},
		log: newTestLogger(t),
	}

	ctx := context.Background()
	r.reconcileWordPressInstalls(ctx)

	// No updates should be made to ready installs yet (probing stubbed).
	if len(mock.updateCalls) != 0 {
		t.Fatalf("expected 0 update calls for ready installs, got %d", len(mock.updateCalls))
	}
}

func TestReconcileWordPressInstalls_ProbeBatchLimit(t *testing.T) {
	now := time.Now()

	// Create 150 ready installs to test batch limit of 100.
	installs := make([]models.WordPressInstall, 150)
	for i := 0; i < 150; i++ {
		installs[i] = models.WordPressInstall{
			ID:        "ready" + string(rune(i)),
			Status:    "ready",
			UpdatedAt: now.Add(time.Duration(-i) * time.Minute),
		}
	}

	mock := &mockWordPressInstallRepo{installs: installs}

	r := &Reconciler{
		wordPressInstalls: mock,
		cfg: &config.Config{
			WordPress: config.WordPressConfig{
				InstallTimeout: 10 * time.Minute,
				CloneTimeout:   30 * time.Minute,
				DeleteTimeout:  5 * time.Minute,
				ProbeBatch:     100,
			},
		},
		log: newTestLogger(t),
	}

	ctx := context.Background()
	r.reconcileWordPressInstalls(ctx)

	// No updates yet (probing stubbed), but the logic should handle batch limits.
	// When probing is implemented, verify that at most 100 probes happen per tick.
	if len(mock.updateCalls) != 0 {
		t.Fatalf("expected 0 update calls with stub probing, got %d", len(mock.updateCalls))
	}
}

func TestReconcileWordPressInstalls_MultipleStuckRows(t *testing.T) {
	now := time.Now()

	installs := []models.WordPressInstall{
		{
			ID:        "install_old",
			Status:    "installing",
			UpdatedAt: now.Add(-15 * time.Minute),
		},
		{
			ID:        "clone_old",
			Status:    "cloning",
			UpdatedAt: now.Add(-40 * time.Minute),
		},
		{
			ID:        "delete_old",
			Status:    "deleting",
			UpdatedAt: now.Add(-7 * time.Minute),
		},
		{
			ID:        "install_fresh",
			Status:    "installing",
			UpdatedAt: now.Add(-2 * time.Minute),
		},
	}

	mock := &mockWordPressInstallRepo{installs: installs}

	r := &Reconciler{
		wordPressInstalls: mock,
		cfg: &config.Config{
			WordPress: config.WordPressConfig{
				InstallTimeout: 10 * time.Minute,
				CloneTimeout:   30 * time.Minute,
				DeleteTimeout:  5 * time.Minute,
				ProbeBatch:     100,
			},
		},
		log: newTestLogger(t),
	}

	ctx := context.Background()
	r.reconcileWordPressInstalls(ctx)

	if len(mock.updateCalls) != 3 {
		t.Fatalf("expected 3 update calls, got %d", len(mock.updateCalls))
	}

	// Verify the three stuck ones were marked failed.
	idSet := make(map[string]bool)
	for _, call := range mock.updateCalls {
		if call.status != "failed" {
			t.Errorf("expected status failed, got %s for id %s", call.status, call.id)
		}
		idSet[call.id] = true
	}

	if !idSet["install_old"] || !idSet["clone_old"] || !idSet["delete_old"] {
		t.Error("expected stuck rows to be updated")
	}
	if idSet["install_fresh"] {
		t.Error("did not expect fresh install to be updated")
	}
}

func stringPtr(s string) *string {
	return &s
}
