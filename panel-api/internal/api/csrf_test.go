package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestEnforceSameOriginForCookieMutations covers the global CSRF guard (GH #460):
// cross-origin cookie mutations are rejected; same-origin, safe-method, and
// Bearer-token requests pass.
func TestEnforceSameOriginForCookieMutations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const host = "panel.example.com:8443"

	cases := []struct {
		name       string
		method     string
		origin     string
		referer    string
		authHeader string
		xfProto    string // X-Forwarded-Proto (nginx sets $scheme); default https
		wantStatus int
	}{
		{"safe GET cross-origin allowed", http.MethodGet, "https://evil.example/", "", "", "https", http.StatusOK},
		{"same-origin POST allowed", http.MethodPost, "https://panel.example.com:8443", "", "", "https", http.StatusOK},
		// JAB-115: same host but different port is a DIFFERENT origin — reject.
		{"same-host diff port rejected", http.MethodPost, "https://panel.example.com", "", "", "https", http.StatusForbidden},
		{"same-host explicit diff port rejected", http.MethodPost, "https://panel.example.com:9999", "", "", "https", http.StatusForbidden},
		// JAB-115: http origin against an https request is cross-origin — reject.
		{"same-authority diff scheme rejected", http.MethodPost, "http://panel.example.com:8443", "", "", "https", http.StatusForbidden},
		{"cross-origin POST rejected", http.MethodPost, "https://evil.example", "", "", "https", http.StatusForbidden},
		{"cross-origin PUT rejected", http.MethodPut, "https://evil.example", "", "", "https", http.StatusForbidden},
		{"cross-origin DELETE rejected", http.MethodDelete, "https://evil.example", "", "", "https", http.StatusForbidden},
		{"bearer token exempt even cross-origin", http.MethodPost, "https://evil.example", "", "Bearer abc123", "https", http.StatusOK},
		{"no origin no referer rejected", http.MethodPost, "", "", "", "https", http.StatusForbidden},
		{"origin absent referer same-origin allowed", http.MethodPost, "", "https://panel.example.com:8443/some/page", "", "https", http.StatusOK},
		{"origin absent referer diff port rejected", http.MethodPost, "", "https://panel.example.com/some/page", "", "https", http.StatusForbidden},
		{"origin absent referer cross-origin rejected", http.MethodPost, "", "https://evil.example/x", "", "https", http.StatusForbidden},
		// Plain-http deployment (no proxy): request scheme comes from XFP=http.
		{"http deployment same-origin allowed", http.MethodPost, "http://panel.example.com:8443", "", "", "http", http.StatusOK},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, engine := gin.CreateTestContext(w)
			engine.Use(EnforceSameOriginForCookieMutations())
			engine.Handle(c.method, "/api/v1/x", func(g *gin.Context) {
				g.Status(http.StatusOK)
			})

			req := httptest.NewRequest(c.method, "/api/v1/x", nil)
			req.Host = host
			if c.xfProto != "" {
				req.Header.Set("X-Forwarded-Proto", c.xfProto)
			}
			if c.origin != "" {
				req.Header.Set("Origin", c.origin)
			}
			if c.referer != "" {
				req.Header.Set("Referer", c.referer)
			}
			if c.authHeader != "" {
				req.Header.Set("Authorization", c.authHeader)
			}
			ctx.Request = req
			engine.ServeHTTP(w, req)

			if w.Code != c.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, c.wantStatus)
			}
		})
	}
}
