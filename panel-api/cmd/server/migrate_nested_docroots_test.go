package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// cPanel puts an addon domain's docroot inside the main one
// (public_html/<addon>). A live pull copies each site to its own docroot, so
// the main site's copy must leave the nested ones out: it carried every addon
// a second time, a stale copy served under the main domain's path.
func TestNestedDocrootExcludes(t *testing.T) {
	src := []string{
		"/home/u/public_html",
		"/home/u/public_html/a.example/",
		"/home/u/public_html/b.example",
		"/home/u/public_html/a.example/sub",
		"/home/u/other.example",
		"/home/u/public_html2",          // a sibling sharing the prefix, not nested
		"/home/u/public_html/b.example", // the same docroot twice is not nested in itself
	}
	want := [][]string{
		{"/a.example/", "/a.example/sub/", "/b.example/"},
		{"/sub/"},
		nil,
		nil,
		nil,
		nil,
		nil,
	}
	got := nestedDocrootExcludes(src)
	for i := range src {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("excludes of %s = %q, want %q", src[i], got[i], want[i])
		}
	}
}

func TestNestedDocrootExcludes_NoNesting(t *testing.T) {
	got := nestedDocrootExcludes([]string{"/home/u/domains/a.example/public_html", "/home/u/domains/b.example/public_html"})
	for i, ex := range got {
		if len(ex) != 0 {
			t.Errorf("row %d: excludes %q, want none", i, ex)
		}
	}
}

// The live-pull home rsync passes each row's nested excludes to the agent.
func TestLivePullRsyncPassesNestedExcludes(t *testing.T) {
	src, err := os.ReadFile("migrate_run_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	compute := strings.Index(body, "nested := nestedDocrootExcludes(srcPaths)")
	call := strings.Index(body, `restoreAgent.Call(ctx, "migration.rsync_remote_home", map[string]any{`)
	pass := strings.Index(body, `"excludes": nested[i],`)
	if compute < 0 || call < 0 || pass < 0 {
		t.Fatalf("missing wiring: compute=%d call=%d pass=%d", compute, call, pass)
	}
	if !(compute < call && call < pass) {
		t.Fatal("the nested excludes must be computed before the per-domain rsync and passed in its params")
	}
	if next := strings.Index(body[call+1:], "restoreAgent.Call("); next >= 0 && call+1+next < pass {
		t.Fatal(`"excludes": nested[i] must sit in the live-pull rsync call, not a later one`)
	}
}
