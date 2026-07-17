package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func newTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	fs := fstest.MapFS{
		"index.html":               {Data: []byte("<html></html>")},
		"assets/index-abc.js":      {Data: []byte("console.log('hi')")},
	}
	RegisterStatic(r, fs)
	return r
}

func TestSPAFallbackSetsNoCache(t *testing.T) {
	r := newTestEngine(t)

	// Deep link — no file match, fallback serves index.html.
	for _, path := range []string{"/", "/domains", "/settings/ssl"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if got := w.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("path %q: Cache-Control = %q, want %q", path, got, "no-cache")
		}
		if got := w.Header().Get("Clear-Site-Data"); got != "" {
			t.Errorf("path %q: Clear-Site-Data = %q, want empty (non-login path)", path, got)
		}
	}
}

func TestLoginFallbackSetsClearSiteData(t *testing.T) {
	r := newTestEngine(t)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-cache")
	}
	want := `"cache"`
	if got := w.Header().Get("Clear-Site-Data"); got != want {
		t.Errorf("Clear-Site-Data = %q, want %q", got, want)
	}
}

func TestAssetResponseHasNoCacheControl(t *testing.T) {
	// Real static assets (hashed .js/.css) must NOT get no-cache —
	// Vite hashes filenames specifically so they can be cached forever.
	r := newTestEngine(t)

	req := httptest.NewRequest(http.MethodGet, "/assets/index-abc.js", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Cache-Control"); got != "" {
		t.Errorf("static asset got Cache-Control = %q, want empty (browser default cacheable)", got)
	}
	if got := w.Header().Get("Clear-Site-Data"); got != "" {
		t.Errorf("static asset got Clear-Site-Data = %q, want empty", got)
	}
}

func TestAPIPathIs404JSON(t *testing.T) {
	r := newTestEngine(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bogus", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct == "" || ct[:16] != "application/json" {
		t.Errorf("Content-Type = %q, want application/json...", ct)
	}
}

// TestMissingAssetReturns404 — a stale hashed chunk (tab open across a deploy)
// must 404, not fall through to index.html; serving text/html for a .js request
// trips the browser MIME check and blanks the SPA. GH: docker-apps blank screen.
func TestMissingAssetReturns404(t *testing.T) {
	r := newTestEngine(t)
	for _, p := range []string{
		"/assets/RowActions-STALE.js",
		"/assets/AdminDockerAppsPage-GONE.js",
		"/assets/index-XXXX.css",
		"/favicon.ico",
	} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("path %q: status = %d, want 404 (must not fall back to SPA shell)", p, w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "" && ct[:9] == "text/html" {
			t.Errorf("path %q: Content-Type = %q, want non-HTML for a missing asset", p, ct)
		}
	}
}

// TestExistingAssetStillServed — a real hashed asset is served (not 404'd).
func TestExistingAssetStillServed(t *testing.T) {
	r := newTestEngine(t)
	req := httptest.NewRequest(http.MethodGet, "/assets/index-abc.js", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("existing asset status = %d, want 200", w.Code)
	}
}

// TestSPARouteStillFallsBack — a genuine deep link (no extension) still serves
// the SPA shell so client routing works.
func TestSPARouteStillFallsBack(t *testing.T) {
	r := newTestEngine(t)
	req := httptest.NewRequest(http.MethodGet, "/jabali-admin/docker-apps", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("SPA deep-link status = %d, want 200 (index.html fallback)", w.Code)
	}
}
