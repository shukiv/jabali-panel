package main

import "testing"

// TestIsHostnameLike guards the Plesk rsync source-path check: only a
// DNS-hostname-shaped domain may anchor a /var/www/vhosts/<Dom>/httpdocs
// source path. A path-traversal or shell-shaped "domain" must be rejected
// so a tampered manifest can't escape the vhost tree.
func TestIsHostnameLike(t *testing.T) {
	ok := []string{"example.com", "lychee-fruits.co.il", "a.b.c.example.net", "x-1.io"}
	bad := []string{
		"",             // empty
		"nodot",        // no dot
		"..",           // traversal
		"a/../b.com",   // slash + traversal
		"a/b.com",      // slash
		"UPPER.com",    // uppercase (Plesk domains are lowercased)
		"a;rm -rf.com", // shell metachar
		"a b.com",      // space
		"a..b.com",     // double dot
		"/etc/passwd",  // absolute path
	}
	for _, h := range ok {
		if !isHostnameLike(h) {
			t.Errorf("%q should be hostname-like", h)
		}
	}
	for _, h := range bad {
		if isHostnameLike(h) {
			t.Errorf("%q must be rejected", h)
		}
	}
}
