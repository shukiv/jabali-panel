package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// GH #738: installing a custom cert must de-track a certbot lineage first, or
// the orphaned renewal/<domain>.conf makes `certbot renew` abort box-wide.
func TestCleanupCertbotLineage_RemovesOrphanRenewalConf_GH738(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "renewal"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(root, "renewal", "example.com.conf")
	if err := os.WriteFile(conf, []byte("# certbot lineage\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// certbot delete (if certbot exists) can't find this name under the temp
	// --config-dir and fails, so the direct-remove floor must clear the conf.
	cleanupCertbotLineage(context.Background(), root, "example.com")

	if _, err := os.Stat(conf); !os.IsNotExist(err) {
		t.Fatalf("expected orphan renewal conf removed, stat err=%v", err)
	}
}

func TestCleanupCertbotLineage_NoConfIsNoop_GH738(t *testing.T) {
	root := t.TempDir()
	// No renewal/ dir and no lineage: must be a clean no-op, never panic.
	cleanupCertbotLineage(context.Background(), root, "never-managed.example")
}
