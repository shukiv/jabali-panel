package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ---- in-memory fakes for the tenant multi-schedule handler tests (GH #454 7B) ----

// schedStore is a tiny stateful BackupScheduleRepository: enough of the interface
// for the /me/backup-schedules handlers, shared between two per-user routers so a
// cross-tenant access attempt can be exercised against a real stored row.
type schedStore struct {
	repository.BackupScheduleRepository
	rows  map[string]*models.BackupSchedule
	dests map[string][]string
	users map[string][]string
}

func newSchedStore() *schedStore {
	return &schedStore{rows: map[string]*models.BackupSchedule{}, dests: map[string][]string{}, users: map[string][]string{}}
}

func (s *schedStore) Create(_ context.Context, m *models.BackupSchedule) error {
	cp := *m
	s.rows[m.ID] = &cp
	return nil
}
func (s *schedStore) Get(_ context.Context, id string) (*models.BackupSchedule, error) {
	if r, ok := s.rows[id]; ok {
		return r, nil
	}
	return nil, repository.ErrNotFound
}
func (s *schedStore) ListForUser(_ context.Context, userID string) ([]models.BackupSchedule, error) {
	var out []models.BackupSchedule
	for _, r := range s.rows {
		if r.UserID != nil && *r.UserID == userID {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (s *schedStore) Update(_ context.Context, m *models.BackupSchedule) error {
	if _, ok := s.rows[m.ID]; !ok {
		return repository.ErrNotFound
	}
	cp := *m
	s.rows[m.ID] = &cp
	return nil
}
func (s *schedStore) Delete(_ context.Context, id string) error {
	if _, ok := s.rows[id]; !ok {
		return repository.ErrNotFound
	}
	delete(s.rows, id)
	return nil
}
func (s *schedStore) GetDestinations(_ context.Context, id string) ([]models.BackupDestination, error) {
	var out []models.BackupDestination
	for _, d := range s.dests[id] {
		out = append(out, models.BackupDestination{ID: d})
	}
	return out, nil
}
func (s *schedStore) ReplaceDestinations(_ context.Context, id string, d []string) error {
	s.dests[id] = d
	return nil
}
func (s *schedStore) ReplaceUsers(_ context.Context, id string, u []string) error {
	s.users[id] = u
	return nil
}

// usersMap resolves users by id so two distinct tenants can be modelled.
type usersMap struct {
	repository.UserRepository
	m map[string]*models.User
}

func (u *usersMap) FindByID(_ context.Context, id string) (*models.User, error) { return u.m[id], nil }

func schedTestPkg(cap uint32) *models.HostingPackage {
	return &models.HostingPackage{
		ID:                            "pkg1",
		MaxBackups:                    5,
		MaxBackupSchedules:            cap,
		ScheduledBackupsEnabled:       true,
		AllowedBackupDestinationKinds: "local",
	}
}

func newSchedHandler(store *schedStore, users *usersMap, pkg *models.HostingPackage, dest *models.BackupDestination) *meBackupHandler {
	return &meBackupHandler{cfg: MeBackupsHandlerConfig{
		Users:        users,
		Packages:     &fakePkgRepo{pkg: pkg},
		Schedules:    store,
		Settings:     &fakeSettingsRepo{s: &models.ServerSettings{ID: 1, TenantBackupWindowStart: "02:00", TenantBackupWindowEnd: "05:00"}},
		Destinations: fakeDestRepo{dest: dest},
	}}
}

func newSchedRouter(h *meBackupHandler, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: false})
		c.Next()
	})
	r.GET("/me/backup-schedules", h.listSchedules)
	r.POST("/me/backup-schedules", h.createSchedule)
	r.PUT("/me/backup-schedules/:id", h.updateSchedule)
	r.DELETE("/me/backup-schedules/:id", h.deleteSchedule)
	return r
}

func doReq(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	return rec
}

func pkgUser(id, pkgID string) *models.User {
	p := pkgID
	return &models.User{ID: id, Username: uname(id), IsAdmin: false, PackageID: &p}
}

func TestMeSchedules_CreateAndList(t *testing.T) {
	store := newSchedStore()
	users := &usersMap{m: map[string]*models.User{"userA": pkgUser("userA", "pkg1")}}
	h := newSchedHandler(store, users, schedTestPkg(3), nil)
	r := newSchedRouter(h, "userA")

	rec := doReq(t, r, http.MethodPost, "/me/backup-schedules", `{"enabled":false,"content":"database","cadence":"hourly"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	// The stored row must be window-governed (cadence set) and owned by userA.
	if len(store.rows) != 1 {
		t.Fatalf("want 1 stored row, got %d", len(store.rows))
	}
	for _, row := range store.rows {
		if row.Cadence != "hourly" || row.UserID == nil || *row.UserID != "userA" || row.NextRunAt == nil {
			t.Errorf("row not correctly initialised: %+v", row)
		}
	}
	rec = doReq(t, r, http.MethodGet, "/me/backup-schedules", "")
	var listResp struct {
		Data meSchedulesView `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Data.Schedules) != 1 || listResp.Data.MaxBackupSchedules != 3 {
		t.Errorf("list = %+v", listResp.Data)
	}
	if listResp.Data.WindowStart != "02:00" || listResp.Data.WindowEnd != "05:00" {
		t.Errorf("window not surfaced: %+v", listResp.Data)
	}
}

