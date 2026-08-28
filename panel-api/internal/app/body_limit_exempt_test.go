package app

import (
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBodyLimitExemptRoutes_ExistInRouteTable pins every body-limit
// exemption to the real route table (JAB-246). A typo'd or renamed route
// template would otherwise silently fall back under the global 1 MiB cap
// and break large uploads with no compile-time signal.
func TestBodyLimitExemptRoutes_ExistInRouteTable(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Kratos.PublicURL = "http://127.0.0.1:4433"
	cfg.Auth.Kratos.AdminURL = "http://127.0.0.1:4434"
	r := NewWithDeps(cfg, fullDeps())
	registered := make(map[string]struct{})
	for _, ri := range r.Routes() {
		registered[ri.Method+" "+ri.Path] = struct{}{}
	}
	for key := range bodyLimitExemptRoutes {
		if _, ok := registered[key]; !ok {
			t.Errorf("body-limit exemption %q does not match any registered route", key)
		}
	}
	// Guard the key format itself — a bare path without the method prefix
	// would never match at request time.
	for key := range bodyLimitExemptRoutes {
		if !strings.HasPrefix(key, "POST ") && !strings.HasPrefix(key, "PUT ") && !strings.HasPrefix(key, "PATCH ") {
			t.Errorf("body-limit exemption %q must be keyed \"METHOD /path\"", key)
		}
	}
}

// TestBodyLimit_EngineWide413 proves the cap holds through the real
// engine — before auth, so the pre-auth surface (JAB-246 impact note)
// is covered too: an oversized body 413s without a session.
func TestBodyLimit_EngineWide413(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Kratos.PublicURL = "http://127.0.0.1:4433"
	cfg.Auth.Kratos.AdminURL = "http://127.0.0.1:4434"
	r := NewWithDeps(cfg, fullDeps())

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files/mkdir",
		strings.NewReader(`{"path":"`+strings.Repeat("x", 2<<20)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized JSON through engine: got %d, want 413", w.Code)
	}
}

// TestBodyLimitExempt_UploadRoutesNotCapped is the reverse-direction guard the
// TestBodyLimitExemptRoutes_ExistInRouteTable pinning test lacked: it proves
// each upload-family route is ACTUALLY exempt, by pushing a >1 MiB body through
// the real engine and asserting the global cap does not 413 it. The cap runs
// pre-auth (see TestBodyLimit_EngineWide413), so an exempt route falls through
// to auth/handler (401/403/404/400 — anything but 413); a route that slipped
// out of bodyLimitExemptRoutes 413s here. GH #1044: the chunked DB restore
// (`/databases/:id/restore-chunk`, 10 MiB chunks) and the #1184 admin File
// Manager upload routes were missing and 413'd every chunk.
func TestBodyLimitExempt_UploadRoutesNotCapped(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Kratos.PublicURL = "http://127.0.0.1:4433"
	cfg.Auth.Kratos.AdminURL = "http://127.0.0.1:4434"
	r := NewWithDeps(cfg, fullDeps())

	// One concrete request path per exempt upload-family route (":id"/variant
	// filled with a dummy segment so Gin resolves the template + FullPath).
	paths := []string{
		"/api/v1/files/upload",
		"/api/v1/files/upload-chunk",
		"/api/v1/files/write",
		"/api/v1/databases/x/restore",
		"/api/v1/databases/x/restore-chunk",
		"/api/v1/admin/files/upload",
		"/api/v1/admin/files/upload-chunk",
		"/api/v1/admin/files/write",
		"/api/v1/admin/migrations/x/tarball",
	}
	big := strings.Repeat("x", 2<<20) // 2 MiB, over the 1 MiB global cap
	for _, p := range paths {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader(big))
		req.Header.Set("Content-Type", "application/octet-stream")
		r.ServeHTTP(w, req)
		if w.Code == http.StatusRequestEntityTooLarge {
			t.Errorf("%s 413'd a 2 MiB body — route is missing from bodyLimitExemptRoutes", p)
		}
	}
}
