package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/apps"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// applicationsRouter wires the M19 generic /applications surface for
// tests. Mirrors wordPressRouter but mounts RegisterApplicationRoutes
// + a populated registry. Returns the agent mock so individual tests
// can assert on the dispatched commands.
func applicationsRouter(t *testing.T, userID string, isAdmin bool, wpRepo *mockWordPressInstallRepo, domainRepo *mockDomainRepo, userRepo *mockUserRepo, registerExtras func(*apps.Registry)) (*gin.Engine, *mockAgent, *mockDatabaseRepo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	if userID != "" {
		v1.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: isAdmin})
			c.Next()
		})
	}

	registry := apps.New()
	if err := apps.RegisterDefaults(registry); err != nil {
		t.Fatalf("RegisterDefaults: %v", err)
	}
	if registerExtras != nil {
		registerExtras(registry)
	}

	dbRepo := &mockDatabaseRepo{}
	ag := &mockAgent{}

	cfg := ApplicationHandlerConfig{
		ApplicationInstalls: wpRepo,
		Databases:           dbRepo,
		DatabaseUsers:       &mockDatabaseUserRepo{},
		DatabaseGrants:      &mockDatabaseGrantRepo{},
		Domains:             domainRepo,
		Users:               userRepo,
		Packages:            &mockPackageRepo{},
		Agent:               ag,
		Apps:                registry,
	}
	RegisterApplicationRoutes(v1, cfg)
	return r, ag, dbRepo
}

func wpUserAndDomain() (*mockWordPressInstallRepo, *mockDomainRepo, *mockUserRepo) {
	return &mockWordPressInstallRepo{},
		&mockDomainRepo{
			domains: map[string]*models.Domain{
				"domain1": {ID: "domain1", UserID: "user1", Name: "example.com", DocRoot: "/home/alice/domains/example.com/public_html"},
			},
		},
		&mockUserRepo{
			users: map[string]*models.User{
				"user1": {ID: "user1", Username: strPtr("alice")},
			},
		}
}

