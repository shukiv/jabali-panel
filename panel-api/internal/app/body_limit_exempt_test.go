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
