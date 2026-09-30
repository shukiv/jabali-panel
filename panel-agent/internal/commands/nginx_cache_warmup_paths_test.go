package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// stubWarmFetch records every fetched path and fails the test if the sitemap is
// read. Restores both hooks after the test.
func stubWarmFetch(t *testing.T, code int) *[]string {
	t.Helper()
	origFetch, origBody := warmupFetch, warmupFetchBody
	t.Cleanup(func() { warmupFetch = origFetch; warmupFetchBody = origBody })
	var mu sync.Mutex
	var fetched []string
	warmupFetch = func(_ context.Context, _ string, path string) int {
		mu.Lock()
		defer mu.Unlock()
		fetched = append(fetched, path)
		return code
	}
	warmupFetchBody = func(_ context.Context, _ string, entry string) string {
		t.Errorf("a paths warmup must not read the sitemap (fetched %s)", entry)
		return ""
	}
	return &fetched
}

func runWarmup(t *testing.T, params map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := nginxCacheWarmupHandler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	return res.(map[string]any)
}

// After a targeted purge the warmup fetches exactly the purged paths.
func TestWarmupPaths_WarmsExactlyThePurgedPaths(t *testing.T) {
	fetched := stubWarmFetch(t, 200)
	m := runWarmup(t, map[string]any{"host": "ex.com", "paths": []string{"/", "/hello-world/"}})
	if want := []string{"/", "/hello-world/"}; !reflect.DeepEqual(*fetched, want) {
		t.Fatalf("fetched %v, want %v", *fetched, want)
	}
	if m["warmed"] != 2 || m["attempted"] != 2 || m["skipped"] != 0 {
		t.Errorf("stats = %+v, want warmed=2 attempted=2 skipped=0", m)
	}
}

// Paths come from a tenant's WordPress via the purge spool, so anything that is
// not a plain same-host path is skipped, never fetched.
func TestWarmupPaths_SkipsInvalidPaths(t *testing.T) {
	fetched := stubWarmFetch(t, 200)
	m := runWarmup(t, map[string]any{"host": "ex.com", "paths": []string{
		"relative",
		"//evil.example/x",
		"https://evil.example/",
		"/has space",
		"/ctrl\nchar",
		"/ok/",
		"/ok/", // duplicate
		"",
	}})
	if want := []string{"/ok/"}; !reflect.DeepEqual(*fetched, want) {
		t.Fatalf("fetched %v, want %v", *fetched, want)
	}
	if m["skipped"] != 5 {
		t.Errorf("skipped = %v, want 5 (the invalid ones; the duplicate and the empty entry are dropped silently)", m["skipped"])
	}
}

func TestWarmupPaths_CappedAtTheHardMax(t *testing.T) {
	fetched := stubWarmFetch(t, 200)
	paths := make([]string, 0, cacheWarmupHardMax+10)
	for i := 0; i < cacheWarmupHardMax+10; i++ {
		paths = append(paths, fmt.Sprintf("/p%d/", i))
	}
	m := runWarmup(t, map[string]any{"host": "ex.com", "paths": paths})
	if len(*fetched) != cacheWarmupHardMax {
		t.Fatalf("fetched %d paths, want the hard max %d", len(*fetched), cacheWarmupHardMax)
	}
	if m["clamped_to"] != cacheWarmupHardMax {
		t.Errorf("clamped_to = %v, want %d", m["clamped_to"], cacheWarmupHardMax)
	}
}

// The breaker still applies to a paths warmup.
func TestWarmupPaths_CircuitBreaks(t *testing.T) {
	fetched := stubWarmFetch(t, 500)
	paths := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		paths = append(paths, fmt.Sprintf("/p%d/", i))
	}
	m := runWarmup(t, map[string]any{"host": "ex.com", "paths": paths})
	if m["circuit_broken"] != true {
		t.Fatalf("circuit_broken = %v, want true", m["circuit_broken"])
	}
	if len(*fetched) > cacheWarmupMaxConsecFail {
		t.Fatalf("fetched %d paths, the breaker should stop at %d", len(*fetched), cacheWarmupMaxConsecFail)
	}
}
