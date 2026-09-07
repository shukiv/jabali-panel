package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveDomainCertArtifacts covers the GH #1579 teardown SSL cleanup: the
// self-signed cert directory for the exact name is removed, and an absent name
// is a harmless no-op. (The LE lineage arm is cleanupCertbotLineage, which
// no-ops without a renewal conf and is covered by its own tests.)
func TestRemoveDomainCertArtifacts(t *testing.T) {
	tmp := t.TempDir()
	orig := baseSelfSignDir
	baseSelfSignDir = tmp
	defer func() { baseSelfSignDir = orig }()

	certDir := filepath.Join(tmp, "old.example.com")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "fullchain.pem"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	removeDomainCertArtifacts(context.Background(), "old.example.com")

	if _, err := os.Stat(certDir); !os.IsNotExist(err) {
		t.Fatalf("self-signed cert dir should be removed, stat err = %v", err)
	}

	// A name with no artifacts must not panic or error.
	removeDomainCertArtifacts(context.Background(), "never-existed.example.com")

	// A sibling domain's cert dir must survive (name-scoped removal).
	sib := filepath.Join(tmp, "keep.example.com")
	if err := os.MkdirAll(sib, 0o755); err != nil {
		t.Fatal(err)
	}
	removeDomainCertArtifacts(context.Background(), "old.example.com")
	if _, err := os.Stat(sib); err != nil {
		t.Fatalf("sibling cert dir must survive, stat err = %v", err)
	}
}
