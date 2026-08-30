package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestMailLogs_DomainScope covers the GH #1387 drill-down ?domain= scope: a
// non-admin can narrow only to a domain they own; a foreign domain returns
// empty WITHOUT reaching the agent (no cross-tenant log leak); an admin narrows
// to any.
func TestMailLogs_DomainScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	domainsRepo := mdDomainRepo{ds: []models.Domain{
		{ID: "da", UserID: "u1", Name: "a.test"},
		{ID: "db", UserID: "u1", Name: "b.test"},
	}}

	run := func(isAdmin bool, query string) (code int, domainNames []string, agentCalls int) {
		var captured []string
		ag := &mockAgent{callFn: func(_ context.Context, _ string, params any) (json.RawMessage, error) {
			if m, ok := params.(map[string]any); ok {
				if dn, ok := m["domain_names"].([]string); ok {
					captured = dn
				}
			}
			return json.RawMessage(`{"entries":[],"total":0}`), nil
		}}
		r := gin.New()
		r.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: isAdmin})
			c.Next()
		})
		RegisterMailLogsRoutes(r.Group("/api/v1"), MailLogsHandlerConfig{Domains: domainsRepo, Agent: ag})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/mail/logs"+query, nil))
		return w.Code, captured, ag.callCount
	}

	if _, dn, _ := run(false, ""); !equalSet(dn, []string{"a.test", "b.test"}) {
		t.Fatalf("no-domain: scope = %v, want all owned", dn)
	}
	if _, dn, _ := run(false, "?domain=a.test"); !equalSet(dn, []string{"a.test"}) {
		t.Fatalf("owned domain: scope = %v, want [a.test]", dn)
	}
	if code, _, calls := run(false, "?domain=evil.test"); code != http.StatusOK || calls != 0 {
		t.Fatalf("foreign domain: code=%d agentCalls=%d, want 200 with the agent NOT called (no leak)", code, calls)
	}
	if _, dn, _ := run(true, "?domain=any.test"); !equalSet(dn, []string{"any.test"}) {
		t.Fatalf("admin: scope = %v, want [any.test]", dn)
	}
}
