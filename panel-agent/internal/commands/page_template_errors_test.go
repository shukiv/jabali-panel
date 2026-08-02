package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// pointPageDirsAtTemp redirects every page-template path at a temp dir and
// stubs the nginx reload; returns a counter of reload invocations.
func pointPageDirsAtTemp(t *testing.T) *int {
	t.Helper()
	base := t.TempDir()
	origErr, origDis, origUnc, origCatch := errorPagesDir, disabledPageDir, unconfiguredPageDir, catchallConfPath
	origReload := nginxTestAndReload
	errorPagesDir = filepath.Join(base, "errors")
	disabledPageDir = filepath.Join(base, "disabled")
	unconfiguredPageDir = filepath.Join(base, "unconfigured")
	catchallConfPath = filepath.Join(base, "jabali-catchall.conf")
	reloads := 0
	nginxTestAndReload = func(context.Context) error { reloads++; return nil }
	t.Cleanup(func() {
		errorPagesDir, disabledPageDir, unconfiguredPageDir, catchallConfPath = origErr, origDis, origUnc, origCatch
		nginxTestAndReload = origReload
	})
	return &reloads
}

func callSyncErrorPages(t *testing.T, params map[string]any) syncErrorPagesResponse {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := syncErrorPagesHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	resp, ok := out.(syncErrorPagesResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", out)
	}
	return resp
}

// GH #860: the sync writes all five branded pages and converges the
// catch-all include, reloading nginx only when the catch-all changed.
func TestSyncErrorPages_WritesPagesAndConvergesCatchall(t *testing.T) {
	reloads := pointPageDirsAtTemp(t)

	resp := callSyncErrorPages(t, map[string]any{
		"error_404":            "<h1>404</h1>",
		"error_403":            "<h1>403</h1>",
		"error_500":            "<h1>500</h1>",
		"domain_disabled":      "<h1>disabled</h1>",
		"domain_unconfigured":  "<h1>unconfigured</h1>",
		"unconfigured_enabled": true,
	})
	if len(resp.Written) != 5 {
		t.Fatalf("written = %v, want 5 files", resp.Written)
	}
	if !resp.CatchallChanged {
		t.Fatal("first converge must report catchall changed")
	}
	if *reloads != 1 {
		t.Fatalf("reloads = %d, want 1", *reloads)
	}
	for path, want := range map[string]string{
		filepath.Join(disabledPageDir, "index.html"):     "<h1>disabled</h1>",
		filepath.Join(unconfiguredPageDir, "index.html"): "<h1>unconfigured</h1>",
		filepath.Join(errorPagesDir, "jabali-err-404.html"): "<h1>404</h1>",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
	if got, _ := os.ReadFile(catchallConfPath); string(got) != catchallOnConf {
		t.Fatalf("catchall = %q, want ON conf", got)
	}

	// Same params again: catch-all already converged — no second reload.
	resp = callSyncErrorPages(t, map[string]any{"unconfigured_enabled": true})
	if resp.CatchallChanged || *reloads != 1 {
		t.Fatalf("steady-state converge must be a no-op (changed=%v reloads=%d)", resp.CatchallChanged, *reloads)
	}

	// Toggle off: back to the 444 drop, one more reload.
	resp = callSyncErrorPages(t, map[string]any{"unconfigured_enabled": false})
	if !resp.CatchallChanged || *reloads != 2 {
		t.Fatalf("toggle off must rewrite + reload (changed=%v reloads=%d)", resp.CatchallChanged, *reloads)
	}
	if got, _ := os.ReadFile(catchallConfPath); string(got) != catchallOffConf {
		t.Fatalf("catchall = %q, want OFF conf", got)
	}
}

// A panel that predates the toggle omits unconfigured_enabled entirely —
// the agent must leave the catch-all alone and not touch nginx.
func TestSyncErrorPages_NilToggleLeavesCatchallAlone(t *testing.T) {
	reloads := pointPageDirsAtTemp(t)

	resp := callSyncErrorPages(t, map[string]any{"error_404": "<h1>x</h1>"})
	if resp.CatchallChanged || *reloads != 0 {
		t.Fatalf("nil toggle must not converge catchall (changed=%v reloads=%d)", resp.CatchallChanged, *reloads)
	}
	if _, err := os.Stat(catchallConfPath); !os.IsNotExist(err) {
		t.Fatalf("catchall must not be created on nil toggle (err=%v)", err)
	}
	// Empty bodies are skipped, only error_404 lands.
	if len(resp.Written) != 1 {
		t.Fatalf("written = %v, want just the 404", resp.Written)
	}
}
