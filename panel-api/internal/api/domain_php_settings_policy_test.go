package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// phpSettingsWriteRepo records UpdatePHPSettings so a test can tell a refused
// PATCH (no write) from an accepted one.
type phpSettingsWriteRepo struct {
	*mockDomainRepo
	writes []repository.DomainPHPSettings
}

func (r *phpSettingsWriteRepo) UpdatePHPSettings(_ context.Context, _ string, s repository.DomainPHPSettings) error {
	r.writes = append(r.writes, s)
	return nil
}

type phpPolicyFixture struct {
	router   *gin.Engine
	domains  *phpSettingsWriteRepo
	packages *mockPackageRepo
	users    *mockUserRepo
	admin    *bool
	// impersonatedBy, when set, makes the caller an admin acting as u1.
	impersonatedBy *string
}

// newPHPPolicyFixture: tenant u1 owns domain d1 (memory_limit 256M stored) on
// package pkg1 with the given policy. The caller is u1 unless admin is set.
func newPHPPolicyFixture(t *testing.T, policy string) *phpPolicyFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &phpPolicyFixture{admin: new(bool), impersonatedBy: new(string)}
	base := newMockDomainRepo()
	base.domains["d1"] = &models.Domain{ID: "d1", UserID: "u1", Name: "ex.test", PHPMemoryLimit: strp("256M")}
	f.domains = &phpSettingsWriteRepo{mockDomainRepo: base}
	pkgID := "pkg1"
	f.users = &mockUserRepo{users: map[string]*models.User{"u1": {ID: "u1", Username: strp("u1"), PackageID: &pkgID}}}
	f.packages = &mockPackageRepo{packages: map[string]*models.HostingPackage{
		"pkg1": {ID: "pkg1", PHPSettingsPolicy: policy},
	}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if *f.admin {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		} else {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", ImpersonatedBy: *f.impersonatedBy})
		}
		c.Next()
	})
	RegisterDomainPHPSettingsRoutes(r.Group("/api/v1"), DomainPHPSettingsHandlerConfig{
		Domains:  f.domains,
		PHPPools: newMockPHPPoolRepo(),
		Users:    f.users,
		Packages: f.packages,
	})
	f.router = r
	return f
}

func (f *phpPolicyFixture) patch(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/domains/d1/php-settings", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func (f *phpPolicyFixture) get(t *testing.T) (policy map[string]string, editable []string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/d1/php-settings", nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Policy   map[string]string `json:"policy"`
		Editable []string          `json:"editable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Policy, body.Editable
}

const lockMemoryLimit = `{"memory_limit":"admin_only"}`

// A tenant changing a directive their package locks is refused with the
// directive named, and nothing is written.
func TestPHPSettingsPolicy_TenantChangingALockedDirectiveIsRefused(t *testing.T) {
	f := newPHPPolicyFixture(t, lockMemoryLimit)
	w := f.patch(t, map[string]any{"php_memory_limit": "1024M"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Error      string   `json:"error"`
		Directives []string `json:"directives"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error != "php_setting_not_permitted" || !reflect.DeepEqual(body.Directives, []string{"memory_limit"}) {
		t.Fatalf("body = %s, want php_setting_not_permitted naming memory_limit", w.Body.String())
	}
	if len(f.domains.writes) != 0 {
		t.Fatalf("a refused PATCH must write nothing, got %d writes", len(f.domains.writes))
	}
}

