package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// JAB-337: the CLI upload staging budget must be per-owner, not a single global
// pool — otherwise one tenant's abandoned CLI temps consume the allowance for
// every other tenant. This proves cliStagingDirBytesForUser counts only the
// querying owner's files.
func TestCLIStagingDirBytesForUser_IsolatesOwners(t *testing.T) {
	dir := t.TempDir()
	old := cliUploadStagingDir
	cliUploadStagingDir = dir
	defer func() { cliUploadStagingDir = old }()

	write := func(name string, n int) {
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, n), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write(cliUploadTmpPrefix("userA")+"aaa", 100)
	write(cliUploadTmpPrefix("userA")+"bbb", 50)
	write(cliUploadTmpPrefix("userB")+"ccc", 999)

	if got := cliStagingDirBytesForUser("userA"); got != 150 {
		t.Errorf("userA staging = %d, want 150 (must NOT include userB's 999-byte temp)", got)
	}
	if got := cliStagingDirBytesForUser("userB"); got != 999 {
		t.Errorf("userB staging = %d, want 999", got)
	}
}

// Owner-namespaced prefixes must be distinct and non-overlapping so one owner's
// temps never match another's budget query.
func TestCLIUploadTmpPrefix_PerOwner(t *testing.T) {
	a, b := cliUploadTmpPrefix("userA"), cliUploadTmpPrefix("userB")
	if a == b {
		t.Fatal("per-owner prefixes must differ")
	}
	if !strings.HasPrefix("jabali-upload-cli-userA-xyz", a) {
		t.Errorf("a userA temp must match userA's prefix, got prefix %q", a)
	}
	if strings.HasPrefix("jabali-upload-cli-userA-xyz", b) {
		t.Error("a userA temp must NOT match userB's prefix — budgets would leak across owners")
	}
}

// Source-pin the upload guard wiring: configured max (not hardcoded) + per-owner
// budget, and the global-budget helper is gone.
func TestCLIUpload_UsesConfiguredMaxAndPerOwnerBudget(t *testing.T) {
	src, err := os.ReadFile("files_cmd.go")
	if err != nil {
		t.Fatalf("read files_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "cliResolveMaxUploadBytes(c.Context())") {
		t.Fatal("upload must enforce the admin-configured max (JAB-337), not a hardcoded 100 MiB cap")
	}
	if !strings.Contains(s, "cliStagingDirBytesForUser(u.ID)") {
		t.Fatal("staging budget must be per-owner (JAB-337)")
	}
	if strings.Contains(s, "cliStagingDirBytes()") {
		t.Fatal("the global (cross-owner) staging budget helper must be gone (JAB-337)")
	}
}
