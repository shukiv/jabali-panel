package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// CheckOrigin gates every log-stream WebSocket handshake. Bug here =
// CSRF-via-WS escape valve, so the matrix is exhaustive.

func mkReq(t *testing.T, host, origin string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/logs/stream/abc", nil)
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func TestCheckLogStreamOrigin_AllowsMissingHeader(t *testing.T) {
	// Native WS clients (curl, ws CLI, panel-api self-call) don't send
	// Origin. They can't be CSRF'd because there's no browser ambient
	// credential to leak — allow.
	if !checkLogStreamOrigin(mkReq(t, "panel.example.com:8443", "")) {
		t.Fatal("missing Origin should be allowed")
	}
}

func TestCheckLogStreamOrigin_AllowsSameOriginHTTPS(t *testing.T) {
	if !checkLogStreamOrigin(mkReq(t, "panel.example.com:8443", "https://panel.example.com:8443")) {
		t.Fatal("same-host origin should be allowed")
	}
}

func TestCheckLogStreamOrigin_AllowsSameOriginHTTP(t *testing.T) {
	// Dev http listener — scheme not compared, only host:port.
	if !checkLogStreamOrigin(mkReq(t, "panel.example.com:8443", "http://panel.example.com:8443")) {
		t.Fatal("same-host origin (http scheme) should be allowed")
	}
}

func TestCheckLogStreamOrigin_RejectsCrossHost(t *testing.T) {
	if checkLogStreamOrigin(mkReq(t, "panel.example.com:8443", "https://attacker.example.com")) {
		t.Fatal("cross-host origin must be rejected")
	}
}

func TestCheckLogStreamOrigin_RejectsCrossPort(t *testing.T) {
	// Panel on :8443; origin claims :443 — same hostname but different
	// listener. Could be a co-tenant on the same FQDN exfiltrating.
	if checkLogStreamOrigin(mkReq(t, "panel.example.com:8443", "https://panel.example.com:443")) {
		t.Fatal("cross-port origin must be rejected")
	}
}

func TestCheckLogStreamOrigin_RejectsSubdomain(t *testing.T) {
	// Subdomain (`api.panel.example.com`) is NOT same-origin even
	// though it shares the parent domain — browsers treat them as
	// distinct origins.
	if checkLogStreamOrigin(mkReq(t, "panel.example.com:8443", "https://api.panel.example.com:8443")) {
		t.Fatal("subdomain origin must be rejected")
	}
}

func TestCheckLogStreamOrigin_RejectsMalformedOrigin(t *testing.T) {
	// "null" (sandboxed iframe), "https://" (no host), random garbage —
	// none should pass.
	for _, bad := range []string{
		"null",
		"https://",
		"javascript:alert(1)",
		"file://",
		"data:text/html,",
	} {
		if checkLogStreamOrigin(mkReq(t, "panel.example.com:8443", bad)) {
			t.Errorf("malformed origin %q must be rejected", bad)
		}
	}
}

// --- renderGoAccessHTTP regression coverage ----------------------------------
//
// The original WS-driven goaccess path pushed HTML into an iframe srcdoc
// which inherited the panel's strict CSP and could not execute GoAccess's
// `new Function(...)` template compiler. The HTTP-render endpoint must
// always reply with the goaccess-scoped CSP that allows 'unsafe-eval', so
// the iframe (loaded by URL, not srcdoc) can run that script.

type fakeLogStreamRepo struct {
	stream *models.LogAccessStream
	err    error
}

func (f *fakeLogStreamRepo) Create(ctx context.Context, s *models.LogAccessStream) error {
	return nil
}
func (f *fakeLogStreamRepo) ReserveWithinCap(ctx context.Context, s *models.LogAccessStream, max int) error {
	return nil
}
func (f *fakeLogStreamRepo) FindByStreamKey(ctx context.Context, k string) (*models.LogAccessStream, error) {
	return f.stream, f.err
}
func (f *fakeLogStreamRepo) DeleteByID(ctx context.Context, id string) error { return nil }
func (f *fakeLogStreamRepo) CleanupExpired(ctx context.Context) (int64, error) {
	return 0, nil
}

const validHexStreamKey = "0123456789abcdef0123456789abcdef" // 32-char hex

func newGoAccessRouter(repo *fakeLogStreamRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterLogStreamRoutes(r, LogStreamHandlerConfig{
		LogAccessStreams: repo,
		Domains:          nil,
		Log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return r
}

func TestRenderGoAccessHTTP_RejectsBadStreamKey(t *testing.T) {
	r := newGoAccessRouter(&fakeLogStreamRepo{})
	req := httptest.NewRequest("GET", "/logs/stream/not-hex-key/goaccess.html", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400 for bad key, got %d", w.Code)
	}
}

func TestRenderGoAccessHTTP_RejectsStreamNotFound(t *testing.T) {
	r := newGoAccessRouter(&fakeLogStreamRepo{err: repository.ErrNotFound})
	req := httptest.NewRequest("GET", "/logs/stream/"+validHexStreamKey+"/goaccess.html", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("expected 404 for missing stream, got %d", w.Code)
	}
}

func TestRenderGoAccessHTTP_RejectsNonGoaccessStream(t *testing.T) {
	r := newGoAccessRouter(&fakeLogStreamRepo{stream: &models.LogAccessStream{
		LogType:   "access",
		ExpiresAt: time.Now().Add(time.Hour),
	}})
	req := httptest.NewRequest("GET", "/logs/stream/"+validHexStreamKey+"/goaccess.html", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected 400 for wrong log type, got %d", w.Code)
	}
}

func TestRenderGoAccessHTTP_RejectsExpiredStream(t *testing.T) {
	r := newGoAccessRouter(&fakeLogStreamRepo{stream: &models.LogAccessStream{
		LogType:   "goaccess",
		ExpiresAt: time.Now().Add(-time.Hour),
	}})
	req := httptest.NewRequest("GET", "/logs/stream/"+validHexStreamKey+"/goaccess.html", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 410 {
		t.Fatalf("expected 410 Gone for expired stream, got %d", w.Code)
	}
}

func TestRenderGoAccessHTTP_SetsRelaxedCSPHeaderEvenWhenLogMissing(t *testing.T) {
	// System (DomainID nil) stream — log path is /var/log/nginx/access.log
	// which won't exist in the test env. The headers are set BEFORE the
	// os.Stat check, so we still see the relaxed CSP on the
	// "no access log yet" 200 fallback. This is the key regression: the
	// iframe MUST load with unsafe-eval in script-src or GoAccess's
	// templating dies under the panel's strict global CSP.
	r := newGoAccessRouter(&fakeLogStreamRepo{stream: &models.LogAccessStream{
		LogType:   "goaccess",
		DomainID:  nil,
		ExpiresAt: time.Now().Add(time.Hour),
	}})
	req := httptest.NewRequest("GET", "/logs/stream/"+validHexStreamKey+"/goaccess.html", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%q", w.Code, w.Body.String())
	}
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatalf("missing Content-Security-Policy header")
	}
	for _, want := range []string{"'unsafe-eval'", "'unsafe-inline'", "frame-ancestors 'self'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got: %s", want, csp)
		}
	}
	if got := w.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q; want SAMEORIGIN", got)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q; want text/html...", got)
	}
}