// Clearing a locked override (omitting it = nil) is a change too.
func TestPHPSettingsPolicy_TenantClearingALockedDirectiveIsRefused(t *testing.T) {
	f := newPHPPolicyFixture(t, lockMemoryLimit)
	w := f.patch(t, map[string]any{"php_timezone": "UTC"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 (omitting the locked memory_limit clears it), got %d: %s", w.Code, w.Body.String())
	}
}

// The page sends the full set: a locked directive sent back unchanged, with a
// permitted one changed, is accepted.
func TestPHPSettingsPolicy_TenantMayChangePermittedDirectivesAroundALockedOne(t *testing.T) {
	f := newPHPPolicyFixture(t, lockMemoryLimit)
	w := f.patch(t, map[string]any{"php_memory_limit": "256M", "php_timezone": "UTC"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(f.domains.writes) != 1 || f.domains.writes[0].Timezone == nil || *f.domains.writes[0].Timezone != "UTC" {
		t.Fatalf("writes = %+v, want one write with timezone UTC", f.domains.writes)
	}
}

// An admin may set every directive whatever the owner's package says.
func TestPHPSettingsPolicy_AdminMayChangeALockedDirective(t *testing.T) {
	f := newPHPPolicyFixture(t, lockMemoryLimit)
	*f.admin = true
	w := f.patch(t, map[string]any{"php_memory_limit": "1024M"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 for an admin, got %d: %s", w.Code, w.Body.String())
	}
}

// No policy: every directive keeps today's behaviour, the tenant may set it.
func TestPHPSettingsPolicy_EmptyPolicyKeepsTodaysTenantAccess(t *testing.T) {
	f := newPHPPolicyFixture(t, "")
	w := f.patch(t, map[string]any{"php_memory_limit": "1024M", "php_display_errors": true})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}

// An account with no package gets the catalog defaults (GH #282).
func TestPHPSettingsPolicy_NoPackageGetsTheDefaults(t *testing.T) {
	f := newPHPPolicyFixture(t, lockMemoryLimit)
	f.users.users["u1"].PackageID = nil
	w := f.patch(t, map[string]any{"php_memory_limit": "1024M"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 (no package = defaults), got %d: %s", w.Code, w.Body.String())
	}
}

// A policy row that does not parse locks everything for the tenant.
func TestPHPSettingsPolicy_CorruptPolicyFailsClosed(t *testing.T) {
	f := newPHPPolicyFixture(t, `{not json`)
	w := f.patch(t, map[string]any{"php_memory_limit": "256M", "php_timezone": "UTC"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for a corrupt policy, got %d: %s", w.Code, w.Body.String())
	}
}

// Without the users/packages repositories a tenant may change nothing.
func TestPHPSettingsPolicy_UnwiredRepositoriesFailClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := newMockDomainRepo()
	base.domains["d1"] = &models.Domain{ID: "d1", UserID: "u1", Name: "ex.test"}
	domains := &phpSettingsWriteRepo{mockDomainRepo: base}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
		c.Next()
	})
	RegisterDomainPHPSettingsRoutes(r.Group("/api/v1"), DomainPHPSettingsHandlerConfig{Domains: domains})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/domains/d1/php-settings", bytes.NewReader([]byte(`{"php_timezone":"UTC"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK || len(domains.writes) != 0 {
		t.Fatalf("want a refusal and no write without the policy repositories, got %d and %d writes", w.Code, len(domains.writes))
	}
}

// GET tells each caller what they may set: the tenant loses the locked
// directive, the admin keeps everything, and both see the owner's policy.
func TestPHPSettingsPolicy_GetReportsPolicyAndEditable(t *testing.T) {
	f := newPHPPolicyFixture(t, lockMemoryLimit)
	policy, editable := f.get(t)
	if policy["memory_limit"] != "admin_only" || policy["date.timezone"] != "tenant_allowed" {
		t.Fatalf("policy = %v, want memory_limit admin_only and date.timezone tenant_allowed", policy)
	}
	for _, d := range editable {
		if d == "memory_limit" {
			t.Fatalf("tenant editable = %v must not include memory_limit", editable)
		}
	}
	if len(editable) != len(models.PHPSettingCatalog)-1 {
		t.Fatalf("tenant editable = %v, want every catalog directive but memory_limit", editable)
	}
	*f.admin = true
	_, adminEditable := f.get(t)
	if len(adminEditable) != len(models.PHPPolicyDirectives()) {
		t.Fatalf("admin editable = %v, want every policy directive", adminEditable)
	}
}

// Every catalog directive must be compared by changedPHPDirectives; one that
// is not would count as always changed and lock the page for a tenant.
func TestChangedPHPDirectives_ComparesEveryCatalogDirective(t *testing.T) {
	dom := &models.Domain{PHPMemoryLimit: strp("256M")}
	req := updateDomainPHPSettingsRequest{PHPMemoryLimit: strp("256M")}
	if got := changedPHPDirectives(req, dom); len(got) != 0 {
		t.Fatalf("an unchanged request reports changes %v: a catalog directive is missing from changedPHPDirectives", got)
	}
}

// An admin acting as the owner (ADR-0128) has no other page for a domain's PHP
// settings, so the owner's policy does not bind them there.
func TestPHPSettingsPolicy_ImpersonatingAdminMayChangeALockedDirective(t *testing.T) {
	f := newPHPPolicyFixture(t, lockMemoryLimit)
	*f.impersonatedBy = "admin1"
	w := f.patch(t, map[string]any{"php_memory_limit": "1024M"})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 for an impersonating admin, got %d: %s", w.Code, w.Body.String())
	}
	_, editable := f.get(t)
	if len(editable) != len(models.PHPPolicyDirectives()) {
		t.Fatalf("impersonating admin editable = %v, want every policy directive", editable)
	}
}

// GH #1701 Slice 2: the new flags are policy-governed like the rest. A tenant
// turning on a locked short_open_tag is refused and nothing is written.
func TestPHPSettingsPolicy_TenantChangingALockedFlagIsRefused(t *testing.T) {
	f := newPHPPolicyFixture(t, `{"short_open_tag":"admin_only"}`)
	w := f.patch(t, map[string]any{"php_memory_limit": "256M", "php_short_open_tag": true})
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Directives []string `json:"directives"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if !reflect.DeepEqual(body.Directives, []string{"short_open_tag"}) {
		t.Fatalf("directives = %v, want [short_open_tag]", body.Directives)
	}
	if len(f.domains.writes) != 0 {
		t.Fatalf("a refused PATCH must write nothing, got %d writes", len(f.domains.writes))
	}
}

// A permitted flag change is written, and GET returns it.
func TestPHPSettingsPolicy_TenantSetsThePermittedFlags(t *testing.T) {
	f := newPHPPolicyFixture(t, "")
	w := f.patch(t, map[string]any{"php_memory_limit": "256M", "php_log_errors": false, "php_file_uploads": false, "php_short_open_tag": true})
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(f.domains.writes) != 1 {
		t.Fatalf("want one write, got %d", len(f.domains.writes))
	}
	s := f.domains.writes[0]
	if s.LogErrors == nil || *s.LogErrors || s.FileUploads == nil || *s.FileUploads || s.ShortOpenTag == nil || !*s.ShortOpenTag {
		t.Fatalf("write = %+v, want log_errors off, file_uploads off, short_open_tag on", s)
	}
}

// GET returns the stored flag values.
func TestPHPSettings_GetReturnsTheFlags(t *testing.T) {
	f := newPHPPolicyFixture(t, "")
	on, off := true, false
	d := f.domains.domains["d1"]
	d.PHPLogErrors, d.PHPFileUploads, d.PHPShortOpenTag = &off, &off, &on
	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains/d1/php-settings", nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["php_log_errors"] != false || body["php_file_uploads"] != false || body["php_short_open_tag"] != true {
		t.Fatalf("GET body = %s, want the three stored flags", w.Body.String())
	}
}
