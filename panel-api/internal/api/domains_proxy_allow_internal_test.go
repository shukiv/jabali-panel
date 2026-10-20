package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestValidateProxyPassTarget_AllowInternal pins the nginx_proxy_allow_internal
// policy (PR #2180, vgsbr): on, loopback / private-range targets and localhost
// pass; unix sockets, link-local (cloud metadata), unspecified and directive
// smuggling stay rejected. Off is the JAB-65 behaviour.
func TestValidateProxyPassTarget_AllowInternal(t *testing.T) {
	internal := []string{
		"http://127.0.0.1:3000",
		"http://localhost:8080/app",
		"http://10.0.0.5",
		"http://192.168.1.20:9000",
		"http://172.16.4.4",
		"http://[::1]:3000",
	}
	for _, tg := range internal {
		if err := validateProxyPassTargetWith(tg, false); err == nil {
			t.Errorf("allow off: %q must be rejected", tg)
		}
		if err := validateProxyPassTargetWith(tg, true); err != nil {
			t.Errorf("allow on: %q should pass, got %v", tg, err)
		}
	}
	for _, tg := range []string{
		"http://unix:/run/php/x.sock:/",
		"http://169.254.169.254/latest/meta-data",
		"http://0.0.0.0:80",
		"http://127.0.0.1/; proxy_pass http://169.254.169.254; #",
	} {
		if err := validateProxyPassTargetWith(tg, true); err == nil {
			t.Errorf("allow on: %q must still be rejected", tg)
		}
	}

	rules := models.NginxRules{{Type: "proxy_pass", Path: "/app", Target: "http://127.0.0.1:3000"}}
	if err := validateNginxRules(rules); err == nil {
		t.Errorf("validateNginxRules must stay strict")
	}
	if err := validateNginxRulesWith(rules, true); err != nil {
		t.Errorf("validateNginxRulesWith(allow) should pass, got %v", err)
	}
}

// The guard checks literal addresses, but nginx resolves a proxy_pass host with
// getaddrinfo. That also reads the inet_aton shorthand (127.1), whole-number
// (2130706433), hex (0x7f000001) and octal (0177.0.0.1) forms as IPv4, and
// answers localhost. and *.localhost with loopback. Each of these reached
// loopback or 169.254.169.254 (cloud metadata) past the guard.
func TestValidateProxyPassTarget_NumericAndLocalhostForms(t *testing.T) {
	alwaysRejected := []string{
		"http://127.1:4434/",
		"http://2130706433:4434/",
		"http://0x7f000001:4434/",
		"http://0177.0.0.1:4434/",
		"http://127.0.0.1.:4434/",
		"http://2852039166/latest/meta-data/", // 169.254.169.254
		"http://0xa9.0xfe.0xa9.0xfe/",         // 169.254.169.254
		"http://169.254.43518/",               // 169.254.169.254
		"http://[fe80::1%25eth0]/",            // link-local, with a zone
		// Forms glibc refuses but another resolver may not: refused too.
		"http://0x7f.0x.0x.1/",
		"http://0x.0x.0x.0/",
		"http://127.0.0.08/",
		"http://1.2.3.4.5/",
	}
	for _, allow := range []bool{false, true} {
		for _, tg := range alwaysRejected {
			if err := validateProxyPassTargetWith(tg, allow); err == nil {
				t.Errorf("allow=%v: %q must be rejected", allow, tg)
			}
		}
	}
	for _, tg := range []string{"http://localhost.:3000/", "http://LOCALHOST.:3000/", "http://app.localhost:3000/"} {
		if err := validateProxyPassTargetWith(tg, false); err == nil {
			t.Errorf("allow off: %q must be rejected", tg)
		}
		if err := validateProxyPassTargetWith(tg, true); err != nil {
			t.Errorf("allow on: %q should pass, got %v", tg, err)
		}
	}
	// Hostnames that only contain digits somewhere stay hostnames.
	for _, tg := range []string{
		"http://1.example.com",
		"http://123.example:8080/",
		"http://0x.example/",
		"http://1e100.net/",
		"http://example.com./",
		"http://app1:3000/",
	} {
		for _, allow := range []bool{false, true} {
			if err := validateProxyPassTargetWith(tg, allow); err != nil {
				t.Errorf("allow=%v: %q should pass, got %v", allow, tg, err)
			}
		}
	}
}

