package api

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func strPtr(s string) *string { return &s }

// WordPress-specific mock repositories

type mockWordPressInstallRepo struct {
	mu       sync.RWMutex
	installs map[string]*models.WordPressInstall
	byDomain map[string]*models.WordPressInstall
}

func (m *mockWordPressInstallRepo) Create(ctx context.Context, inst *models.WordPressInstall) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.installs == nil {
		m.installs = make(map[string]*models.WordPressInstall)
	}
	if m.byDomain == nil {
		m.byDomain = make(map[string]*models.WordPressInstall)
	}
	m.installs[inst.ID] = inst
	m.byDomain[inst.DomainID] = inst
	return nil
}

func (m *mockWordPressInstallRepo) FindByID(ctx context.Context, id string) (*models.WordPressInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inst, ok := m.installs[id]; ok {
		return inst, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockWordPressInstallRepo) FindByIDAndUserID(ctx context.Context, id, userID string) (*models.WordPressInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	inst, ok := m.installs[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if inst.UserID != userID {
		return nil, repository.ErrNotFound
	}
	return inst, nil
}

func (m *mockWordPressInstallRepo) FindByDomainID(ctx context.Context, domainID string) (*models.WordPressInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if inst, ok := m.byDomain[domainID]; ok {
		return inst, nil
	}
	return nil, repository.ErrNotFound
}

// ListByDomainIDs — GH #1543 domains-list app summary. Returns every seeded
// install whose domain_id is in the set, docroot-first, mirroring the repo's
// order so the api tests that assert the summary get a stable shape.
func (m *mockWordPressInstallRepo) ListByDomainIDs(_ context.Context, domainIDs []string) ([]models.ApplicationInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	want := make(map[string]struct{}, len(domainIDs))
	for _, id := range domainIDs {
		want[id] = struct{}{}
	}
	var out []models.ApplicationInstall
	for _, inst := range m.byDomain {
		if inst == nil {
			continue
		}
		if _, ok := want[inst.DomainID]; ok {
			out = append(out, *inst)
		}
	}
	return out, nil
}

func (m *mockWordPressInstallRepo) FindByDomainAndSubdirectory(ctx context.Context, domainID, subdirectory string) (*models.WordPressInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.installs {
		if inst.DomainID == domainID && inst.Subdirectory == subdirectory {
			return inst, nil
		}
	}
	return nil, repository.ErrNotFound
}

// FindByDomainAndSubdirectoryAndAppType — added for the M19 generalisation.
// Pre-M19 rows had no AppType column; the model defaults to "wordpress" so
// the legacy WP test fixtures match an app_type="wordpress" query without
// each test having to set the field explicitly.
func (m *mockWordPressInstallRepo) FindByDomainAndSubdirectoryAndAppType(ctx context.Context, domainID, subdirectory, appType string) (*models.WordPressInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.installs {
		instAppType := inst.AppType
		if instAppType == "" {
			instAppType = "wordpress"
		}
		if inst.DomainID == domainID && inst.Subdirectory == subdirectory && instAppType == appType {
			return inst, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockWordPressInstallRepo) FindByDBID(ctx context.Context, dbID string) (*models.WordPressInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, inst := range m.installs {
		if inst.DBIDOr() == dbID {
			return inst, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockWordPressInstallRepo) List(ctx context.Context, opts repository.ListOptions) ([]models.WordPressInstall, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.WordPressInstall
	for _, inst := range m.installs {
		result = append(result, *inst)
	}
	return result, int64(len(result)), nil
}

func (m *mockWordPressInstallRepo) ListByUserID(ctx context.Context, userID string, opts repository.ListOptions) ([]models.WordPressInstall, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []models.WordPressInstall
	for _, inst := range m.installs {
		if inst.UserID == userID {
			result = append(result, *inst)
		}
	}
	return result, int64(len(result)), nil
}

func (m *mockWordPressInstallRepo) UpdateCacheEnabled(ctx context.Context, id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.installs[id]; ok {
		inst.CacheEnabled = enabled
		inst.UpdatedAt = time.Now()
	}
	return nil
}

func (m *mockWordPressInstallRepo) UpdateCacheSettings(ctx context.Context, id string, raw json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.installs[id]; ok {
		inst.CacheSettings = raw
		inst.UpdatedAt = time.Now()
	}
	return nil
}

func (m *mockWordPressInstallRepo) ListCacheEnabledByDomainID(_ context.Context, domainID string) ([]models.ApplicationInstall, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []models.ApplicationInstall
	for _, inst := range m.installs {
		if inst.DomainID == domainID && inst.AppType == "wordpress" && inst.CacheEnabled {
			out = append(out, *inst)
		}
	}
	return out, nil
}

func (m *mockWordPressInstallRepo) ListAllCacheEnabled(_ context.Context) ([]models.ApplicationInstall, error) {
	return nil, nil
}

func (m *mockWordPressInstallRepo) CountCacheEnabledByUserID(_ context.Context, userID, excludeID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, inst := range m.installs {
		if inst.UserID == userID && inst.AppType == "wordpress" && inst.CacheEnabled && inst.ID != excludeID {
			n++
		}
	}
	return n, nil
}

func (m *mockWordPressInstallRepo) CountCacheEnabledByDomainID(_ context.Context, domainID, excludeID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, inst := range m.installs {
		if inst.DomainID == domainID && inst.AppType == "wordpress" && inst.CacheEnabled && inst.ID != excludeID {
			n++
		}
	}
	return n, nil
}

func (m *mockWordPressInstallRepo) UpdateStatus(ctx context.Context, id, status string, lastError *string, version *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.installs[id]; ok {
		inst.Status = status
		if lastError != nil {
			inst.LastError = *lastError
		}
		inst.Version = version
		inst.UpdatedAt = time.Now()
		return nil
	}
	return repository.ErrNotFound
}

func (m *mockWordPressInstallRepo) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, ok := m.installs[id]; ok {
		delete(m.byDomain, inst.DomainID)
		delete(m.installs, id)
		return nil
	}
	return repository.ErrNotFound
}

// GetByIDUnsafe returns the install without locking (for tests that need quick reads)
// This is only safe if called immediately after an operation or if the caller ensures no concurrent access
func (m *mockWordPressInstallRepo) GetByIDUnsafe(id string) *models.WordPressInstall {
	if inst, ok := m.installs[id]; ok {
		return inst
	}
	return nil
}

func (m *mockWordPressInstallRepo) ListReadyByUpdatedAtAsc(_ context.Context, _ int) ([]models.ApplicationInstall, error) {
	return nil, nil
}

// Test helper

type mockDatabaseGrantRepo struct {
	grants map[string]*models.DatabaseUserGrant
}

func (m *mockDatabaseGrantRepo) Create(ctx context.Context, g *models.DatabaseUserGrant) error {
	if m.grants == nil {
		m.grants = make(map[string]*models.DatabaseUserGrant)
	}
	m.grants[g.ID] = g
	return nil
}

func (m *mockDatabaseGrantRepo) Delete(ctx context.Context, id string) error {
	if _, ok := m.grants[id]; ok {
		delete(m.grants, id)
		return nil
	}
	return repository.ErrNotFound
}

func (m *mockDatabaseGrantRepo) FindByID(ctx context.Context, id string) (*models.DatabaseUserGrant, error) {
	if g, ok := m.grants[id]; ok {
		return g, nil
	}
	return nil, repository.ErrNotFound
}

func (m *mockDatabaseGrantRepo) ListByDatabaseID(ctx context.Context, databaseID string) ([]models.DatabaseUserGrant, error) {
	return nil, nil
}

func (m *mockDatabaseGrantRepo) ListByDatabaseUserID(ctx context.Context, databaseUserID string) ([]models.DatabaseUserGrant, error) {
	return nil, nil
}

func (m *mockDatabaseGrantRepo) ListByDatabaseUserIDs(ctx context.Context, databaseUserIDs []string) ([]models.DatabaseUserGrant, error) {
	return nil, nil
}

func (m *mockDatabaseGrantRepo) UpdateLevel(ctx context.Context, id string, level string) error {
	return nil
}

func (m *mockDatabaseGrantRepo) UpdatePrivileges(ctx context.Context, id string, privileges string) error {
	return nil
}

func (m *mockDatabaseGrantRepo) FindByDBAndDBUser(ctx context.Context, databaseID string, databaseUserID string) (*models.DatabaseUserGrant, error) {
	return nil, repository.ErrNotFound
}

// Tests
