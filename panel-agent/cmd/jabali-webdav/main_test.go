package main

import (
	"context"
	"io"
	"net"
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

// acquireListener with neither systemd socket activation (LISTEN_FDS) nor a
// --socket path is a hard error — the worker has nothing to listen on.
func TestAcquireListener_NoSocketNoActivation(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	t.Setenv("LISTEN_FDS", "")
	if _, err := acquireListener(""); err == nil {
		t.Fatal("expected an error with no socket and no activation, got nil")
	}
}

// The --socket (test / manual) path binds the path itself at 0660.
func TestAcquireListener_BindPath(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	sock := filepath.Join(t.TempDir(), "dav.sock")
	ln, err := acquireListener(sock)
	if err != nil {
		t.Fatalf("acquireListener(%q): %v", sock, err)
	}
	defer ln.Close()
	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("socket not created: %v", err)
	}
	if fi.Mode().Perm() != 0o660 {
		t.Fatalf("socket mode = %o, want 0660", fi.Mode().Perm())
	}
}

// listenerFromFd is the core of the socket-activation path: systemd binds the
// socket and passes it as an inherited fd; the worker wraps that fd and serves
// WebDAV over it. Exercising a real inherited fd here (rather than the exact
// fd-3 the SD protocol mandates) proves the wrap end-to-end.
func TestListenerFromFd_ServesWebdav(t *testing.T) {
	root := t.TempDir()
	sock := filepath.Join(t.TempDir(), "activated.sock")

	// Stand in for systemd: bind the socket, then hand its fd to the worker.
	raw, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("pre-bind socket: %v", err)
	}
	defer raw.Close()
	f, err := raw.(*net.UnixListener).File()
	if err != nil {
		t.Fatalf("dup listener fd: %v", err)
	}
	defer f.Close()

	ln, err := listenerFromFd(f.Fd(), "test-activated")
	if err != nil {
		t.Fatalf("listenerFromFd: %v", err)
	}
	srv := &http.Server{Handler: newWebdavHandler(root, "")}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
	// PUT over the activated socket, then confirm it landed in root.
	req, _ := http.NewRequest("PUT", "http://unix/note.txt", strings.NewReader("via-fd"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT over activated socket: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT: got %d", resp.StatusCode)
	}
	if b, err := os.ReadFile(filepath.Join(root, "note.txt")); err != nil || string(b) != "via-fd" {
		t.Fatalf("file on disk: %q err=%v", b, err)
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
