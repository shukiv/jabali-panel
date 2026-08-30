package commands

import (
	"os/user"
	"path/filepath"
	"testing"
)

func TestDocrootUnderHome(t *testing.T) {
	const home = "/home/alice"
	tests := []struct {
		name    string
		docroot string
		wantErr bool
	}{
		{"depth-2 public_html/domain", "/home/alice/public_html/example.com", false},
		{"depth-3 domains/domain/public_html", "/home/alice/domains/example.com/public_html", false},
		{"home itself", "/home/alice", true},
		{"bare public_html", "/home/alice/public_html", true},
		{"bare domains", "/home/alice/domains", true},
		{"outside home", "/etc/passwd", true},
		{"root path", "/", true},
		{"prefix sibling", "/home/alicexx/public_html/x", true},
		{"unclean dotdot", "/home/alice/../bob/public_html/x", true},
		{"relative", "public_html/x", true},
		{"trailing dotdot escape", "/home/alice/domains/../../etc", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := docrootUnderHome(home, tt.docroot)
			if (err != nil) != tt.wantErr {
				t.Fatalf("docrootUnderHome(%q) err=%v, wantErr=%v", tt.docroot, err, tt.wantErr)
			}
		})
	}
}

func TestResolveTenantDocroot_Refusals(t *testing.T) {
	// Unknown user is refused before any path work.
	if _, _, err := resolveTenantDocroot("no_such_user_zzz_1382", "/home/x/public_html/y"); err == nil {
		t.Fatal("unknown user must be refused")
	}
	// root (uid 0) is refused outright even with an otherwise-valid path.
	if u, err := user.Lookup("root"); err == nil {
		if _, _, rerr := resolveTenantDocroot("root", filepath.Join(u.HomeDir, "a", "b")); rerr == nil {
			t.Fatal("root (uid 0) must be refused")
		}
	}
	// empty username refused.
	if _, _, err := resolveTenantDocroot("", "/home/x/public_html/y"); err == nil {
		t.Fatal("empty username must be refused")
	}
}