// buildProxyAllowRouter is buildSafeOptionsRouter with the server's
// nginx_proxy_allow_internal switch set.
func buildProxyAllowRouter(claims *auth.AccessClaims, allow bool, dom *models.Domain) (*gin.Engine, *mockDomainRepo) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) { ginctx.SetClaims(c, claims); c.Next() })
	repo := newMockDomainRepo()
	repo.domains[dom.ID] = dom
	settings := &mockServerSettingsRepo{getResult: &models.ServerSettings{NginxProxyAllowInternal: allow}}
	RegisterDomainRoutes(v1, DomainHandlerConfig{Domains: repo, ServerSettings: settings})
	return r, repo
}

// An admin's proxy_pass rule to an app on this host is saved only while the
// server allows internal targets. Cloud metadata stays refused either way, and
// a tenant still can't set proxy_pass at all.
func TestDomainPatch_AdminProxyPassInternal(t *testing.T) {
	admin := &auth.AccessClaims{UserID: "admin", IsAdmin: true}
	local := `{"nginx_rules":[{"type":"proxy_pass","path":"/","target":"http://127.0.0.1:3000"}]}`
	for _, tc := range []struct {
		allow bool
		want  int
	}{{false, http.StatusBadRequest}, {true, http.StatusOK}} {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, repo := buildProxyAllowRouter(admin, tc.allow, dom)
		if code := patchNginxRules(t, r, local); code != tc.want {
			t.Fatalf("allow=%v: status %d, want %d", tc.allow, code, tc.want)
		}
		if saved := len(repo.domains["d1"].NginxRules); (saved == 1) != tc.allow {
			t.Errorf("allow=%v: %d rules saved", tc.allow, saved)
		}
	}

	dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
	r, _ := buildProxyAllowRouter(admin, true, dom)
	metadata := `{"nginx_rules":[{"type":"proxy_pass","path":"/","target":"http://2852039166/"}]}`
	if code := patchNginxRules(t, r, metadata); code != http.StatusBadRequest {
		t.Errorf("metadata target with the switch on: status %d, want 400", code)
	}

	r, _ = buildProxyAllowRouter(&auth.AccessClaims{UserID: "u1"}, true, dom)
	if code := patchNginxRules(t, r, local); code == http.StatusOK && len(dom.NginxRules) != 0 {
		t.Errorf("a tenant set proxy_pass with the switch on: %+v", dom.NginxRules)
	}
}

// rdcProxySettings is a server whose owners may set domain options, with the
// nginx_proxy_allow_internal switch set.
type rdcProxySettings bool

func (o rdcProxySettings) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{Hostname: "panel.example.net", TenantDomainOptionsEnabled: true, NginxProxyAllowInternal: bool(o)}, nil
}

// A restored admin rule passes the same check as one saved in the Rule
// Builder: an internal target is kept only while the server allows it. A rule
// in an uploaded file still has to pass the tenant rules, which have no
// proxy_pass.
func TestRestoreWebCheck_ProxyAllowInternal(t *testing.T) {
	rules := func() models.NginxRules {
		return models.NginxRules{{Type: "proxy_pass", Path: "/app/", Target: "http://127.0.0.1:3000"}}
	}
	for _, tc := range []struct {
		allow  bool
		source RestoreSource
		kept   bool
	}{
		{false, RestoreFromOwnBackup, false},
		{true, RestoreFromOwnBackup, true},
		{true, RestoreFromUpload, false},
	} {
		row := rdcRow("shop.org")
		row.NginxRules = rules()
		check := RestoreDomainCheck(rdcDomains{}, rdcAliases{}, rdcProxySettings(tc.allow), nil, tc.source)
		w, _ := check(context.Background(), row, "alice")
		if kept := len(row.NginxRules) == 1; kept != tc.kept {
			t.Errorf("allow=%v source=%v: kept=%v, want %v; warnings: %s", tc.allow, tc.source, kept, tc.kept, strings.Join(w, "; "))
		}
	}
}

// The switch is validation policy only: saving it stores the flag and
// re-applies nothing to nginx.
func TestServerSettingsPatch_NginxProxyAllowInternal(t *testing.T) {
	mockRepo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22}}
	mockAgent := agent.NewMockClient()
	r := settingsRouter(true, mockRepo, mockAgent)

	body, _ := json.Marshal(map[string]any{"nginx_proxy_allow_internal": true})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(t, mockRepo.getResult.NginxProxyAllowInternal)

	require.Never(t, func() bool {
		return nginxCommandDispatched(mockAgent, "nginx.tunables.apply") ||
			nginxCommandDispatched(mockAgent, "nginx.cache.capacity_apply")
	}, 300*time.Millisecond, 10*time.Millisecond, "saving the switch must not re-apply nginx")
}
