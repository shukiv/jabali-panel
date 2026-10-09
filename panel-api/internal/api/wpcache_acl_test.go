package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// captureACL records every ACL command in full (stubGCRedis keeps only the
// user and password of a SETUSER).
func captureACL(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := wpCacheACL
	t.Cleanup(func() { wpCacheACL = orig })
	wpCacheACL = func(_ context.Context, _ *redis.Client, args ...any) error {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = fmt.Sprint(a)
		}
		calls = append(calls, parts)
		return nil
	}
	return &calls
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if strings.EqualFold(a, want) {
			return true
		}
	}
	return false
}

// The per-install rule is the plugin's command set. Since jabali-cache 1.2.0
// a flush bumps a counter and the panel does the cleanup, so SCAN is not part
// of it (ADR-0173).
func TestProvisionInstallACL_RuleHasNoScan(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	calls := captureACL(t)
	h := &wordPressHandler{cfg: ApplicationHandlerConfig{Redis: rdb}}

	if err := h.provisionInstallACL(context.Background(), "bob", gcA, "tok"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 {
		t.Fatalf("acl calls = %v, want SETUSER then SAVE", *calls)
	}
	set := (*calls)[0]
	if set[0] != "SETUSER" || set[1] != installACLUser("bob", gcA) || set[2] != "reset" || !hasArg(set, ">tok") || !hasArg(set, installACLKeyPattern("bob", gcA)) {
		t.Errorf("SETUSER = %v, want an absolute rule for the install's user, token and key fence", set)
	}
	for _, c := range []string{"+GET", "+SET", "+UNLINK", "+INCRBY", "+MGET"} {
		if !hasArg(set, c) {
			t.Errorf("rule lacks %s, which the plugin issues", c)
		}
	}
	for _, c := range []string{"+SCAN", "+KEYS", "+RANDOMKEY", "+DBSIZE", "+FLUSHDB", "+FLUSHALL", "+@all", "+@keyspace"} {
		if hasArg(set, c) {
			t.Errorf("rule grants %s", c)
		}
	}
	if strings.Join((*calls)[1], " ") != "SAVE" {
		t.Errorf("second call = %v, want SAVE", (*calls)[1])
	}
}

func TestCachePluginFlushesWithoutScan(t *testing.T) {
	for v, want := range map[string]bool{
		"1.2.0": true, "1.2.1": true, "1.10.0": true, "2.0.0": true, " 1.2.1\n": true, "1.2": true,
		"1.1.0": false, "1.1.9": false, "0.9.0": false, "": false, "garbage": false, "1": false,
		"1.x": false, "v1.2.0": false, "-1.5.0": false,
	} {
		if got := CachePluginFlushesWithoutScan(v); got != want {
			t.Errorf("CachePluginFlushesWithoutScan(%q) = %v, want %v", v, got, want)
		}
	}
}

func resyncCfg(rdb *redis.Client) ApplicationHandlerConfig {
	return ApplicationHandlerConfig{Redis: rdb, CacheTokenSecret: gcSecret, CacheTokenSalts: gcSalts{}}
}

// A re-sync re-applies the same rule with the site's own token, so the site
// keeps authenticating.
func TestResyncInstallACL_ReappliesTheRuleWithTheSitesToken(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	token := cacheInstallToken(gcSecret, "bob", gcA, "salt-u1")
	stubGCRedis(t, mr, map[string]string{installACLUser("bob", gcA): token})
	calls := captureACL(t)

	if err := ResyncInstallACL(context.Background(), resyncCfg(rdb), "u1", "bob", gcA); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0][0] != "SETUSER" || !hasArg((*calls)[0], ">"+token) || strings.Join((*calls)[1], " ") != "SAVE" {
		t.Fatalf("acl calls = %v, want SETUSER with the site's token, then SAVE", *calls)
	}
	if hasArg((*calls)[0], "+SCAN") {
		t.Error("the re-synced rule grants SCAN")
	}
}

// The rule resets the user's passwords. When the token the panel derives no
// longer authenticates, a re-sync would cut the site off, so it leaves the
// user alone.
func TestResyncInstallACL_LeavesTheUserAloneWhenTheTokenDiffers(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	stubGCRedis(t, mr, map[string]string{installACLUser("bob", gcA): "something-else"})
	calls := captureACL(t)

	if err := ResyncInstallACL(context.Background(), resyncCfg(rdb), "u1", "bob", gcA); err == nil {
		t.Error("want an error for a site whose token doesn't authenticate")
	}
	if len(*calls) != 0 {
		t.Errorf("acl calls = %v, want none", *calls)
	}
}

func TestResyncInstallACL_NeedsRedisAndTheSecret(t *testing.T) {
	mr, rdb := newMiniRedis(t)
	defer mr.Close()
	calls := captureACL(t)
	for name, cfg := range map[string]ApplicationHandlerConfig{
		"no redis":  {CacheTokenSecret: gcSecret},
		"no secret": {Redis: rdb},
	} {
		if err := ResyncInstallACL(context.Background(), cfg, "u1", "bob", gcA); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("acl calls = %v, want none", *calls)
	}
}
