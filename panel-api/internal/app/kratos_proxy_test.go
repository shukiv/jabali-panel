package app

import (
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRegisterKratosProxy_StripsPrefixAndForwards(t *testing.T) {
	// Fake Kratos: capture path + method + body + response cookies.
	var gotPath, gotMethod string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		// Emit a Kratos-style Set-Cookie to confirm pass-through.
		http.SetCookie(w, &http.Cookie{Name: "csrf_token_abc", Value: "xyz", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"f","ui":{"action":"","method":"POST","nodes":[]}}`))
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := RegisterKratosProxy(r, upstream.URL, testKratosRL(), nil); err != nil {
		t.Fatalf("RegisterKratosProxy: %v", err)
	}

	srv := httptest.NewServer(r)
	defer srv.Close()

	// GET: flow init path.
	resp, err := http.Get(srv.URL + "/.ory/self-service/login/browser")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if gotMethod != "GET" {
		t.Errorf("method=%q, want GET", gotMethod)
	}
	if gotPath != "/self-service/login/browser" {
		t.Errorf("path=%q, want /self-service/login/browser (prefix must be stripped)", gotPath)
	}
	// Set-Cookie from Kratos must come through — the SPA's CSRF handling
	// depends on it.
	if cookies := resp.Cookies(); len(cookies) == 0 || cookies[0].Name != "csrf_token_abc" {
		t.Errorf("expected csrf_token_abc cookie in response, got %+v", cookies)
	}

	// POST: flow submit path with body, JSON as the panel UI sends it (the
	// limiter reads the account from it and refuses other body types).
	const submitted = `{"method":"password","identifier":"a","password":"b"}`
	body := strings.NewReader(submitted)
	resp2, err := http.Post(
		srv.URL+"/.ory/self-service/login?flow=abc",
		"application/json",
		body,
	)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp2.Body.Close()
	if gotMethod != "POST" {
		t.Errorf("method=%q, want POST", gotMethod)
	}
	if gotPath != "/self-service/login" {
		t.Errorf("path=%q, want /self-service/login (query preserved separately)", gotPath)
	}
	if string(gotBody) != submitted {
		t.Errorf("body=%q, want %s", gotBody, submitted)
	}
}

func TestRegisterKratosProxy_RejectsBadURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// url.Parse is lenient — truly malformed URLs (control chars in host)
	// trip it. Use that to confirm the error path returns rather than panics.
	err := RegisterKratosProxy(r, "http://\x00bad", testKratosRL(), nil)
	if err == nil {
		t.Fatal("expected error for malformed upstream URL, got nil")
	}
}

// M25 Step 3: verify the proxy works against a Unix-socket-bound upstream
// (the `unix:/abs/path` form). Boots a tiny HTTP server on a unix socket,
// registers the proxy with `unix:<sock>`, hits it through gin, and checks
// the path is stripped + the response comes back. End-to-end coverage for
// the parse → rewrite → custom-transport chain.
func TestRegisterKratosProxy_UnixSocketUpstream(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "kratos-public.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	var gotPath string
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := RegisterKratosProxy(r, "unix:"+sockPath, testKratosRL(), nil); err != nil {
		t.Fatalf("RegisterKratosProxy: %v", err)
	}

	front := httptest.NewServer(r)
	defer front.Close()

	resp, err := http.Get(front.URL + "/.ory/self-service/login/browser")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	if gotPath != "/self-service/login/browser" {
		t.Errorf("upstream path=%q, want /self-service/login/browser (prefix must be stripped)", gotPath)
	}
}

// testKratosRL returns a permissive limiter so existing GET-based proxy tests
// pass through KratosFlows (which only gates POST login/recovery).
func testKratosRL() *middleware.RateLimiter {
	return middleware.NewRateLimiter(middleware.RateLimiterConfig{KratosRate: 1000, KratosBurst: 1000})
}
