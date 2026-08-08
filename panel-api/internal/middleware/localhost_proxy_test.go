package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// panel-api is fronted by nginx over /run/jabali-panel/api.sock (ADR-0050), so
// a proxied request and a direct agent call BOTH arrive with RemoteAddr "@".
// RequireLocalhost accepted that unconditionally, which made every route whose
// only gate is this middleware — /api/v1/internal/* and the malware ingest —
// reachable unauthenticated from the public internet.
//
// Verified against a live panel before the fix: a public POST to
// /api/v1/internal/notifications/enqueue returned the handler's own
// {"error":"event_kind not in allowlist"} rather than a 403, while
// /api/v1/domains correctly returned 401.
//
// nginx always sets forwarding headers; the agent dialling the socket never
// does. Their presence is proof the request came through the proxy.
func TestRequireLocalhost_RejectsProxiedUnixSocketRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)

	proxyHeaders := []string{"X-Forwarded-For", "X-Real-IP", "X-Forwarded-Proto", "X-Forwarded-Host"}
	for _, h := range proxyHeaders {
		t.Run("rejects "+h, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/internal/notifications/enqueue", nil)
			c.Request.RemoteAddr = "@" // unix socket, exactly as nginx-proxied traffic appears
			c.Request.Header.Set(h, "203.0.113.7")

			RequireLocalhost()(c)

			if w.Code != http.StatusForbidden {
				t.Errorf("proxied request with %s was allowed (HTTP %d) — this is the internet-exposure bug", h, w.Code)
			}
			if !c.IsAborted() {
				t.Errorf("%s: handler chain was not aborted", h)
			}
		})
	}
}

// The agent's own call must still work: unix socket, no proxy headers.
func TestRequireLocalhost_AllowsDirectAgentSocketCall(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, remote := range []string{"@", ""} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/internal/notifications/enqueue", nil)
		c.Request.RemoteAddr = remote

		RequireLocalhost()(c)

		if c.IsAborted() {
			t.Errorf("direct agent call with RemoteAddr %q was blocked — this breaks agent→panel notifications", remote)
		}
	}
}

// Loopback TCP (the legacy bind) keeps working, and a non-loopback peer is
// still refused — the pre-existing behaviour, unchanged.
func TestRequireLocalhost_TCPBehaviourUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JABALI_ALLOW_TCP_INTERNAL", "")

	// Loopback TCP is now refused by default (see
	// TestRequireLocalhost_RejectsLoopbackTCPByDefault for why); non-loopback
	// was and remains refused.
	cases := []struct {
		remote  string
		allowed bool
	}{
		{"127.0.0.1:54321", false},
		{"[::1]:54321", false},
		{"203.0.113.7:54321", false},
		{"10.0.0.5:1234", false},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/internal/x", nil)
		c.Request.RemoteAddr = tc.remote

		RequireLocalhost()(c)

		if got := !c.IsAborted(); got != tc.allowed {
			t.Errorf("remote %s: allowed=%v, want %v", tc.remote, got, tc.allowed)
		}
	}
}
