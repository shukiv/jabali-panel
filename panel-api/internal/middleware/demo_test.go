//go:build demo

package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func newDemoRouter(t *testing.T, allowed []string, banner string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(DemoMode(allowed, banner))
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		r.Handle(m, "/api/v1/domains", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	}
	r.Handle("POST", "/api/v1/auth/login", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.GET("/info", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.POST("/sso/webmail", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	return r
}

func do(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	r.ServeHTTP(w, req)
	return w
}

func TestDemoMode_AllowsReadsAndPreflights(t *testing.T) {
	r := newDemoRouter(t, nil, "")
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		w := do(r, m, "/api/v1/domains")
		if w.Code != http.StatusOK {
			t.Errorf("%s should pass, got %d", m, w.Code)
		}
	}
}

func TestDemoMode_BlocksWrites(t *testing.T) {
	r := newDemoRouter(t, nil, "")
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		w := do(r, m, "/api/v1/domains")
		if w.Code != http.StatusForbidden {
			t.Errorf("%s should 403, got %d", m, w.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: body not JSON: %v", m, err)
		}
		if body["error"] != "demo_mode" {
			t.Errorf("%s: expected error=demo_mode, got %v", m, body["error"])
		}
	}
}

func TestDemoMode_BannerInPayload(t *testing.T) {
	r := newDemoRouter(t, nil, "Custom banner copy 123")
	w := do(r, "POST", "/api/v1/domains")
	if !strings.Contains(w.Body.String(), "Custom banner copy 123") {
		t.Errorf("banner not in payload: %s", w.Body.String())
	}
}

func TestDemoMode_AllowedPrefixesPassThrough(t *testing.T) {
	r := newDemoRouter(t, []string{"/api/v1/auth"}, "")
	w := do(r, "POST", "/api/v1/auth/login")
	if w.Code != http.StatusOK {
		t.Errorf("POST allowed-prefix should pass, got %d", w.Code)
	}
}

func TestDemoMode_NonAPIWritesPassThrough(t *testing.T) {
	r := newDemoRouter(t, nil, "")
	w := do(r, "POST", "/sso/webmail")
	if w.Code != http.StatusOK {
		t.Errorf("non-/api/v1 POST should pass, got %d", w.Code)
	}
}