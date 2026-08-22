package filesafe

import (
	"os"
	"path/filepath"
	"testing"
)

// GH #1184 + JAB-358: the admin File Manager uses a root-scoped Scope so it can
// READ (browse) the whole filesystem, minus a hard deny-list, but every
// MUTATION is confined to a closed write allow-list of safe data roots via
// WriteScope. These tests pin both halves of that boundary.

func TestNewAdminScope_RootReachesFilesystem(t *testing.T) {
	s, err := NewAdminScope("uid", "admin", nil, DefaultAdminMutableRoots)
	if err != nil {
		t.Fatalf("NewAdminScope: %v", err)
	}
	// Reads range across the whole tree (the FM browses "/").
	for _, p := range []string{"/", "/etc/nginx/nginx.conf", "/var/log/syslog", "/tmp/x", "/home/u/f"} {
		if _, err := s.Clean(p); err != nil {
			t.Errorf("Clean(%q) under root scope should pass, got %v", p, err)
		}
	}
}

func TestNewAdminScope_DenyList(t *testing.T) {
	s, err := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes, DefaultAdminMutableRoots)
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
		// JAB-358 additions: control-plane secret + state stores.
		"/etc/jabali-panel/bulwark.env",
		"/var/lib/jabali-panel/anything",
		"/run/jabali-panel/api.sock",
		"/run/jabali-bulwark/bulwark.sock",
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
	s, _ := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes, DefaultAdminMutableRoots)
	// Look-alikes that share a textual prefix but are NOT inside a denied dir.
	for _, p := range []string{"/etc/sshfoo", "/etc/jabali-extra", "/etc/shadowy"} {
		if _, err := s.Clean(p); err != nil {
			if ve, ok := err.(*ValidationError); ok && ve.Code == ErrCodeDenied {
				t.Errorf("Clean(%q) wrongly denied by word-boundary bug: %v", p, err)
			}
		}
	}
}

// TestNewAdminScope_RequiresMutableRoots: the constructor refuses to build a
// footgun scope — an empty write allow-list, or "/" as a mutable root, would
// re-open whole-filesystem writes.
func TestNewAdminScope_RequiresMutableRoots(t *testing.T) {
	if _, err := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes, nil); err == nil {
		t.Error("NewAdminScope with no mutable roots must error")
	}
	if _, err := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes, []string{"/"}); err == nil {
		t.Error(`NewAdminScope with "/" as a mutable root must error`)
	}
	if _, err := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes, []string{"relative"}); err == nil {
		t.Error("NewAdminScope with a relative mutable root must error")
	}
}

// TestAdminScope_WriteScope_ReadOnlyOutsideAllowList is the core JAB-358
// boundary at the string layer: the same non-deny-listed system path the admin
// FM can READ cannot be WRITTEN (WriteScope rejects it as read-only), while a
// path inside a safe data root is writable.
func TestAdminScope_WriteScope_ReadOnlyOutsideAllowList(t *testing.T) {
	s, _ := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes, DefaultAdminMutableRoots)
	w := s.WriteScope()

	readOnly := []string{
		"/etc/cron.d/evil",
		"/etc/systemd/system/pwn.service",
		"/etc/passwd",
		"/etc/pam.d/sshd",
		"/etc/ld.so.conf.d/evil.conf",
		"/usr/local/bin/anything",
		"/bin/anything",
		"/boot/grub/grub.cfg",
		"/lib/systemd/system/x.service",
		"/root/.bashrc",
		"/opt/app/bin/x",
		"/var/spool/cron/crontabs/root",
		// word-boundary lookalikes of the mutable roots
		"/homework",
		"/var/wwwfoo",
	}
	for _, p := range readOnly {
		// The admin can still READ these via the broad scope.
		if _, rerr := s.Clean(p); rerr != nil {
			t.Logf("note: Clean(%q) on read scope = %v", p, rerr)
		}
		// But the WRITE scope refuses them as read-only.
		if _, werr := w.Clean(p); werr == nil {
			t.Errorf("WriteScope.Clean(%q) must be read-only, but passed", p)
		} else if ve, ok := werr.(*ValidationError); !ok || ve.Code != ErrCodeReadOnly {
			t.Errorf("WriteScope.Clean(%q): want ErrCodeReadOnly, got %v", p, werr)
		}
	}

	writable := []string{
		"/home",
		"/home/alice/public_html/index.php",
		"/var/www/site/wp-config.php",
		"/srv/app/data.json",
		"/tmp/scratch",
		"/var/tmp/scratch",
	}
	for _, p := range writable {
		if _, err := w.Clean(p); err != nil {
			t.Errorf("WriteScope.Clean(%q) inside allow-list should pass, got %v", p, err)
		}
	}
}

