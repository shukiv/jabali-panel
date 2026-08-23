package api

// JAB-380 — recent-auth (step-up) gate unit tests.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

func newStepUpCtx(t *testing.T, claims *auth.AccessClaims, cookie string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/files", nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: cookie})
	}
	c.Request = req
	if claims != nil {
		ginctx.SetClaims(c, claims)
	}
	return c, w
}

func TestRequireRecentAuth_FreshCachedClaimsPass(t *testing.T) {
	c, w := newStepUpCtx(t, &auth.AccessClaims{
		UserID: "u1", IsAdmin: true, Source: auth.SourceKratos,
		AuthenticatedAt: time.Now().Add(-1 * time.Minute),
	}, "")
	// kc=nil: fast path must succeed on fresh claims without any remote call.
	if !requireRecentAuth(c, nil, stepUpWindow) {
		t.Fatalf("fresh claims should pass; got %d %s", w.Code, w.Body.String())
	}
	if c.IsAborted() {
		t.Fatalf("context must not be aborted on pass")
	}
}

func TestRequireRecentAuth_StaleClaimsRequireStepUp(t *testing.T) {
	c, w := newStepUpCtx(t, &auth.AccessClaims{
		UserID: "u1", IsAdmin: true, Source: auth.SourceKratos,
		AuthenticatedAt: time.Now().Add(-30 * time.Minute),
	}, "")
	if requireRecentAuth(c, nil, stepUpWindow) {
		t.Fatalf("stale claims must not pass")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	assertErrCode(t, w, "stepup_required")
}

func TestRequireRecentAuth_ZeroAuthenticatedAtFailsClosed(t *testing.T) {
	c, w := newStepUpCtx(t, &auth.AccessClaims{
		UserID: "u1", IsAdmin: true, Source: auth.SourceKratos,
		// zero AuthenticatedAt (Kratos omitted / unparseable)
	}, "")
	if requireRecentAuth(c, nil, stepUpWindow) {
		t.Fatalf("zero authenticated_at must fail closed")
	}
	assertErrCode(t, w, "stepup_required")
}

func TestRequireRecentAuth_TokenSourceUnavailable(t *testing.T) {
	c, w := newStepUpCtx(t, &auth.AccessClaims{
		UserID: "u1", IsAdmin: true, Source: auth.SourceUserAPIToken,
		AuthenticatedAt: time.Now(), // even "fresh" doesn't matter
	}, "")
	if requireRecentAuth(c, nil, stepUpWindow) {
		t.Fatalf("API-token source must be refused")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	assertErrCode(t, w, "stepup_unavailable")
}

func TestRequireRecentAuth_ImpersonationUnavailable(t *testing.T) {
	c, w := newStepUpCtx(t, &auth.AccessClaims{
		UserID: "target", IsAdmin: true, Source: auth.SourceKratos,
		ImpersonatedBy:  "realadmin",
		AuthenticatedAt: time.Now(),
	}, "")
	if requireRecentAuth(c, nil, stepUpWindow) {
		t.Fatalf("impersonated request must be refused from root surfaces")
	}
	assertErrCode(t, w, "stepup_unavailable")
}

func TestRequireRecentAuth_NilClaimsUnauthorized(t *testing.T) {
	c, w := newStepUpCtx(t, nil, "")
	if requireRecentAuth(c, nil, stepUpWindow) {
		t.Fatalf("nil claims must not pass")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

// TestRequireRecentAuth_CacheBypassSeesRefresh: cached claims are stale, but a
// fresh (cache-bypassing) whoami against the request cookie sees a bumped
// authenticated_at — the refresh-loop fix. The gate must pass.
func TestRequireRecentAuth_CacheBypassSeesRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":               "s",
			"active":           true,
			"authenticated_at": time.Now().UTC().Format(time.RFC3339Nano), // fresh
			"identity":         map[string]any{"id": "u1"},
		})
	}))
	defer server.Close()
	kc := kratosclient.NewClient(server.URL, server.URL)

	c, w := newStepUpCtx(t, &auth.AccessClaims{
		UserID: "u1", IsAdmin: true, Source: auth.SourceKratos,
		AuthenticatedAt: time.Now().Add(-30 * time.Minute), // stale cached value
	}, "cookie-refreshed")

	if !requireRecentAuth(c, kc, stepUpWindow) {
		t.Fatalf("cache-bypass should see the fresh authenticated_at and pass; got %d %s", w.Code, w.Body.String())
	}
}

// TestRequireRecentAuth_CacheBypassStillStale: stale claims AND the fresh whoami
// still reports a stale authenticated_at → step-up required.
func TestRequireRecentAuth_CacheBypassStillStale(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":               "s",
			"active":           true,
			"authenticated_at": time.Now().Add(-45 * time.Minute).UTC().Format(time.RFC3339Nano),
			"identity":         map[string]any{"id": "u1"},
		})
	}))
	defer server.Close()
	kc := kratosclient.NewClient(server.URL, server.URL)

	c, w := newStepUpCtx(t, &auth.AccessClaims{
		UserID: "u1", IsAdmin: true, Source: auth.SourceKratos,
		AuthenticatedAt: time.Now().Add(-30 * time.Minute),
	}, "cookie-old")

	if requireRecentAuth(c, kc, stepUpWindow) {
		t.Fatalf("still-stale session must require step-up")
	}
	assertErrCode(t, w, "stepup_required")
}

func assertErrCode(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	if body.Error != want {
		t.Fatalf("error code = %q, want %q", body.Error, want)
	}
}
