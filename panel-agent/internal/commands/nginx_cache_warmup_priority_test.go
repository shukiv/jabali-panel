package commands

import (
	"reflect"
	"testing"
)

// JAB-95 Phase 2: high-value pages warm before low-value archives; within a
// tier the sitemap priority hint then lastmod break ties.
func TestPrioritizeWarmPaths(t *testing.T) {
	entries := []sitemapEntry{
		{path: "/tag/sale", priority: 0.5},
		{path: "/about", priority: 0.5},
		{path: "/product/widget", priority: 0.5},
		{path: "/blog", priority: 0.9, lastmod: "2026-07-01"},
		{path: "/blog-old", priority: 0.9, lastmod: "2026-01-01"},
		{path: "/product-category/tools", priority: 0.5},
	}
	got := prioritizeWarmPaths(entries)
	want := []string{
		"/product/widget",         // tier 3
		"/product-category/tools", // tier 3
		"/blog",                   // tier 2, prio 0.9, newer lastmod
		"/blog-old",               // tier 2, prio 0.9, older lastmod
		"/about",                  // tier 2, prio 0.5
		"/tag/sale",               // tier 1
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order=%v\nwant %v", got, want)
	}
}

func TestWarmValueTier(t *testing.T) {
	cases := map[string]int{
		"/":                    4,
		"/shop":                3,
		"/product/x":           3,
		"/product-category/y":  3,
		"/about":               2,
		"/2025/06/hello":       1,
		"/tag/foo":             1,
		"/author/jane":         1,
		"/page/3":              1,
	}
	for path, want := range cases {
		if got := warmValueTier(path); got != want {
			t.Errorf("warmValueTier(%q)=%d, want %d", path, got, want)
		}
	}
}

// parseUrlset pulls loc + optional priority/lastmod, defaults priority 0.5, and
// drops off-host URLs.
func TestParseUrlset(t *testing.T) {
	body := `<urlset>
	  <url><loc>https://ex.com/a</loc><priority>0.8</priority><lastmod>2026-07-01</lastmod></url>
	  <url><loc>https://ex.com/b</loc></url>
	  <url><loc>https://other.com/x</loc></url>
	</urlset>`
	got := parseUrlset("ex.com", body)
	if len(got) != 2 {
		t.Fatalf("expected 2 same-host entries, got %d: %+v", len(got), got)
	}
	if got[0].path != "/a" || got[0].priority != 0.8 || got[0].lastmod != "2026-07-01" {
		t.Errorf("entry a wrong: %+v", got[0])
	}
	if got[1].path != "/b" || got[1].priority != 0.5 {
		t.Errorf("entry b should default priority 0.5: %+v", got[1])
	}
}
