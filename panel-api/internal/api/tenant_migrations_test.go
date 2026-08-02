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

type fakeTMJobs struct {
	repository.MigrationJobRepository
	jobs    map[string]*models.MigrationJob
	created *models.MigrationJob
}

func (f *fakeTMJobs) FindByID(_ context.Context, id string) (*models.MigrationJob, error) {
	if j, ok := f.jobs[id]; ok {
		return j, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeTMJobs) Create(_ context.Context, j *models.MigrationJob) error {
	f.created = j
	return nil
}
func (f *fakeTMJobs) UpdateSourcePath(_ context.Context, _, _ string) error { return nil }

type fakeTMDomains struct {
	repository.DomainRepository
	byName map[string]*models.Domain
}

func (f *fakeTMDomains) FindByName(_ context.Context, n string) (*models.Domain, error) {
	if d, ok := f.byName[n]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

type fakeTMUsers struct {
	repository.UserRepository
	byID map[string]*models.User
}

func (f *fakeTMUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

type fakeTMAgent struct{ last map[string]any }

func (f *fakeTMAgent) Call(_ context.Context, _ string, p any) (json.RawMessage, error) {
	f.last, _ = p.(map[string]any)
	return json.RawMessage("{}"), nil
}

func newTMHandler() (*tenantMigrationsHandler, *fakeTMJobs, *fakeTMAgent) {
	jobs := &fakeTMJobs{jobs: map[string]*models.MigrationJob{}}
	ag := &fakeTMAgent{}
	uidB := "USERB"
	h := &tenantMigrationsHandler{cfg: TenantMigrationsConfig{
		Jobs: jobs,
		Domains: &fakeTMDomains{byName: map[string]*models.Domain{
			"mine.com":   {ID: "D1", UserID: "USERA", Name: "mine.com"},
			"theirs.com": {ID: "D2", UserID: uidB, Name: "theirs.com"},
		}},
		Users: &fakeTMUsers{byID: map[string]*models.User{
			"USERA": {ID: "USERA", Username: strptr("usera")},
		}},
		Agent: ag,
	}}
	// A job owned by USERB — USERA must never reach it.
	otherUID := uidB
	jobs.jobs["JOBB"] = &models.MigrationJob{ID: "JOBB", SourceKind: models.MigrationSourceWordPressSSH, TargetUserID: &otherUID, State: "validating"}
	return h, jobs, ag
}

func ctxAs(uid, body, param string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if uid != "" {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: uid})
	}
	if param != "" {
		c.Params = gin.Params{{Key: "id", Value: param}}
	}
	return c, w
}

// SECURITY: a tenant must never reach another tenant's job (ownedJob → 404).
func TestTenantMigration_CrossTenantJob_404(t *testing.T) {
	h, _, _ := newTMHandler()
	c, w := ctxAs("USERA", `{}`, "JOBB") // USERA reaching USERB's job
	h.get(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant job = %d, want 404", w.Code)
	}
}

// SECURITY: a tenant cannot target a domain they don't own.
func TestTenantMigration_CreateForeignDomain_404(t *testing.T) {
	h, jobs, _ := newTMHandler()
	c, w := ctxAs("USERA", `{"source_kind":"wordpress_ssh","source_host":"old.host","dest_domain":"theirs.com"}`, "")
	h.create(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("create into foreign domain = %d, want 404", w.Code)
	}
	if jobs.created != nil {
		t.Fatal("job created despite foreign dest domain")
	}
}

// SECURITY: target_user_id is forced to the caller, never the request.
func TestTenantMigration_CreateForcesTarget(t *testing.T) {
	h, jobs, _ := newTMHandler()
	c, w := ctxAs("USERA", `{"source_kind":"wordpress_ssh","source_host":"old.host","dest_domain":"mine.com","target_user_id":"USERB"}`, "")
	h.create(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("create own domain = %d, want 201", w.Code)
	}
	if jobs.created == nil || jobs.created.TargetUserID == nil || *jobs.created.TargetUserID != "USERA" {
		t.Fatalf("target_user_id not forced to caller: %+v", jobs.created)
	}
}

// Panel-account kinds are refused for tenants.
func TestTenantMigration_RejectsPanelKind(t *testing.T) {
	h, _, _ := newTMHandler()
	c, w := ctxAs("USERA", `{"source_kind":"cpanel","source_host":"x","dest_domain":"mine.com"}`, "")
	h.create(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("cpanel kind = %d, want 400", w.Code)
	}
}

// Unauthenticated → 401.
func TestTenantMigration_Unauth_401(t *testing.T) {
	h, _, _ := newTMHandler()
	c, w := ctxAs("", `{}`, "")
	h.create(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth = %d, want 401", w.Code)
	}
}

// importWP forces dest_user to the caller's own OS username.
func TestTenantMigration_ImportForcesDestUser(t *testing.T) {
	h, jobs, ag := newTMHandler()
	myUID := "USERA"
	jobs.jobs["JOBA"] = &models.MigrationJob{ID: "JOBA", SourceKind: models.MigrationSourceWordPressSSH, TargetUserID: &myUID, State: "validating"}
	c, w := ctxAs("USERA", `{"dest_domain":"mine.com","dest_user":"root"}`, "JOBA")
	h.importWP(c)
	if w.Code != http.StatusOK {
		t.Fatalf("import = %d, want 200", w.Code)
	}
	if ag.last["dest_user"] != "usera" {
		t.Fatalf("dest_user not forced to caller OS user: %v", ag.last["dest_user"])
	}
}