func TestApplications_GetRegistry_IncludesWordPress(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	req := httptest.NewRequest("GET", "/api/v1/applications/registry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var resp struct {
		Data []registryEntry `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range resp.Data {
		if e.Name == "wordpress" {
			found = true
			if !e.RequiresDB {
				t.Error("wordpress entry should advertise requires_db=true")
			}
			if e.DisplayName == "" {
				t.Error("wordpress entry should have a display name")
			}
			break
		}
	}
	if !found {
		t.Errorf("registry missing wordpress entry: %+v", resp.Data)
	}
}

func TestApplications_CreateWordPress_HappyPath(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, ag, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":     "wordpress",
		"domain_id":    "domain1",
		"subdirectory": "blog",
		"params": map[string]any{
			"admin_email":    "admin@example.com",
			"admin_password": "s3cret-pw",
			"site_title":     "Hello",
			"locale":         "en_US",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var resp createApplicationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AppType != "wordpress" {
		t.Errorf("AppType = %q", resp.AppType)
	}
	if resp.Status != "pending" {
		t.Errorf("Status = %q", resp.Status)
	}
	if resp.DBID == "" {
		t.Error("WordPress install should provision a DB and return its id")
	}
	if resp.AdminPassword != "s3cret-pw" {
		t.Errorf("AdminPassword should echo the supplied value, got %q", resp.AdminPassword)
	}
	if resp.AdminUsername == "" {
		t.Error("AdminUsername should be auto-generated server-side, got empty")
	}
	// We can't read ag.callCount here without racing the install kicker
	// goroutine — DBID being non-empty already proves the synchronous
	// db.create / db_user.create / db_user.grant chain ran. The kicker
	// itself is exercised by wordpress_test.go in the legacy WP suite.
	_ = ag
}

func TestApplications_CreateITFlow_SurfacesEmailAsLogin(t *testing.T) {
	// GH #226: ITFlow logs in by EMAIL, not a username. The framework must
	// surface the admin email as the login credential instead of the random
	// generated username (which left users unable to sign in).
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, ag, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":  "itflow",
		"domain_id": "domain1",
		"params": map[string]any{
			"company_name": "Acme MSP",
			"admin_name":   "Administrator",
			"admin_email":  "boss@example.com",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var resp createApplicationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AdminUsername != "boss@example.com" {
		t.Errorf("EmailLogin app should surface the admin email as the login, got AdminUsername=%q", resp.AdminUsername)
	}
	_ = ag
}

func TestApplications_CreateInvoiceShelf_SurfacesEmailAsLogin(t *testing.T) {
	// GH #1042 follow-up: InvoiceShelf logs in by EMAIL (the seeded super
	// admin has no username), so the descriptor is EmailLogin — the random
	// generated username must never be what the operator is shown ("the
	// random admin user is still be displayed").
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, ag, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":  "invoiceshelf",
		"domain_id": "domain1",
		"params": map[string]any{
			"site_title":  "Acme Ltd",
			"admin_email": "boss@example.com",
			"country":     "IL",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var resp createApplicationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.AdminUsername != "boss@example.com" {
		t.Errorf("EmailLogin app should surface the admin email as the login, got AdminUsername=%q", resp.AdminUsername)
	}
	_ = ag
}

func TestApplications_CreateInvoiceShelf_RejectsUnknownCountry(t *testing.T) {
	// The country enum mirrors the app's own countries table; an unknown
	// code must fail validation at the API, not silently no-op in tinker.
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":  "invoiceshelf",
		"domain_id": "domain1",
		"params": map[string]any{
			"admin_email": "boss@example.com",
			"country":     "XX",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
}

func TestApplications_CreateRequiresDBFalse_SkipsDBChain(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, ag, dbRepo := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, func(reg *apps.Registry) {
		// Register a flat-file app so the RequiresDB=false branch runs.
		// Step 6 will replace this with the real DokuWiki descriptor;
		// here we only need to prove the framework skips DB provisioning.
		_ = reg.Register(apps.App{
			Name:        "flatfile",
			DisplayName: "Flatfile",
			RequiresDB:  false,
			InstallParamSchema: map[string]apps.ParamSpec{
				"admin_email":    {Type: "email", Required: true},
				"admin_password": {Type: "password", Required: true},
			},
		})
	})

	body := map[string]any{
		"app_type":  "flatfile",
		"domain_id": "domain1",
		"params": map[string]any{
			"admin_email":    "admin@example.com",
			"admin_password": "s3cret-pw",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var resp createApplicationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.DBID != "" {
		t.Errorf("RequiresDB=false: expected empty DBID, got %q", resp.DBID)
	}
	if ag.callCount != 0 {
		t.Errorf("RequiresDB=false: expected 0 agent calls (no DB chain, no install kicker for non-WP), got %d", ag.callCount)
	}
	if len(dbRepo.databases) != 0 {
		t.Errorf("RequiresDB=false: expected 0 db rows created, got %d", len(dbRepo.databases))
	}
}

func TestApplications_Create_InvalidAppType_400(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":  "magento",
		"domain_id": "domain1",
		"params":    map[string]any{},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_app_type") {
		t.Errorf("body missing invalid_app_type: %s", w.Body.String())
	}
}

func TestApplications_Create_MissingRequiredParam_400(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":  "wordpress",
		"domain_id": "domain1",
		"params": map[string]any{
			// site_title missing — required by descriptor.
			// admin_username is no longer in the schema — it's auto-
			// generated server-side, so we exercise a different
			// required field here.
			"admin_email":    "admin@example.com",
			"admin_password": "p",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_params") {
		t.Errorf("body missing invalid_params: %s", w.Body.String())
	}
}

func TestApplications_Create_BadEmailParam_400(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":  "wordpress",
		"domain_id": "domain1",
		"params": map[string]any{
			"admin_email":    "not-an-email",
			"admin_password": "p",
			"site_title":     "t",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_params") {
		t.Errorf("body missing invalid_params: %s", w.Body.String())
	}
}

func TestApplications_Create_UnknownParam_400(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":  "wordpress",
		"domain_id": "domain1",
		"params": map[string]any{
			"admin_email":    "admin@example.com",
			"admin_password": "p",
			"site_title":     "t",
			"surprise":       "extra",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
}

func TestApplications_Create_DuplicateAppTypeAtSameSlot_409(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	wpRepo.installs = map[string]*models.WordPressInstall{
		"inst1": {ID: "inst1", UserID: "user1", DomainID: "domain1", Subdirectory: "blog", AppType: "wordpress"},
	}
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":     "wordpress",
		"domain_id":    "domain1",
		"subdirectory": "blog",
		"params": map[string]any{
			"admin_email":    "admin@example.com",
			"admin_password": "p",
			"site_title":     "t",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "install_exists") {
		t.Errorf("body missing install_exists: %s", w.Body.String())
	}
}

func TestApplications_Create_DifferentAppTypeSameSlot_Conflict(t *testing.T) {
	// Per the operator's directive (2026-04-19): a (domain, subdir) slot
	// hosts AT MOST ONE application — even if the second install is a
	// different app_type. This is stricter than the DB-level UNIQUE in
	// migration 000046 (which still allows distinct app_types in the
	// same slot for forward compat); the API check enforces the
	// product rule. ADR-0033 was updated to reflect this.
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	wpRepo.installs = map[string]*models.WordPressInstall{
		"inst1": {ID: "inst1", UserID: "user1", DomainID: "domain1", Subdirectory: "wiki", AppType: "wordpress"},
	}
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, func(reg *apps.Registry) {
		_ = reg.Register(apps.App{
			Name:        "flatfile",
			DisplayName: "Flatfile",
			RequiresDB:  false,
			InstallParamSchema: map[string]apps.ParamSpec{
				"admin_email":    {Type: "email", Required: true},
				"admin_password": {Type: "password", Required: true},
			},
		})
	})

	body := map[string]any{
		"app_type":     "flatfile",
		"domain_id":    "domain1",
		"subdirectory": "wiki",
		"params": map[string]any{
			"admin_email":    "admin@example.com",
			"admin_password": "p",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 (slot already taken regardless of app_type): %d body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "install_exists") {
		t.Errorf("body missing install_exists: %s", w.Body.String())
	}
}

func TestApplications_Create_DomainNotOwned_404(t *testing.T) {
	wpRepo, _, userRepo := wpUserAndDomain()
	domainRepo := &mockDomainRepo{
		domains: map[string]*models.Domain{
			"domainX": {ID: "domainX", UserID: "userOTHER", Name: "other.com"},
		},
	}
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":  "wordpress",
		"domain_id": "domainX",
		"params": map[string]any{
			"admin_email":    "admin@example.com",
			"admin_password": "p",
			"site_title":     "t",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
}

func TestApplications_Create_BadSubdirectory_400(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	body := map[string]any{
		"app_type":     "wordpress",
		"domain_id":    "domain1",
		"subdirectory": "../escape",
		"params": map[string]any{
			"admin_email":    "admin@example.com",
			"admin_password": "p",
			"site_title":     "t",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid_subdirectory") {
		t.Errorf("body missing invalid_subdirectory: %s", w.Body.String())
	}
}

func TestApplications_List_DelegatesToWPHandler(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	wpRepo.installs = map[string]*models.WordPressInstall{
		"inst1": {ID: "inst1", UserID: "user1", DomainID: "domain1", AppType: "wordpress", Status: "ready"},
	}
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	req := httptest.NewRequest("GET", "/api/v1/applications", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["total"] != float64(1) {
		t.Fatalf("total = %v", resp["total"])
	}
}

func TestValidateInstallParams_TableDriven(t *testing.T) {
	pat := "^[a-z]+$"
	schema := map[string]apps.ParamSpec{
		"name":   {Type: "string", Required: true, Pattern: &pat},
		"email":  {Type: "email", Required: false},
		"flag":   {Type: "bool", Required: false},
		"choice": {Type: "enum", Values: []string{"a", "b"}, Required: true},
	}
	cases := []struct {
		name    string
		params  map[string]any
		wantErr string
	}{
		{"happy", map[string]any{"name": "abc", "choice": "a"}, ""},
		{"missing required", map[string]any{"choice": "a"}, "required"},
		{"pattern fail", map[string]any{"name": "ABC", "choice": "a"}, "pattern"},
		{"enum fail", map[string]any{"name": "abc", "choice": "z"}, "must be one of"},
		{"wrong type bool", map[string]any{"name": "abc", "choice": "a", "flag": "yes"}, "expected bool"},
		{"unknown field", map[string]any{"name": "abc", "choice": "a", "extra": 1}, "unknown param"},
		{"bad email", map[string]any{"name": "abc", "choice": "a", "email": "no-at-sign"}, "invalid email"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateInstallParams(c.params, schema)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want error containing %q, got %q", c.wantErr, err.Error())
			}
		})
	}
}

// Compile-time guard: the legacy alias keeps existing wiring valid.
var _ ApplicationHandlerConfig = ApplicationHandlerConfig{}

// Use context to keep imports tight in case future tests need it.
var _ = context.Background

// TestApplications_Create_RootOnly_Rejects covers GH #226: ITFlow only works at
// the domain root, so a subdirectory or www prefix must be rejected at the API.
func TestApplications_Create_RootOnly_Rejects(t *testing.T) {
	mkReq := func(extra map[string]any) map[string]any {
		body := map[string]any{
			"app_type":  "itflow",
			"domain_id": "domain1",
			"params": map[string]any{
				"company_name":   "Acme",
				"admin_name":     "Admin",
				"admin_email":    "admin@example.com",
				"admin_password": "password1",
			},
		}
		for k, v := range extra {
			body[k] = v
		}
		return body
	}
	cases := []struct {
		name    string
		extra   map[string]any
		wantErr string
	}{
		{"subdirectory", map[string]any{"subdirectory": "itflow"}, "subdirectory_not_allowed"},
		{"www", map[string]any{"use_www": true}, "www_not_allowed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wpRepo, domainRepo, userRepo := wpUserAndDomain()
			r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, func(reg *apps.Registry) {
				_ = reg.Register(apps.ITFlow)
			})
			bodyBytes, _ := json.Marshal(mkReq(tc.extra))
			req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantErr) {
				t.Errorf("body missing %s: %s", tc.wantErr, w.Body.String())
			}
		})
	}
}

// TestApplications_DBPasswordSeparateFromAdmin covers GH #226 item 3: the
// MariaDB password must be its own generated secret, never the admin password.
func TestApplications_DBPasswordSeparateFromAdmin(t *testing.T) {
	wpRepo, domainRepo, userRepo := wpUserAndDomain()
	r, ag, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)

	var dbPass string
	ag.callFn = func(_ context.Context, command string, params any) (json.RawMessage, error) {
		if command == "db_user.create" {
			if m, ok := params.(map[string]any); ok {
				if pw, ok := m["password"].(string); ok {
					dbPass = pw
				}
			}
		}
		return json.RawMessage(`{}`), nil
	}

	body := map[string]any{
		"app_type":     "wordpress",
		"domain_id":    "domain1",
		"subdirectory": "blog",
		"params": map[string]any{
			"admin_email":    "admin@example.com",
			"admin_password": "s3cret-pw",
			"site_title":     "X",
		},
	}
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
	}
	var resp createApplicationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// db_user.create runs synchronously in provisionDBChain, before the
	// async kicker — safe to read here.
	if dbPass == "" {
		t.Fatal("db_user.create password was not captured")
	}
	if dbPass == "s3cret-pw" {
		t.Fatal("DB password must NOT equal the supplied admin password (GH #226)")
	}
	if dbPass == resp.AdminPassword {
		t.Fatalf("DB password must differ from the surfaced admin password (both %q)", dbPass)
	}
}

// TestApplications_AdminUsername covers GH #228: an optional admin_username
// field — used when supplied, randomly generated when blank.
func TestApplications_AdminUsername(t *testing.T) {
	mk := func(params map[string]any) string {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		body := map[string]any{"app_type": "wordpress", "domain_id": "domain1", "subdirectory": "blog", "params": params}
		bodyBytes, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/v1/applications", bytes.NewReader(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("status: %d body: %s", w.Code, w.Body.String())
		}
		var resp createApplicationResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.AdminUsername
	}

	base := map[string]any{"admin_email": "a@example.com", "admin_password": "pw", "site_title": "X"}

	// Supplied username is used verbatim.
	withName := map[string]any{}
	for k, v := range base {
		withName[k] = v
	}
	withName["admin_username"] = "custombob"
	if got := mk(withName); got != "custombob" {
		t.Errorf("custom admin_username not used: got %q", got)
	}

	// Blank → a random username is generated (non-empty, not the literal).
	blank := map[string]any{}
	for k, v := range base {
		blank[k] = v
	}
	blank["admin_username"] = ""
	g := mk(blank)
	if g == "" || g == "custombob" {
		t.Errorf("blank admin_username should generate a random one, got %q", g)
	}

	// Absent → also generated.
	if g2 := mk(base); g2 == "" {
		t.Error("absent admin_username should generate a random one")
	}
}

// TestAppCronSpecs verifies the auto-created cron commands for WordPress,
// Joomla and MediaWiki are well-formed AND pass the wp/php cron allowlist
// (internal/cronvalidate) — the constraint that makes them installable.
func TestAppCronSpecs(t *testing.T) {
	const installPath = "/home/u1/domains/site.com/public_html"
	owned := []string{installPath}

	for _, app := range []string{"wordpress", "joomla", "mediawiki"} {
		specs := appCronSpecs(app, installPath)
		if len(specs) == 0 {
			t.Fatalf("%s: expected at least one cron spec", app)
		}
		for _, c := range specs {
			if c.name == "" || c.command == "" {
				t.Errorf("%s: empty name/command", app)
			}
			if err := cronvalidate.ValidateSchedule(c.schedule); err != nil {
				t.Errorf("%s: bad schedule %q: %v", app, c.schedule, err)
			}
			if err := cronvalidate.ValidateCronName(c.name); err != nil {
				t.Errorf("%s: bad cron name %q: %v", app, c.name, err)
			}
			if _, err := cronvalidate.ValidateCommand(c.command, owned); err != nil {
				t.Errorf("%s: command rejected by allowlist %q: %v", app, c.command, err)
			}
		}
	}

	// Email-login / unsupported apps get no auto-cron.
	for _, app := range []string{"itflow", "dokuwiki", "flarum", "unknown"} {
		if specs := appCronSpecs(app, installPath); len(specs) != 0 {
			t.Errorf("%s: expected no auto-cron, got %d", app, len(specs))
		}
	}
}

// TestApplications_Registry_ExposesRootOnly is the regression guard for the
// GH #226 follow-up: the install form hides Use-www-prefix + Directory based on
// the descriptor's root_only flag, so GET /applications/registry MUST surface
// it. It was dropped from the response DTO, so the UI never hid the controls
// for ITFlow even though RootOnly was set + the API rejected the values.
func TestApplications_Registry_ExposesRootOnly(t *testing.T) {
	wpRepo := &mockWordPressInstallRepo{}
	domainRepo := &mockDomainRepo{}
	userRepo := &mockUserRepo{}
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, func(reg *apps.Registry) {
		_ = reg.Register(apps.ITFlow)
	})

	req := httptest.NewRequest("GET", "/api/v1/applications/registry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var resp struct {
		Data []registryEntry `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var itflow *registryEntry
	for i := range resp.Data {
		if resp.Data[i].Name == "itflow" {
			itflow = &resp.Data[i]
			break
		}
	}
	if itflow == nil {
		t.Fatalf("registry missing itflow entry: %+v", resp.Data)
	}
	if !itflow.RootOnly {
		t.Error("itflow registry entry must advertise root_only=true so the UI hides www/Directory")
	}
}

// GH #341: ITFlow needs PHP exec functions (git-based updater + expiry
// lookups). The descriptor carries an InstallNotice warning the SPA renders in
// the install modal, but the response DTO dropped it — so the operator never
// saw the notice. GET /applications/registry MUST surface install_notice.
func TestApplications_Registry_ExposesInstallNotice(t *testing.T) {
	wpRepo := &mockWordPressInstallRepo{}
	domainRepo := &mockDomainRepo{}
	userRepo := &mockUserRepo{}
	r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, func(reg *apps.Registry) {
		_ = reg.Register(apps.ITFlow)
	})

	req := httptest.NewRequest("GET", "/api/v1/applications/registry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var resp struct {
		Data []registryEntry `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var itflow *registryEntry
	for i := range resp.Data {
		if resp.Data[i].Name == "itflow" {
			itflow = &resp.Data[i]
			break
		}
	}
	if itflow == nil {
		t.Fatalf("registry missing itflow entry: %+v", resp.Data)
	}
	if itflow.InstallNotice == "" {
		t.Error("itflow registry entry must surface install_notice so the UI shows the exec-functions warning")
	}
}

// --- GH #556: characterization test locking the app cache-toggle behavior
// before it is extracted into a shared core for CLI parity. Asserts the
// distinct status codes the UI depends on. ---

func putCache(r *gin.Engine, id string, enabled bool) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]bool{"enabled": enabled})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/applications/"+id+"/cache", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestApplicationsCache_Characterization(t *testing.T) {
	ctx := context.Background()

	t.Run("install not found -> 404", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		if w := putCache(r, "missing", true); w.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("non-wordpress -> 400", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		wpRepo.Create(ctx, &models.WordPressInstall{ID: "i1", UserID: "user1", DomainID: "domain1", AppType: "dokuwiki"})
		r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		if w := putCache(r, "i1", true); w.Code != http.StatusBadRequest {
			t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("enable without redis -> 503", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		wpRepo.Create(ctx, &models.WordPressInstall{ID: "i2", UserID: "user1", DomainID: "domain1", AppType: "wordpress"})
		r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		// cfg has no Redis/secret -> enable must 503, not 500.
		if w := putCache(r, "i2", true); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("want 503, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("disable -> 200 + agent cache_set + install flag off", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		wpRepo.Create(ctx, &models.WordPressInstall{ID: "i3", UserID: "user1", DomainID: "domain1", AppType: "wordpress", CacheEnabled: true})
		r, ag, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		w := putCache(r, "i3", false)
		if w.Code != http.StatusOK {
			t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
		}
		if ag.lastCommand != "wordpress.cache_set" {
			t.Fatalf("expected wordpress.cache_set agent call, got %q", ag.lastCommand)
		}
		inst, _ := wpRepo.FindByID(ctx, "i3")
		if inst.CacheEnabled {
			t.Fatalf("install cache flag should be off after disable")
		}
	})
}

// postClone drives POST /applications/:id/clone with the given dest domain.
func postClone(r *gin.Engine, id, destDomainID string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"dest_domain_id": destDomainID})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/applications/"+id+"/clone", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestApplicationsClone_Characterization locks the web POST
// /applications/:id/clone behavior BEFORE the cloneCore extraction (GH #556
// part 3). It asserts only the synchronous provisioning path — never the async
// createCloneAndKickAgent goroutine's terminal status (racy).
func TestApplicationsClone_Characterization(t *testing.T) {
	ctx := context.Background()

	t.Run("source not found -> 404", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		if w := postClone(r, "missing", "domain1"); w.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("dest domain not found -> 404", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		wpRepo.Create(ctx, &models.WordPressInstall{ID: "src", UserID: "user1", DomainID: "domain1", AppType: "wordpress", Subdirectory: "blog"})
		r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		if w := postClone(r, "src", "missing"); w.Code != http.StatusNotFound {
			t.Fatalf("want 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("dest domain cross-user -> 403", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		wpRepo.Create(ctx, &models.WordPressInstall{ID: "src", UserID: "user1", DomainID: "domain1", AppType: "wordpress", Subdirectory: "blog"})
		// domain2 is owned by a different user -> ownership check must 403.
		domainRepo.domains["domain2"] = &models.Domain{ID: "domain2", UserID: "user2", Name: "other.com", DocRoot: "/home/bob/domains/other.com/public_html"}
		r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		if w := postClone(r, "src", "domain2"); w.Code != http.StatusForbidden {
			t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("collision at dest subdir -> 409", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		// dest domain owned by user1, already hosting an install at subdir "blog".
		domainRepo.domains["domain2"] = &models.Domain{ID: "domain2", UserID: "user1", Name: "dest.com", DocRoot: "/home/alice/domains/dest.com/public_html"}
		wpRepo.Create(ctx, &models.WordPressInstall{ID: "src", UserID: "user1", DomainID: "domain1", AppType: "wordpress", Subdirectory: "blog"})
		wpRepo.Create(ctx, &models.WordPressInstall{ID: "occupant", UserID: "user1", DomainID: "domain2", AppType: "wordpress", Subdirectory: "blog"})
		r, _, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		if w := postClone(r, "src", "domain2"); w.Code != http.StatusConflict {
			t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("agent failure -> 502, no leaked detail", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		domainRepo.domains["domain2"] = &models.Domain{ID: "domain2", UserID: "user1", Name: "dest.com", DocRoot: "/home/alice/domains/dest.com/public_html"}
		wpRepo.Create(ctx, &models.WordPressInstall{ID: "src", UserID: "user1", DomainID: "domain1", AppType: "wordpress", Subdirectory: "blog", DBID: models.DBIDPtr("srcdb")})
		r, ag, _ := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		ag.callErr = errors.New("boom") // agent db.create fails -> 502
		w := postClone(r, "src", "domain2")
		if w.Code != http.StatusBadGateway {
			t.Fatalf("want 502, got %d: %s", w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["error"] != "agent_failed" {
			t.Fatalf("want error=agent_failed, got %v", body["error"])
		}
		// JAB-114: the raw agent error ("boom") must NOT be echoed to the
		// client — it's logged server-side instead.
		if _, ok := body["detail"]; ok {
			t.Fatalf("agent error detail leaked to client: %v", body["detail"])
		}
		if strings.Contains(w.Body.String(), "boom") {
			t.Fatalf("raw agent error leaked in body: %s", w.Body.String())
		}
	})

	t.Run("success -> 202 + cloning install + dest DB row", func(t *testing.T) {
		wpRepo, domainRepo, userRepo := wpUserAndDomain()
		domainRepo.domains["domain2"] = &models.Domain{ID: "domain2", UserID: "user1", Name: "dest.com", DocRoot: "/home/alice/domains/dest.com/public_html"}
		src := &models.WordPressInstall{ID: "src", UserID: "user1", DomainID: "domain1", AppType: "wordpress", Subdirectory: "blog", DBID: models.DBIDPtr("srcdb")}
		wpRepo.Create(ctx, src)
		r, _, dbRepo := applicationsRouter(t, "user1", false, wpRepo, domainRepo, userRepo, nil)
		w := postClone(r, "src", "domain2")
		if w.Code != http.StatusAccepted {
			t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
		}
		var resp createWordPressResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Status != "cloning" {
			t.Fatalf("want status cloning, got %q", resp.Status)
		}
		if resp.ID == "" || resp.ID == "src" {
			t.Fatalf("clone should mint a fresh install ID, got %q", resp.ID)
		}
		if resp.DomainID != "domain2" {
			t.Fatalf("clone should target domain2, got %q", resp.DomainID)
		}
		// Synchronous path provisioned exactly one dest database row.
		if len(dbRepo.databases) != 1 {
			t.Fatalf("want 1 dest database provisioned, got %d", len(dbRepo.databases))
		}
	})
}
