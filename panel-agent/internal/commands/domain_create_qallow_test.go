package commands

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

var updateGolden = flag.Bool("update-qallow-golden", false, "rewrite the query-allowlist golden fixture")

// canonicalCacheVhostData is a fixed, representative cache-enabled vhostData
// used for the byte-identical baseline. Keep it stable — the golden captures
// the render of THIS exact input.
func canonicalCacheVhostData() vhostData {
	return vhostData{
		Domain: "example.com", DocRoot: "/home/u/public_html/example.com",
		HasPHP: true, PHPVersion: "8.3", Username: "u", IsEnabled: true,
		FPMSocket:    "/run/php/jabali-u/fpm.sock",
		CacheEnabled: true, CacheKeyZone: "jabali_fcgi", CacheTTL: "600s",
		CacheGate: "/blog", CachePath: "/",
	}
}

func renderQAllowVhost(t *testing.T, vd vhostData) string {
	t.Helper()
	tmpl := template.Must(template.New("vhost").Parse(vhostTemplate))
	var b bytes.Buffer
	if err := tmpl.Execute(&b, vd); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestVhostRender_NoAllowlist_ByteIdentical is the #1 backward-compat gate: with
// an empty CacheQueryAllowlist the rendered vhost must be byte-identical to the
// pre-feature output. Capture the baseline with:
//
//	go test ./internal/commands/ -run NoAllowlist_ByteIdentical -update-qallow-golden
//
// (run BEFORE the template gains the allowlist directives; the fixture is the
// frozen pre-feature render). Then this test fails if any allowlist-empty render
// drifts.
func TestVhostRender_NoAllowlist_ByteIdentical(t *testing.T) {
	got := renderQAllowVhost(t, canonicalCacheVhostData()) // CacheQAllow* zero-valued = feature off
	golden := filepath.Join("testdata", "vhost_qallow_baseline.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote golden %s (%d bytes)", golden, len(got))
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update-qallow-golden first): %v", err)
	}
	if got != string(want) {
		t.Errorf("empty-allowlist render drifted from the pre-feature baseline.\n--- got ---\n%s", got)
	}
}

// TestVhostRender_Allowlist_Paged: allowlist ["paged"] renders the dirty-query
// gate + the canonical $arg_paged key line in the vhost, and drops the shared
// qs_kind=other bypass line.
func TestVhostRender_Allowlist_Paged(t *testing.T) {
	vd := canonicalCacheVhostData()
	vd.CacheQAllowNames = sanitizeCacheQueryAllowlist([]string{"paged"})
	vd.CacheQAllowRegex = cacheQueryAllowRegex(vd.CacheQAllowNames)
	out := renderQAllowVhost(t, vd)

	if !strings.Contains(out, `if ($query_string ~ "^(?:(?:paged)=[^&]+)(?:&(?:paged)=[^&]+)*$")`) {
		t.Error("missing dirty-query gate for allowlist paged")
	}
	if !strings.Contains(out, `if ($arg_paged) { set $jabali_qkey "${jabali_qkey}paged=$arg_paged&"; }`) {
		t.Error("missing canonical $arg_paged key line")
	}
	if !strings.Contains(out, `fastcgi_cache_key "$scheme$request_method$host$jabali_cache_path?$jabali_qkey"`) {
		t.Error("cache key must append the allowlisted-query suffix")
	}
	if strings.Contains(out, "if ($jabali_qs_kind = other) { set $jabali_skip 1; }") {
		t.Error("allowlist mode must REPLACE the shared qs_kind=other bypass line")
	}
	// Both cache blocks (root + subdir) must carry the gate — expect the dirty
	// test to appear at least twice (or the subdir block absent for this input;
	// this input renders only the root block, so >=1).
	if n := strings.Count(out, "set $jabali_qs_dirty 1;"); n < 1 {
		t.Errorf("dirty gate rendered %d times, want >=1", n)
	}
}

// TestVhostRender_Allowlist_SortedCanonical: names render in sorted order so the
// key is deterministic regardless of client param order.
func TestVhostRender_Allowlist_SortedCanonical(t *testing.T) {
	vd := canonicalCacheVhostData()
	vd.CacheQAllowNames = sanitizeCacheQueryAllowlist([]string{"s", "paged"})
	vd.CacheQAllowRegex = cacheQueryAllowRegex(vd.CacheQAllowNames)
	out := renderQAllowVhost(t, vd)
	ip := strings.Index(out, "paged=$arg_paged")
	is := strings.Index(out, "s=$arg_s")
	if ip < 0 || is < 0 {
		t.Fatalf("both key lines must render (paged=%d s=%d)", ip, is)
	}
	if ip > is {
		t.Error("key lines must be in sorted order: paged before s")
	}
	if !strings.Contains(out, "(?:paged|s)=[^&]+") {
		t.Error("gate regex alternation must be sorted: paged|s")
	}
}
