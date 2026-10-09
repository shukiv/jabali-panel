package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
)

// After a site's plugin is refreshed to a version that flushes without SCAN,
// the sweep re-applies the site's Redis ACL rule (which no longer grants it).
// Anything else leaves the ACL as it is: the version string is read from the
// site, so it may only ever tighten the rule, never decide to loosen it.
func TestRefreshResyncACL_OnlyAfterARefreshToAVersionThatNeedsNoScan(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0"})
	defer rdb.Close()
	cfg := api.ApplicationHandlerConfig{Redis: rdb, CacheTokenSecret: "s"}

	type call struct{ userID, osUser, installID string }
	var calls []call
	orig := resyncInstallACL
	t.Cleanup(func() { resyncInstallACL = orig })
	resyncInstallACL = func(_ context.Context, _ api.ApplicationHandlerConfig, userID, osUser, installID string) error {
		calls = append(calls, call{userID, osUser, installID})
		return nil
	}

	for _, tc := range []struct {
		res  cachePluginRefreshResult
		want bool
	}{
		{cachePluginRefreshResult{Refreshed: true, Version: "1.2.1"}, true},
		{cachePluginRefreshResult{Refreshed: true, Version: "1.2.0"}, true},
		{cachePluginRefreshResult{Refreshed: true, Version: "2.0.0"}, true},
		{cachePluginRefreshResult{Refreshed: true, Version: "1.1.0"}, false},
		{cachePluginRefreshResult{Refreshed: true, Version: ""}, false},
		{cachePluginRefreshResult{Refreshed: true, Version: "garbage"}, false},
		{cachePluginRefreshResult{Refreshed: false, Version: "1.2.1"}, false},
	} {
		calls = nil
		synced, err := refreshResyncACL(context.Background(), cfg, tc.res, "u1", "bob", "01AAAAAAAAAAAAAAAAAAAAAAAA")
		if err != nil {
			t.Fatalf("%+v: %v", tc.res, err)
		}
		if synced != tc.want || (len(calls) == 1) != tc.want {
			t.Errorf("%+v: synced=%v calls=%v, want re-sync %v", tc.res, synced, calls, tc.want)
		}
		if tc.want && calls[0] != (call{"u1", "bob", "01AAAAAAAAAAAAAAAAAAAAAAAA"}) {
			t.Errorf("%+v: re-synced %+v, want the refreshed install", tc.res, calls[0])
		}
	}

	// No Redis (an unprovisioned host): nothing to re-sync, not an error.
	calls = nil
	if synced, err := refreshResyncACL(context.Background(), api.ApplicationHandlerConfig{}, cachePluginRefreshResult{Refreshed: true, Version: "1.2.1"}, "u1", "bob", "x"); synced || err != nil || len(calls) != 0 {
		t.Errorf("without Redis: synced=%v err=%v calls=%v, want a quiet skip", synced, err, calls)
	}

	// A failed re-sync is reported.
	resyncInstallACL = func(context.Context, api.ApplicationHandlerConfig, string, string, string) error {
		return errors.New("boom")
	}
	if synced, err := refreshResyncACL(context.Background(), cfg, cachePluginRefreshResult{Refreshed: true, Version: "1.2.1"}, "u1", "bob", "x"); synced || err == nil {
		t.Errorf("failed re-sync: synced=%v err=%v, want the error", synced, err)
	}
}

// The sweep re-syncs each site right after that site's refresh, from the
// refresh's own result.
func TestRefreshCachePlugin_ResyncsTheACLAfterEachRefresh(t *testing.T) {
	src := stripLineComments(readGoSource(t, "app_cache_refresh_cmd.go"))
	refresh := strings.Index(src, `"wordpress.cache_plugin_refresh"`)
	resync := strings.Index(src, `refreshResyncACL(ctx, cacheCfg, res, in.UserID, *u.Username, in.ID)`)
	if refresh < 0 || resync < 0 {
		t.Fatalf("refresh call at %d, re-sync call at %d: both must be in the sweep", refresh, resync)
	}
	if resync < refresh {
		t.Fatal("the ACL must be re-synced after the site's plugin is refreshed, not before")
	}
	if !strings.Contains(src, "cacheCfg, _ := buildAppDeps()") {
		t.Fatal("the sweep must build the Redis + cache token deps the re-sync needs")
	}
}
