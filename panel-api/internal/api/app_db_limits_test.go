package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/apps"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// capDBUsers stores database user rows and holds CreateWithinCap to its max,
// like the real repository.
type capDBUsers struct {
	mockDatabaseUserRepo
	mu   sync.Mutex
	rows []models.DatabaseUser
}

func (r *capDBUsers) Create(_ context.Context, du *models.DatabaseUser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, *du)
	return nil
}

func (r *capDBUsers) CreateWithinCap(ctx context.Context, du *models.DatabaseUser, max uint32) error {
	r.mu.Lock()
	n := 0
	for _, row := range r.rows {
		if row.UserID == du.UserID {
			n++
		}
	}
	r.mu.Unlock()
	if n >= int(max) {
		return repository.ErrDatabaseUserCapExceeded
	}
	return r.Create(ctx, du)
}

func (r *capDBUsers) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

// failingPackages cannot read any package.
type failingPackages struct{ mockPackageRepo }

func (*failingPackages) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return nil, errors.New("connection refused")
}

// commandLog records the agent commands a request sends.
type commandLog struct {
	mu   sync.Mutex
	cmds []string
}

func (l *commandLog) agent() *mockAgent {
	return &mockAgent{callFn: func(_ context.Context, command string, _ any) (json.RawMessage, error) {
		l.mu.Lock()
		l.cmds = append(l.cmds, command)
		l.mu.Unlock()
		return json.RawMessage(`{}`), nil
	}}
}

func (l *commandLog) sent() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.cmds...)
}

type appLimitsFixture struct {
	r       *gin.Engine
	dbs     *mockDatabaseRepo
	dbUsers *capDBUsers
	wp      *mockWordPressInstallRepo
	agent   *commandLog
}

// newAppLimitsFixture: user1 (alice) on package pkg1 with the given limits,
// already holding one database and one database user.
func newAppLimitsFixture(t *testing.T, maxDatabases, maxDatabaseUsers uint32, pkgs repository.PackageRepository) *appLimitsFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if pkgs == nil {
		pkgs = &mockPackageRepo{packages: map[string]*models.HostingPackage{
			"pkg1": {ID: "pkg1", MaxDatabases: maxDatabases, MaxDatabaseUsers: maxDatabaseUsers},
		}}
	}
	pkgID := "pkg1"
	f := &appLimitsFixture{
		dbs:     &mockDatabaseRepo{databases: []models.Database{{ID: "db0", UserID: "user1", Name: "alice_db0"}}},
		dbUsers: &capDBUsers{rows: []models.DatabaseUser{{ID: "du0", UserID: "user1", Username: "alice_u0"}}},
		wp:      &mockWordPressInstallRepo{},
		agent:   &commandLog{},
	}
	domains := &mockDomainRepo{domains: map[string]*models.Domain{
		"domain1": {ID: "domain1", UserID: "user1", Name: "example.com", DocRoot: "/home/alice/domains/example.com/public_html"},
		"domain2": {ID: "domain2", UserID: "user1", Name: "dest.com", DocRoot: "/home/alice/domains/dest.com/public_html"},
	}}
	users := &mockUserRepo{users: map[string]*models.User{
		"user1": {ID: "user1", Username: strPtr("alice"), PackageID: &pkgID},
	}}
	registry := apps.New()
	if err := apps.RegisterDefaults(registry); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	f.r = gin.New()
	v1 := f.r.Group("/api/v1", func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "user1"})
		c.Next()
	})
	RegisterApplicationRoutes(v1, ApplicationHandlerConfig{
		ApplicationInstalls: f.wp,
		Databases:           f.dbs,
		DatabaseUsers:       f.dbUsers,
		DatabaseGrants:      &mockDatabaseGrantRepo{},
		Domains:             domains,
		Users:               users,
		Packages:            pkgs,
		Agent:               f.agent.agent(),
		Apps:                registry,
	})
	return f
}

