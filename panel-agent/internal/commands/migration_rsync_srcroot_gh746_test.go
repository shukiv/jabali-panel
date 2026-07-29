package commands

import (
	"path/filepath"
	"strings"
	"testing"
)

// GH #746: the home-rsync guard hardcoded /home/<src_account>, refusing every
// Plesk file rsync (Plesk docroots live under /var/www/vhosts). It now honors a
// caller-provided src_root. Tests the real resolveRsyncSrcRoot + the src-path
// containment the handler applies.
func srcPathAllowed(srcRoot, srcAccount, srcPath string) bool {
	root, errMsg := resolveRsyncSrcRoot(srcRoot, srcAccount)
	if errMsg != "" {
		return false
	}
	c := filepath.Clean(srcPath)
	return c == root || strings.HasPrefix(c, root+"/")
}

func TestRsyncSrcRootGuard_GH746(t *testing.T) {
	cases := []struct {
		name             string
		srcRoot, srcAcct string
		srcPath          string
		want             bool
	}{
		{"plesk vhost docroot allowed", "/var/www/vhosts", "demolab.test", "/var/www/vhosts/demolab.test/httpdocs", true},
		{"plesk subdomain site1 allowed", "/var/www/vhosts", "demolab.test", "/var/www/vhosts/demolab.test/site1", true},
		{"plesk path outside vhosts refused", "/var/www/vhosts", "demolab.test", "/etc/passwd", false},
		{"plesk src_root traversal rejected", "/var/www/../etc", "demolab.test", "/etc/passwd", false},
		{"legacy cpanel home allowed", "", "acct1", "/home/acct1/public_html", true},
		{"legacy cross-tenant refused", "", "acct1", "/home/victim/secret", false},
		{"legacy missing src_account refused", "", "", "/home/x", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := srcPathAllowed(tc.srcRoot, tc.srcAcct, tc.srcPath); got != tc.want {
				t.Errorf("srcPathAllowed(%q,%q,%q)=%v want %v", tc.srcRoot, tc.srcAcct, tc.srcPath, got, tc.want)
			}
		})
	}
}
