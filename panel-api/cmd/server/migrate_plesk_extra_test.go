package main

import (
	"strings"
	"testing"
)

// GH #429 follow-up: the vhost-extra rsync must exclude every per-domain
// docroot (relative to the vhost root) and the Plesk system dirs, and must
// never emit a traversal pattern for a docroot outside the vhost root.
func TestPleskExtraExcludes(t *testing.T) {
	got := pleskExtraExcludes("/var/www/vhosts/demolab.test", []string{
		"/var/www/vhosts/demolab.test/httpdocs",
		"/var/www/vhosts/demolab.test/site1",
		"/var/www/vhosts/OTHER.example/httpdocs", // outside the root — skipped
	})
	want := map[string]bool{"/httpdocs": true, "/site1": true}
	for _, sys := range pleskSystemExcludes {
		want[sys] = true
	}
	if len(got) != len(want) {
		t.Fatalf("excludes = %v, want exactly %d entries", got, len(want))
	}
	for _, e := range got {
		if !want[e] {
			t.Errorf("unexpected exclude %q", e)
		}
		if !strings.HasPrefix(e, "/") {
			t.Errorf("exclude %q must be anchored to the transfer root", e)
		}
		if strings.Contains(e, "..") {
			t.Errorf("exclude %q carries traversal", e)
		}
	}
}
