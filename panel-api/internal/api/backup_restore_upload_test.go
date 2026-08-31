package api

import (
	"strings"
	"testing"
)

// GH #1408: the reassembly path must be derived from the authenticated admin +
// upload_id so one admin can never point the restore at another's staged file,
// and it must always land under the uploads handoff dir.
func TestRestoreUploadPath_Isolation(t *testing.T) {
	a := restoreUploadPath("admin-A", "up-1")
	b := restoreUploadPath("admin-B", "up-1") // same upload_id, different admin
	c := restoreUploadPath("admin-A", "up-2") // same admin, different upload_id

	for _, p := range []string{a, b, c} {
		if !strings.HasPrefix(p, restoreUploadDir+"/") {
			t.Fatalf("path %q not under %q", p, restoreUploadDir)
		}
		if strings.Contains(p, "..") {
			t.Fatalf("path %q contains ..", p)
		}
	}
	if a == b {
		t.Fatal("same upload_id across different admins must map to DIFFERENT paths")
	}
	if a == c {
		t.Fatal("different upload_ids for one admin must map to different paths")
	}
	// Deterministic for resumed chunks of the same (admin, upload_id).
	if a != restoreUploadPath("admin-A", "up-1") {
		t.Fatal("path must be stable for the same admin+upload_id")
	}
}

func TestUploadIDValidation(t *testing.T) {
	good := []string{"abc12345", "a1b2-c3d4_E5", strings.Repeat("x", 128)}
	bad := []string{"short", "", "has space", "path/traversal", "..", strings.Repeat("x", 129), "semi;colon"}
	for _, g := range good {
		if !uploadIDRE.MatchString(g) {
			t.Fatalf("uploadID %q should be valid", g)
		}
	}
	for _, b := range bad {
		if uploadIDRE.MatchString(b) {
			t.Fatalf("uploadID %q should be rejected", b)
		}
	}
}

func TestRestoreTargetUsernameRE(t *testing.T) {
	for _, g := range []string{"alice", "a", "a_b-c", "user01"} {
		if !restoreTargetUsernameRE.MatchString(g) {
			t.Fatalf("username %q should be valid", g)
		}
	}
	for _, b := range []string{"", "Alice", "1abc", "a" + strings.Repeat("x", 40), "a/b", "root;rm"} {
		if restoreTargetUsernameRE.MatchString(b) {
			t.Fatalf("username %q should be rejected", b)
		}
	}
}
