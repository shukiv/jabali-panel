package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An oversized body on the SSO validate socket gets a 413 before any handler
// reads it, the same cap every JSON route on the panel listener has. The
// listener runs with no repositories, so only the cap can answer.
func TestSSOUDSListener_RefusesAnOversizedBody(t *testing.T) {
	dir, err := os.MkdirTemp("", "ssouds") // short: unix socket paths are capped near 108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, shutdown, err := StartSSOUDSListener(sock, nil, nil, nil, nil, nil, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}},
	}
	post := func(body string) int {
		t.Helper()
		resp, err := client.Post("http://sso/sso/phpmyadmin/validate", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	// "!" is not base64url, so a body that reaches the handler gets its 400
	// before any repository is used.
	if got := post(`{"token":"` + strings.Repeat("!", int(ssoUDSBodyLimitBytes)) + `"}`); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized validate body: got %d, want 413", got)
	}
	if got := post(`{"token":"` + strings.Repeat("!", 64) + `"}`); got != http.StatusBadRequest {
		t.Fatalf("token-sized validate body: got %d, want the handler's 400", got)
	}
}
