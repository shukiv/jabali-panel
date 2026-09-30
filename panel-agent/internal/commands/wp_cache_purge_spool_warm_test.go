package commands

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

type warmCall struct {
	host        string
	paths       []string
	wholeDomain bool
}

// spoolWarmFixture gives the watcher a temp vhost dir whose <host>.conf root is
// a temp docroot owned by the test user, stubs the purge with purgeErr, and
// records every warm the watcher starts.
func spoolWarmFixture(t *testing.T, purgeErr error) (string, *[]warmCall) {
	t.Helper()
	return spoolWarmFixtureVhost(t, purgeErr, "    location ~ \\.php$ {\n        fastcgi_cache jabali_fcgi;\n    }\n")
}

// spoolWarmFixtureVhost is spoolWarmFixture with extra vhost body lines (the
// cache directive, or none for a cache-off domain).
func spoolWarmFixtureVhost(t *testing.T, purgeErr error, extra string) (string, *[]warmCall) {
	t.Helper()
	const host = "blog.example"
	sites := t.TempDir()
	docroot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sites, host+".conf"),
		[]byte("server {\n    root "+docroot+";\n"+extra+"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	origSites, origPurge, origWarm := wpPurgeSitesDir, wpPurgeRunPurge, wpPurgeStartWarm
	t.Cleanup(func() { wpPurgeSitesDir, wpPurgeRunPurge, wpPurgeStartWarm = origSites, origPurge, origWarm })
	wpPurgeSitesDir = sites
	wpPurgeRunPurge = func(context.Context, json.RawMessage) (any, error) { return nil, purgeErr }
	var mu sync.Mutex
	var warms []warmCall
	wpPurgeStartWarm = func(_ context.Context, h string, paths []string, whole bool, _ *slog.Logger) {
		mu.Lock()
		defer mu.Unlock()
		warms = append(warms, warmCall{h, append([]string(nil), paths...), whole})
	}
	return host, &warms
}