func TestMeSchedules_CapEnforced(t *testing.T) {
	store := newSchedStore()
	users := &usersMap{m: map[string]*models.User{"userA": pkgUser("userA", "pkg1")}}
	h := newSchedHandler(store, users, schedTestPkg(2), nil)
	r := newSchedRouter(h, "userA")

	body := `{"enabled":false,"content":"full","cadence":"daily"}`
	if rec := doReq(t, r, http.MethodPost, "/me/backup-schedules", body); rec.Code != http.StatusCreated {
		t.Fatalf("1st create = %d", rec.Code)
	}
	if rec := doReq(t, r, http.MethodPost, "/me/backup-schedules", body); rec.Code != http.StatusCreated {
		t.Fatalf("2nd create = %d", rec.Code)
	}
	rec := doReq(t, r, http.MethodPost, "/me/backup-schedules", body)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "schedule_limit_reached") {
		t.Fatalf("3rd create must hit the cap: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMeSchedules_InvalidCadenceRejected(t *testing.T) {
	store := newSchedStore()
	users := &usersMap{m: map[string]*models.User{"userA": pkgUser("userA", "pkg1")}}
	h := newSchedHandler(store, users, schedTestPkg(3), nil)
	r := newSchedRouter(h, "userA")

	// Empty cadence is the legacy-cron discriminator — must not be accepted here.
	for _, c := range []string{`"cadence":""`, `"cadence":"monthly"`, `"cadence":"* * * * *"`} {
		rec := doReq(t, r, http.MethodPost, "/me/backup-schedules", `{"enabled":false,"content":"full",`+c+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("cadence %s should 400, got %d", c, rec.Code)
		}
	}
	if len(store.rows) != 0 {
		t.Errorf("no row should be created for bad cadence, got %d", len(store.rows))
	}
}

func TestMeSchedules_DestinationKindGated(t *testing.T) {
	store := newSchedStore()
	users := &usersMap{m: map[string]*models.User{"userA": pkgUser("userA", "pkg1")}}
	// Package allows only "local"; the destination is "sftp" → 403.
	dest := &models.BackupDestination{ID: "d1", Kind: "sftp", Enabled: true}
	h := newSchedHandler(store, users, schedTestPkg(3), dest)
	r := newSchedRouter(h, "userA")

	rec := doReq(t, r, http.MethodPost, "/me/backup-schedules", `{"enabled":true,"content":"full","cadence":"daily","destination_id":"d1"}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "destination_not_allowed") {
		t.Fatalf("disallowed destination kind must 403: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMeSchedules_CrossTenantIsolation(t *testing.T) {
	store := newSchedStore()
	users := &usersMap{m: map[string]*models.User{
		"userA": pkgUser("userA", "pkg1"),
		"userB": pkgUser("userB", "pkg1"),
	}}
	pkg := schedTestPkg(3)
	// userA creates a schedule.
	ha := newSchedHandler(store, users, pkg, nil)
	ra := newSchedRouter(ha, "userA")
	if rec := doReq(t, ra, http.MethodPost, "/me/backup-schedules", `{"enabled":false,"content":"full","cadence":"daily"}`); rec.Code != http.StatusCreated {
		t.Fatalf("userA create = %d", rec.Code)
	}
	var aID string
	for id := range store.rows {
		aID = id
	}

	// userB — same store — must NOT see, edit, or delete userA's schedule.
	hb := newSchedHandler(store, users, pkg, nil)
	rb := newSchedRouter(hb, "userB")

	rec := doReq(t, rb, http.MethodGet, "/me/backup-schedules", "")
	var listResp struct {
		Data meSchedulesView `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Data.Schedules) != 0 {
		t.Errorf("userB must not see userA's schedule, saw %d", len(listResp.Data.Schedules))
	}
	if rec := doReq(t, rb, http.MethodPut, "/me/backup-schedules/"+aID, `{"enabled":true,"content":"files","cadence":"hourly"}`); rec.Code != http.StatusNotFound {
		t.Errorf("userB PUT on userA's schedule must 404, got %d", rec.Code)
	}
	if rec := doReq(t, rb, http.MethodDelete, "/me/backup-schedules/"+aID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("userB DELETE on userA's schedule must 404, got %d", rec.Code)
	}
	// The row must survive both cross-tenant attempts.
	if _, ok := store.rows[aID]; !ok {
		t.Error("userA's schedule was destroyed by a cross-tenant request")
	}
}
