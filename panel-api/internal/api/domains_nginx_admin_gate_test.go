package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Regression for the 2026-06-04 decision: nginx_custom_directives +
// nginx_rules are an admin-only field on PATCH /api/v1/domains/:id.
// A tenant that owns the domain MUST still be able to PATCH unrelated
// fields (the gate is silent-drop, not 403). The threat model is
// listed in the handler comment in domains.go.

func TestDomainPatch_TenantNginxDirectivesSilentlyDropped(t *testing.T) {
	ips := &fakeManagedIPsForDomain{rows: []models.ManagedIP{
		{ID: 1, Address: "203.0.113.1", Family: "ipv4", IsDefault: true},
	}}
	dom := &models.Domain{
		ID: "d1", UserID: "u1", Name: "x.com",
		NginxCustomDirectives: strPtr("# untouched"),
	}
	r, repo := setupDomainListenIPHarness(t,
		&auth.AccessClaims{UserID: "u1", IsAdmin: false}, ips, dom)

	body := bytes.NewBufferString(`{
		"is_enabled": true,
		"nginx_custom_directives": "location /panel-leak { root /etc/jabali-panel; }",
		"nginx_rules": [{"type":"proxy_pass","path":"/","target":"http://127.0.0.1:8443"}]
	}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/domains/d1", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	got := repo.domains["d1"]
	if got.NginxCustomDirectives == nil || *got.NginxCustomDirectives != "# untouched" {
		t.Errorf("tenant managed to mutate nginx_custom_directives: got %v", got.NginxCustomDirectives)
	}
	if len(got.NginxRules) != 0 {
		t.Errorf("tenant managed to set nginx_rules: %+v", got.NginxRules)
	}
	if !got.IsEnabled {
		t.Errorf("is_enabled was supposed to flip through; the gate is silent-drop on the nginx fields ONLY, not 403 on the whole patch")
	}
}

func TestDomainPatch_AdminNginxDirectivesPersist(t *testing.T) {
	ips := &fakeManagedIPsForDomain{rows: []models.ManagedIP{
		{ID: 1, Address: "203.0.113.1", Family: "ipv4", IsDefault: true},
	}}
	dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
	r, repo := setupDomainListenIPHarness(t,
		&auth.AccessClaims{UserID: "admin", IsAdmin: true}, ips, dom)

	body := bytes.NewBufferString(`{
		"nginx_custom_directives": "add_header X-Test 1;"
	}`)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/domains/d1", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	got := repo.domains["d1"]
	if got.NginxCustomDirectives == nil || !strings.Contains(*got.NginxCustomDirectives, "X-Test 1") {
		t.Errorf("admin's nginx_custom_directives did not persist: %v", got.NginxCustomDirectives)
	}

	// Sanity-check the response carries the new value.
	var resp struct {
		NginxCustomDirectives *string `json:"nginx_custom_directives"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.NginxCustomDirectives == nil || !strings.Contains(*resp.NginxCustomDirectives, "X-Test 1") {
		t.Errorf("response nginx_custom_directives missing: %+v", resp.NginxCustomDirectives)
	}
}
