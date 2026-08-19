package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// TestCreateAccess_TenantWithoutDomainRejected is the HTTP-adapter half of the
// JAB-303 parity contract. The shared accept/reject matrix is pinned by the
// logaccess policy truth table; the CLI's delegation is pinned by
// TestLogAccessCreate_RoutesThroughSharedScopePolicy; this test pins the HTTP
// handler's delegation. A non-admin caller that omits domain_id is asking for a
// server-wide log grant, which the shared policy forbids — the handler must
// return 400 before any stream row is created. Empty repos are fine: the
// rejection happens in ValidateGrantScope, ahead of any repository call.
func TestCreateAccess_TenantWithoutDomainRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "tenant1", IsAdmin: false})
		c.Next()
	})
	RegisterLogRoutes(v1, LogHandlerConfig{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs/access",
		strings.NewReader(`{"log_type":"access"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tenant without domain must be rejected 400, got %d (%s)", rec.Code, rec.Body.String())
	}
}