func (f *appLimitsFixture) install(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"app_type":     "wordpress",
		"domain_id":    "domain1",
		"subdirectory": "blog",
		"params": map[string]any{
			"admin_email": "admin@example.com",
			"site_title":  "Hello",
			"locale":      "en_US",
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/applications", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.r.ServeHTTP(w, req)
	return w
}

func requireQuotaExceeded(t *testing.T, w *httptest.ResponseRecorder, resource string, limit int64) {
	t.Helper()
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "quota_exceeded" {
		t.Fatalf("want quota_exceeded, got %s", w.Body.String())
	}
	if resource != "" && body["resource"] != resource {
		t.Fatalf("want resource %q, got %s", resource, w.Body.String())
	}
	if limit != 0 && body["limit"] != float64(limit) {
		t.Fatalf("want limit %d, got %s", limit, w.Body.String())
	}
}

func TestAppInstall_OverDatabaseLimitIsRefusedBeforeTheAgent(t *testing.T) {
	f := newAppLimitsFixture(t, 1, 5, nil)
	requireQuotaExceeded(t, f.install(t), "", 0)
	if len(f.dbs.databases) != 1 || f.dbUsers.count() != 1 {
		t.Fatalf("rows changed: %d databases, %d database users", len(f.dbs.databases), f.dbUsers.count())
	}
	if cmds := f.agent.sent(); len(cmds) != 0 {
		t.Fatalf("the agent was called: %v", cmds)
	}
}

func TestAppInstall_OverDatabaseUserLimitIsRefusedBeforeTheAgent(t *testing.T) {
	f := newAppLimitsFixture(t, 5, 1, nil)
	requireQuotaExceeded(t, f.install(t), "", 0)
	if f.dbUsers.count() != 1 {
		t.Fatalf("a database user row was added: %d", f.dbUsers.count())
	}
	if cmds := f.agent.sent(); len(cmds) != 0 {
		t.Fatalf("the agent was called: %v", cmds)
	}
}

func TestAppInstall_UnderTheLimitsInstalls(t *testing.T) {
	f := newAppLimitsFixture(t, 2, 2, nil)
	if w := f.install(t); w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}
	if f.dbUsers.count() != 2 {
		t.Fatalf("want the install's database user row, have %d rows", f.dbUsers.count())
	}
}

func TestAppInstall_UnreadablePackageIs503(t *testing.T) {
	f := newAppLimitsFixture(t, 0, 0, &failingPackages{})
	w := f.install(t)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", w.Code, w.Body.String())
	}
	if cmds := f.agent.sent(); len(cmds) != 0 {
		t.Fatalf("the agent was called: %v", cmds)
	}
}

func TestAppClone_OverDatabaseLimitIsRefusedBeforeTheAgent(t *testing.T) {
	f := newAppLimitsFixture(t, 1, 5, nil)
	_ = f.wp.Create(context.Background(), &models.WordPressInstall{ID: "src", UserID: "user1", DomainID: "domain1", AppType: "wordpress", Subdirectory: "blog", DBID: models.DBIDPtr("db0")})
	requireQuotaExceeded(t, postClone(f.r, "src", "domain2"), "databases", 1)
	if len(f.dbs.databases) != 1 {
		t.Fatalf("a database row was added: %d", len(f.dbs.databases))
	}
	if cmds := f.agent.sent(); len(cmds) != 0 {
		t.Fatalf("the agent was called: %v", cmds)
	}
}

func TestAppClone_OverDatabaseUserLimitIsRefusedBeforeTheAgent(t *testing.T) {
	f := newAppLimitsFixture(t, 5, 1, nil)
	_ = f.wp.Create(context.Background(), &models.WordPressInstall{ID: "src", UserID: "user1", DomainID: "domain1", AppType: "wordpress", Subdirectory: "blog", DBID: models.DBIDPtr("db0")})
	requireQuotaExceeded(t, postClone(f.r, "src", "domain2"), "database_users", 1)
	if f.dbUsers.count() != 1 {
		t.Fatalf("a database user row was added: %d", f.dbUsers.count())
	}
	if cmds := f.agent.sent(); len(cmds) != 0 {
		t.Fatalf("the agent was called: %v", cmds)
	}
}
