package filesafe

import "testing"

// GH #1184: the admin File Manager uses a root-scoped Scope with a hard
// deny-list. These tests pin the security boundary: root reaches the whole
// tree, but denied prefixes are blocked for every op, with correct word
// boundaries and no /xyz-matches-/x confusion.
func TestNewAdminScope_RootReachesFilesystem(t *testing.T) {
	s, err := NewAdminScope("uid", "admin", nil)
	if err != nil {
		t.Fatalf("NewAdminScope: %v", err)
	}
	for _, p := range []string{"/", "/etc/nginx/nginx.conf", "/var/log/syslog", "/tmp/x", "/home/u/f"} {
		if _, err := s.Clean(p); err != nil {
			t.Errorf("Clean(%q) under root scope should pass, got %v", p, err)
		}
	}
}

func TestNewAdminScope_DenyList(t *testing.T) {
	s, err := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes)
	if err != nil {
		t.Fatalf("NewAdminScope: %v", err)
	}
	denied := []string{
		"/etc/shadow",
		"/etc/ssh",
		"/etc/ssh/sshd_config",
		"/etc/jabali",
		"/etc/jabali/config.toml",
		"/etc/letsencrypt/live/example.com/privkey.pem",
		"/usr/local/bin/jabali-agent",
		"/run/jabali/agent.sock",
		"/root/.ssh/authorized_keys",
	}
	for _, p := range denied {
		if _, err := s.Clean(p); err == nil {
			t.Errorf("Clean(%q) must be denied, but passed", p)
		} else if ve, ok := err.(*ValidationError); !ok || ve.Code != ErrCodeDenied {
			t.Errorf("Clean(%q): want ErrCodeDenied, got %v", p, err)
		}
	}
}

func TestNewAdminScope_DenyWordBoundary(t *testing.T) {
	s, _ := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes)
	// Look-alikes that share a textual prefix but are NOT inside a denied dir.
	for _, p := range []string{"/etc/sshfoo", "/etc/jabali-extra", "/etc/shadowy"} {
		if _, err := s.Clean(p); err != nil {
			if ve, ok := err.(*ValidationError); ok && ve.Code == ErrCodeDenied {
				t.Errorf("Clean(%q) wrongly denied by word-boundary bug: %v", p, err)
			}
		}
	}
}

func TestTenantScope_DenyListEmpty_NoOp(t *testing.T) {
	// Backward-compat: an ordinary tenant scope has no deny-list and behaves
	// exactly as before (in-home passes, out-of-home fails as not_in_scope).
	s, err := NewScope("uid", "bob", []string{"/home/bob"})
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	if _, err := s.Clean("/home/bob/public_html/index.php"); err != nil {
		t.Errorf("in-home path should pass: %v", err)
	}
	if _, err := s.Clean("/etc/shadow"); err == nil {
		t.Error("out-of-home path should fail")
	} else if ve, _ := err.(*ValidationError); ve != nil && ve.Code == ErrCodeDenied {
		t.Error("tenant scope must reject via not_in_scope, never path_denied")
	}
}

// TestAdminScope_baseFor_RootTraversal pins the openat2 traversal helper for the
// root scope — the box-verify caught baseFor rejecting /etc/* with a "//" prefix
// bug even though verifyInScope passed (GH #1184).
func TestAdminScope_baseFor_RootTraversal(t *testing.T) {
	s, _ := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes)
	cases := map[string]string{
		"/etc/nginx/nginx.conf": "etc/nginx/nginx.conf",
		"/tmp/x":                "tmp/x",
		"/var/log/syslog":       "var/log/syslog",
	}
	for path, wantRel := range cases {
		base, rel, err := s.baseFor(path)
		if err != nil {
			t.Errorf("baseFor(%q) errored: %v", path, err)
			continue
		}
		if base != "/" || rel != wantRel {
			t.Errorf("baseFor(%q) = base %q rel %q; want base \"/\" rel %q", path, base, rel, wantRel)
		}
	}
	// Root itself resolves to ".".
	if base, rel, err := s.baseFor("/"); err != nil || base != "/" || rel != "." {
		t.Errorf(`baseFor("/") = %q,%q,%v; want "/",".",nil`, base, rel, err)
	}
}
