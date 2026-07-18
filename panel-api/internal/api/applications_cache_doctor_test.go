package api

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-11 fleet: in-memory cache-doctor run repo for the sweep executor tests.
type memDoctorRunRepo struct {
	runs map[string]*models.CacheDoctorRun
}

func newMemDoctorRunRepo() *memDoctorRunRepo {
	return &memDoctorRunRepo{runs: map[string]*models.CacheDoctorRun{}}
}
func (m *memDoctorRunRepo) Create(_ context.Context, run *models.CacheDoctorRun) error {
	cp := *run
	m.runs[run.ID] = &cp
	return nil
}
func (m *memDoctorRunRepo) Update(_ context.Context, run *models.CacheDoctorRun) error {
	cp := *run
	m.runs[run.ID] = &cp
	return nil
}
func (m *memDoctorRunRepo) Get(_ context.Context, id string) (*models.CacheDoctorRun, error) {
	if r, ok := m.runs[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}
func (m *memDoctorRunRepo) ListRecent(_ context.Context, _ int) ([]models.CacheDoctorRun, error) {
	out := make([]models.CacheDoctorRun, 0, len(m.runs))
	for _, r := range m.runs {
		out = append(out, *r)
	}
	return out, nil
}
func (m *memDoctorRunRepo) CountActive(_ context.Context) (int64, error) {
	var n int64
	for _, r := range m.runs {
		if r.State == models.CacheDoctorStateQueued || r.State == models.CacheDoctorStateRunning {
			n++
		}
	}
	return n, nil
}

func doctorTestHandler(t *testing.T, ag *mockAgent) (*wordPressHandler, *memDoctorRunRepo, *mockWordPressInstallRepo) {
	t.Helper()
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	runs := newMemDoctorRunRepo()
	h := &wordPressHandler{cfg: ApplicationHandlerConfig{
		ApplicationInstalls: wpRepo,
		Domains:             domainRepo,
		Users:               userRepo,
		Agent:               ag,
		CacheDoctorRuns:     runs,
	}}
	return h, runs, wpRepo
}

func seedDoctorCacheInstall(wpRepo *mockWordPressInstallRepo, id string) {
	wpRepo.installs = map[string]*models.WordPressInstall{
		id: {ID: id, UserID: "user1", DomainID: "domain1", AppType: "wordpress", CacheEnabled: true, Status: "ready"},
	}
}

func TestCacheDoctorSweep_DetectsDrift(t *testing.T) {
	ag := &mockAgent{}
	ag.callFn = func(_ context.Context, command string, _ any) (json.RawMessage, error) {
		if command == "wordpress.cache_health" {
			return json.RawMessage(`{"healthy": false, "detail": "plugin inactive"}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	h, runs, wpRepo := doctorTestHandler(t, ag)
	seedDoctorCacheInstall(wpRepo, "inst1")

	run := &models.CacheDoctorRun{ID: "run1", Kind: models.CacheDoctorKindDoctor, State: models.CacheDoctorStateQueued}
	_ = runs.Create(context.Background(), run)
	h.runCacheDoctorSweep("run1", models.CacheDoctorKindDoctor, "admin1")

	got, _ := runs.Get(context.Background(), "run1")
	if got.State != models.CacheDoctorStateDone {
		t.Fatalf("state = %q, want done", got.State)
	}
	if got.Total != 1 || got.Checked != 1 || got.Drifted != 1 {
		t.Errorf("counts total/checked/drifted = %d/%d/%d, want 1/1/1", got.Total, got.Checked, got.Drifted)
	}
	if got.Repaired != 0 {
		t.Errorf("doctor kind must not repair, got repaired=%d", got.Repaired)
	}
	var items []models.CacheDoctorItem
	if err := json.Unmarshal(got.Results, &items); err != nil || len(items) != 1 {
		t.Fatalf("results: %v (%d items)", err, len(items))
	}
	if !items[0].Drifted || items[0].InstallID != "inst1" {
		t.Errorf("item wrong: %+v", items[0])
	}
}

func TestCacheDoctorSweep_HealthySkipsDrift(t *testing.T) {
	ag := &mockAgent{}
	ag.callFn = func(_ context.Context, command string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"healthy": true, "detail": "ok"}`), nil
	}
	h, runs, wpRepo := doctorTestHandler(t, ag)
	seedDoctorCacheInstall(wpRepo, "inst1")

	run := &models.CacheDoctorRun{ID: "run2", Kind: models.CacheDoctorKindDoctor, State: models.CacheDoctorStateQueued}
	_ = runs.Create(context.Background(), run)
	h.runCacheDoctorSweep("run2", models.CacheDoctorKindDoctor, "admin1")

	got, _ := runs.Get(context.Background(), "run2")
	if got.Drifted != 0 || got.Failed != 0 {
		t.Errorf("healthy install: drifted/failed = %d/%d, want 0/0", got.Drifted, got.Failed)
	}
}

func TestCacheDoctorSweep_RefreshUpdatesPlugin(t *testing.T) {
	ag := &mockAgent{}
	var refreshCalls int
	ag.callFn = func(_ context.Context, command string, _ any) (json.RawMessage, error) {
		if command == "wordpress.cache_plugin_refresh" {
			refreshCalls++
			return json.RawMessage(`{"updated": true}`), nil
		}
		return json.RawMessage(`{}`), nil
	}
	h, runs, wpRepo := doctorTestHandler(t, ag)
	seedDoctorCacheInstall(wpRepo, "inst1")

	run := &models.CacheDoctorRun{ID: "run3", Kind: models.CacheDoctorKindRefresh, State: models.CacheDoctorStateQueued}
	_ = runs.Create(context.Background(), run)
	h.runCacheDoctorSweep("run3", models.CacheDoctorKindRefresh, "admin1")

	got, _ := runs.Get(context.Background(), "run3")
	if got.Refreshed != 1 || refreshCalls != 1 {
		t.Errorf("refresh: refreshed=%d calls=%d, want 1/1", got.Refreshed, refreshCalls)
	}
}
