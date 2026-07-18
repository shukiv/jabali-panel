package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// JAB-95 Phase 4: a site that keeps failing trips the circuit breaker instead
// of hammering the whole URL list.
func TestWarmupCircuitBreak(t *testing.T) {
	origFetch, origBody := warmupFetch, warmupFetchBody
	t.Cleanup(func() { warmupFetch = origFetch; warmupFetchBody = origBody })

	var sb strings.Builder
	sb.WriteString("<urlset>")
	for i := 0; i < 30; i++ {
		sb.WriteString(fmt.Sprintf("<url><loc>https://ex.com/p%d</loc></url>", i))
	}
	sb.WriteString("</urlset>")
	sitemap := sb.String()

	warmupFetchBody = func(_ context.Context, _ string, entry string) string {
		if strings.HasSuffix(entry, ".xml") {
			return sitemap
		}
		return ""
	}
	warmupFetch = func(_ context.Context, _ string, _ string) int { return 500 } // always fail

	params, _ := json.Marshal(map[string]any{"host": "ex.com", "max_urls": 50})
	res, err := nginxCacheWarmupHandler(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	m := res.(map[string]any)
	if m["circuit_broken"] != true {
		t.Fatalf("circuit_broken=%v, want true", m["circuit_broken"])
	}
	if att := m["attempted"].(int); att > cacheWarmupMaxConsecFail+2 {
		t.Fatalf("attempted=%d, breaker should stop near %d, not the whole list", att, cacheWarmupMaxConsecFail)
	}
	if !strings.Contains(m["first_error"].(string), "circuit-broken") {
		t.Errorf("first_error should note the break: %q", m["first_error"])
	}
}

// A healthy site warms without tripping the breaker.
func TestWarmupNoBreakOnSuccess(t *testing.T) {
	origFetch, origBody := warmupFetch, warmupFetchBody
	t.Cleanup(func() { warmupFetch = origFetch; warmupFetchBody = origBody })
	warmupFetchBody = func(_ context.Context, _ string, entry string) string {
		if strings.HasSuffix(entry, ".xml") {
			return "<urlset><url><loc>https://ex.com/a</loc></url><url><loc>https://ex.com/b</loc></url></urlset>"
		}
		return ""
	}
	warmupFetch = func(_ context.Context, _ string, _ string) int { return 200 }
	params, _ := json.Marshal(map[string]any{"host": "ex.com", "max_urls": 50})
	res, _ := nginxCacheWarmupHandler(context.Background(), params)
	m := res.(map[string]any)
	if m["circuit_broken"] != false {
		t.Fatalf("circuit_broken=%v, want false on all-success", m["circuit_broken"])
	}
	if m["warmed"].(int) != 3 { // homepage + /a + /b
		t.Errorf("warmed=%d, want 3", m["warmed"])
	}
}