// TestAdminScope_WriteScope_baseForReadOnly pins the same rejection at the
// openat2 entry point (baseFor), the layer every escape-proof mutation uses.
func TestAdminScope_WriteScope_baseForReadOnly(t *testing.T) {
	w := mustAdmin(t).WriteScope()
	if _, _, err := w.baseFor("/etc/cron.d/x"); err == nil {
		t.Error("baseFor(/etc/cron.d/x) on write scope must be read-only")
	} else if ve, ok := err.(*ValidationError); !ok || ve.Code != ErrCodeReadOnly {
		t.Errorf("baseFor write scope: want ErrCodeReadOnly, got %v", err)
	}
	// Inside a safe root, baseFor picks the safe root as the openat2 base.
	base, rel, err := w.baseFor("/home/alice/x")
	if err != nil || base != "/home" || rel != "alice/x" {
		t.Errorf(`baseFor("/home/alice/x") = %q,%q,%v; want "/home","alice/x",nil`, base, rel, err)
	}
}

// TestAdminScope_WriteScope_SymlinkEscapeBlocked is the security proof that a
// logical prefix check ALONE cannot give: a relative symlink planted INSIDE a
// safe root whose target climbs out of it (../secret) resolves — logically — to
// a path still under the safe root, so w.Clean() PASSES it. The actual write,
// performed via the escape-proof openat2 op with the safe root as base, is
// refused by the kernel (RESOLVE_BENEATH), and nothing is created outside the
// safe root. This is exactly the TOCTOU-proof confinement WriteScope buys.
func TestAdminScope_WriteScope_SymlinkEscapeBlocked(t *testing.T) {
	parent := t.TempDir()
	safe := filepath.Join(parent, "safe")
	secret := filepath.Join(parent, "secret")
	for _, d := range []string{safe, secret} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// A RELATIVE symlink inside safe that climbs out into the sibling secret dir.
	if err := os.Symlink("../secret", filepath.Join(safe, "break")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	// An ABSOLUTE symlink inside safe pointing at secret.
	if err := os.Symlink(secret, filepath.Join(safe, "abs")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Admin scope whose write allow-list is exactly `safe`.
	s, err := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes, []string{safe})
	if err != nil {
		t.Fatalf("NewAdminScope: %v", err)
	}
	w := s.WriteScope()

	for _, name := range []string{"break", "abs"} {
		logical := filepath.Join(safe, name, "pwned")
		// The LOGICAL check passes — the cleaned path is textually under `safe`.
		if _, cerr := w.Clean(logical); cerr != nil {
			t.Fatalf("precondition: w.Clean(%q) should pass logically, got %v", logical, cerr)
		}
		// The real escape-proof write is refused by the kernel.
		if f, oerr := w.CreateExclInScope(logical, 0o644); oerr == nil {
			f.Close()
			t.Errorf("CreateExclInScope(%q) escaped the safe root via symlink %q", logical, name)
		}
		// Nothing was created in the sibling secret dir.
		if _, serr := os.Lstat(filepath.Join(secret, "pwned")); serr == nil {
			t.Errorf("write escaped: %s/pwned exists", secret)
		}
	}

	// A legitimate write straight into the safe root still works.
	f, err := w.CreateExclInScope(filepath.Join(safe, "ok.txt"), 0o644)
	if err != nil {
		t.Fatalf("CreateExclInScope inside safe root should succeed: %v", err)
	}
	f.Close()
}

// TestTenantScope_WriteScope_NoOp: a tenant scope has no MutableRoots, so
// WriteScope returns it unchanged — tenant file ops are byte-for-byte identical
// to before JAB-358. This is the backward-compat pin.
func TestTenantScope_WriteScope_NoOp(t *testing.T) {
	s, _ := NewScope("uid", "bob", []string{"/home/bob"})
	w := s.WriteScope()
	if w != s {
		t.Fatal("tenant WriteScope must return the same scope (no narrowing)")
	}
	if _, err := w.Clean("/home/bob/public_html/index.php"); err != nil {
		t.Errorf("in-home write should pass: %v", err)
	}
	if _, err := w.Clean("/etc/passwd"); err == nil {
		t.Error("out-of-home write should fail")
	} else if ve, ok := err.(*ValidationError); !ok || ve.Code != ErrCodeNotInScope {
		// A tenant scope is not writeNarrowed, so it rejects via not_in_scope,
		// never the admin-only read_only.
		t.Errorf("tenant out-of-home write: want ErrCodeNotInScope, got %v", err)
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
// READ (root) scope — the box-verify caught baseFor rejecting /etc/* with a "//"
// prefix bug even though verifyInScope passed (GH #1184).
func TestAdminScope_baseFor_RootTraversal(t *testing.T) {
	s := mustAdmin(t)
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

func mustAdmin(t *testing.T) *Scope {
	t.Helper()
	s, err := NewAdminScope("uid", "admin", DefaultAdminDeniedPrefixes, DefaultAdminMutableRoots)
	if err != nil {
		t.Fatalf("NewAdminScope: %v", err)
	}
	return s
}
