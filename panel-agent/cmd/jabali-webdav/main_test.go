package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// do issues a WebDAV request against the test server and returns status + body.
func do(t *testing.T, base, method, path, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func TestWebdavHandler_RoundTrip(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(newWebdavHandler(root, ""))
	defer srv.Close()

	// PUT a file, then GET it back.
	if code, _ := do(t, srv.URL, "PUT", "/hello.txt", "world"); code != http.StatusCreated && code != http.StatusNoContent {
		t.Fatalf("PUT: got %d", code)
	}
	code, body := do(t, srv.URL, "GET", "/hello.txt", "")
	if code != http.StatusOK || body != "world" {
		t.Fatalf("GET: got %d %q", code, body)
	}
	// The file really landed in root (uid-owned in production).
	if b, err := os.ReadFile(filepath.Join(root, "hello.txt")); err != nil || string(b) != "world" {
		t.Fatalf("file on disk: %q err=%v", b, err)
	}

	// MKCOL + PROPFIND work (proves it's real WebDAV, not just GET/PUT).
	if code, _ := do(t, srv.URL, "MKCOL", "/sub", ""); code != http.StatusCreated {
		t.Fatalf("MKCOL: got %d", code)
	}
	if code, _ := do(t, srv.URL, "PROPFIND", "/", ""); code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND: got %d, want 207", code)
	}
}

// A traversal in the request path must not escape the served root — webdav.Dir
// confines lexically; the kernel chroot is the belt in production.
func TestWebdavHandler_NoEscape(t *testing.T) {
	root := t.TempDir()
	// A secret one level above root that must stay unreachable.
	secret := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(secret, []byte("top"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })

	srv := httptest.NewServer(newWebdavHandler(root, ""))
	defer srv.Close()

	// net/http cleans "/../" before it reaches the handler, and webdav.Dir
	// rejects any that survive — either way the secret is not served.
	code, body := do(t, srv.URL, "GET", "/../secret.txt", "")
	if code == http.StatusOK && strings.Contains(body, "top") {
		t.Fatalf("escaped the root: served the parent secret (%d)", code)
	}
}
