package certbot

import (
	"os"
	"path/filepath"
	"testing"
)

// writeManagedRenewalConf marks live/<domain> as a real certbot-managed lineage
// (renewal/<domain>.conf present) so Issue()'s GH #738 stale-dir clear leaves it
// in place — the state any legit --expand / --keep-until-expiring flow is in.
func writeManagedRenewalConf(t *testing.T, leRoot, domain string) {
	t.Helper()
	dir := filepath.Join(leRoot, "renewal")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, domain+".conf"), []byte("# managed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// GH #738: a stale non-certbot-managed live dir (left by a custom-cert install)
// must be cleared before `certbot certonly` so it doesn't spawn a <domain>-0001
// duplicate — but a real managed lineage (has a renewal conf) must be kept.
func TestClearStaleLineageDir_GH738(t *testing.T) {
	mkLineage := func(root, domain string) {
		for _, sub := range []string{"live", "archive"} {
			if err := os.MkdirAll(filepath.Join(root, sub, domain), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("stale dir (no renewal conf) is removed", func(t *testing.T) {
		root := t.TempDir()
		mkLineage(root, "example.com")
		r := &Runner{LERoot: root}

		r.clearStaleLineageDir("example.com")

		for _, sub := range []string{"live", "archive"} {
			if _, err := os.Stat(filepath.Join(root, sub, "example.com")); !os.IsNotExist(err) {
				t.Errorf("expected %s/example.com removed, err=%v", sub, err)
			}
		}
	})

	t.Run("managed lineage (renewal conf present) is kept", func(t *testing.T) {
		root := t.TempDir()
		mkLineage(root, "example.com")
		if err := os.MkdirAll(filepath.Join(root, "renewal"), 0o755); err != nil {
			t.Fatal(err)
		}
		conf := filepath.Join(root, "renewal", "example.com.conf")
		if err := os.WriteFile(conf, []byte("# managed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		r := &Runner{LERoot: root}

		r.clearStaleLineageDir("example.com")

		if _, err := os.Stat(filepath.Join(root, "live", "example.com")); err != nil {
			t.Errorf("managed lineage live dir must be preserved, err=%v", err)
		}
	})
}