func spoolItem(t *testing.T, host string, uid int, paths []string) *wpPurgeItem {
	t.Helper()
	f := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(f, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &wpPurgeItem{path: f, uid: uint64(uid), username: "tenant", host: host, paths: paths}
}

// A post edit purges home + the post, then warms exactly those paths so the
// next visitor gets a cache HIT instead of a cold render.
func TestCoalesceAndPurge_WarmsThePurgedPaths(t *testing.T) {
	host, warms := spoolWarmFixture(t, nil)
	coalesceAndPurge(context.Background(), []*wpPurgeItem{
		spoolItem(t, host, os.Getuid(), []string{"/", "/hello-world/"}),
	}, quietLog())
	want := []warmCall{{host, []string{"/", "/hello-world/"}, false}}
	if !reflect.DeepEqual(*warms, want) {
		t.Fatalf("warms = %+v, want %+v", *warms, want)
	}
}

func TestCoalesceAndPurge_WholeDomainPurgeWarmsWholeDomain(t *testing.T) {
	host, warms := spoolWarmFixture(t, nil)
	coalesceAndPurge(context.Background(), []*wpPurgeItem{spoolItem(t, host, os.Getuid(), nil)}, quietLog())
	if len(*warms) != 1 || !(*warms)[0].wholeDomain || (*warms)[0].host != host {
		t.Fatalf("warms = %+v, want one whole-domain warm for %s", *warms, host)
	}
}

func TestCoalesceAndPurge_NoWarmWhenThePurgeFails(t *testing.T) {
	host, warms := spoolWarmFixture(t, errors.New("nginx down"))
	coalesceAndPurge(context.Background(), []*wpPurgeItem{spoolItem(t, host, os.Getuid(), []string{"/"})}, quietLog())
	if len(*warms) != 0 {
		t.Fatalf("warms = %+v, want none after a failed purge", *warms)
	}
}

// A domain with the page cache off still purges (a no-op) but is not warmed:
// nginx would store nothing, so a warm is only extra PHP renders.
func TestCoalesceAndPurge_NoWarmWhenTheCacheIsOff(t *testing.T) {
	for name, extra := range map[string]string{
		"no directive":      "    location ~ \\.php$ {\n        fastcgi_pass unix:/run/php/x.sock;\n        fastcgi_cache_valid 200 5m;\n    }\n",
		"fastcgi_cache off": "    fastcgi_cache off;\n",
	} {
		t.Run(name, func(t *testing.T) {
			host, warms := spoolWarmFixtureVhost(t, nil, extra)
			purged := false
			wpPurgeRunPurge = func(context.Context, json.RawMessage) (any, error) { purged = true; return nil, nil }
			coalesceAndPurge(context.Background(), []*wpPurgeItem{spoolItem(t, host, os.Getuid(), []string{"/"})}, quietLog())
			if !purged {
				t.Fatal("the purge should still run for a cache-off domain")
			}
			if len(*warms) != 0 {
				t.Fatalf("warms = %+v, want none for a cache-off domain", *warms)
			}
		})
	}
}

// The cache-off guard reads the vhost the agent itself renders: a cache-on vhost
// must read as on, a cache-off one as off, or warming silently stops (or runs
// for every domain) after a template change.
func TestNginxVhostCacheOn_MatchesTheRenderedVhost(t *testing.T) {
	for _, on := range []bool{true, false} {
		f := filepath.Join(t.TempDir(), "example.com.conf")
		if err := os.WriteFile(f, []byte(renderVhostForCacheTest(t, on)), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := nginxVhostCacheOn(f); got != on {
			t.Errorf("rendered vhost with cache_enabled=%v: nginxVhostCacheOn = %v", on, got)
		}
	}
	if nginxVhostCacheOn(filepath.Join(t.TempDir(), "missing.conf")) {
		t.Error("a missing vhost must read as cache off")
	}
}

// The ownership check still gates everything: a request from a user who does
// not own the vhost docroot neither purges nor warms.
func TestCoalesceAndPurge_NoWarmForANonOwner(t *testing.T) {
	host, warms := spoolWarmFixture(t, nil)
	purged := false
	wpPurgeRunPurge = func(context.Context, json.RawMessage) (any, error) { purged = true; return nil, nil }
	coalesceAndPurge(context.Background(), []*wpPurgeItem{spoolItem(t, host, os.Getuid()+1, []string{"/"})}, quietLog())
	if purged || len(*warms) != 0 {
		t.Fatalf("purged=%v warms=%+v, want neither for a non-owner", purged, *warms)
	}
}

// startWarmAfterPurge runs at most one warm per host at a time, and a
// whole-domain warm at most once per cooldown per host.
func TestStartWarmAfterPurge_OnePerHostAndWholeDomainCooldown(t *testing.T) {
	origRun := wpWarmRun
	t.Cleanup(func() {
		wpWarmRun = origRun
		wpWarmMu.Lock()
		wpWarmInFlight = map[string]bool{}
		wpWarmLastWhole = map[string]time.Time{}
		wpWarmMu.Unlock()
	})
	release := make(chan struct{})
	done := make(chan map[string]any, 10)
	wpWarmRun = func(_ context.Context, params map[string]any) (any, error) {
		<-release
		done <- params
		return nil, nil
	}
	wait := func() map[string]any {
		select {
		case p := <-done:
			// Let the goroutine clear its in-flight mark.
			for i := 0; i < 100; i++ {
				wpWarmMu.Lock()
				busy := wpWarmInFlight["a.example"]
				wpWarmMu.Unlock()
				if !busy {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			return p
		case <-time.After(2 * time.Second):
			t.Fatal("warm never ran")
			return nil
		}
	}
	log := quietLog()
	ctx := context.Background()

	startWarmAfterPurge(ctx, "a.example", []string{"/"}, false, log)
	startWarmAfterPurge(ctx, "a.example", []string{"/other/"}, false, log) // skipped: one in flight
	close(release)
	if p := wait(); !reflect.DeepEqual(p["paths"], []string{"/"}) {
		t.Fatalf("first warm params = %+v, want paths [/]", p)
	}
	select {
	case p := <-done:
		t.Fatalf("a second warm ran while the first was in flight: %+v", p)
	case <-time.After(50 * time.Millisecond):
	}

	startWarmAfterPurge(ctx, "a.example", nil, true, log)
	if p := wait(); p["max_urls"] != cacheWarmupDefaultMax || p["paths"] != nil {
		t.Fatalf("whole-domain warm params = %+v, want max_urls=%d and no paths", p, cacheWarmupDefaultMax)
	}
	startWarmAfterPurge(ctx, "a.example", nil, true, log) // within the cooldown: skipped
	select {
	case p := <-done:
		t.Fatalf("a whole-domain warm ran inside the cooldown: %+v", p)
	case <-time.After(50 * time.Millisecond):
	}
	// A targeted warm is not held back by the whole-domain cooldown.
	startWarmAfterPurge(ctx, "a.example", []string{"/new-post/"}, false, log)
	if p := wait(); !reflect.DeepEqual(p["paths"], []string{"/new-post/"}) {
		t.Fatalf("targeted warm params = %+v, want paths [/new-post/]", p)
	}
}
