package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #1408: a cross-server bundle's rows must reattach to THIS box's target user.
func TestRemapMetadataUserID(t *testing.T) {
	in := json.RawMessage(`{"user":{"id":"SRC","username":"alice","email":"a@x"},"domains":[{"id":"d1"}]}`)
	out, err := remapMetadataUserID(in, "TARGET")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	u := m["user"].(map[string]any)
	if u["id"] != "TARGET" {
		t.Fatalf("user.id = %v, want TARGET", u["id"])
	}
	if u["username"] != "alice" {
		t.Fatalf("username must be preserved, got %v", u["username"])
	}
	if len(m["domains"].([]any)) != 1 {
		t.Fatal("child rows must be preserved")
	}
}

// The detached restore seals its outcome marker and drops the consumed tar, on
// both success and agent failure.
func TestRunUploadRestore_Outcome(t *testing.T) {
	newHandler := func(callErr bool) (*backupHandler, string, string) {
		dir := t.TempDir()
		tar := filepath.Join(dir, "up.tar.zst")
		if err := os.WriteFile(tar, []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
		outcome := filepath.Join(dir, "o.json")
		mock := &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
			if cmd != "backup.restore_from_tar" {
				t.Fatalf("unexpected agent command %q", cmd)
			}
			if callErr {
				return nil, context.DeadlineExceeded
			}
			return json.RawMessage(`{"applied":["home → /home/alice"],"warnings":["mail staged"]}`), nil
		}}
		return &backupHandler{cfg: BackupHandlerConfig{Agent: mock}}, tar, outcome
	}

	t.Run("success", func(t *testing.T) {
		h, tar, outcome := newHandler(false)
		h.runUploadRestore(uploadRestoreArgs{path: tar, outcomePath: outcome, username: "alice", targetID: "T", components: []string{"home"}})
		o, err := readRestoreUploadOutcome(outcome)
		if err != nil || o.Status != "done" {
			t.Fatalf("outcome = %+v err=%v, want done", o, err)
		}
		if len(o.Applied) != 1 {
			t.Fatalf("applied = %v, want 1", o.Applied)
		}
		if _, err := os.Stat(tar); !os.IsNotExist(err) {
			t.Fatal("consumed tar must be deleted on success")
		}
	})

	t.Run("agent failure", func(t *testing.T) {
		h, tar, outcome := newHandler(true)
		h.runUploadRestore(uploadRestoreArgs{path: tar, outcomePath: outcome, username: "alice", targetID: "T"})
		o, err := readRestoreUploadOutcome(outcome)
		if err != nil || o.Status != "failed" {
			t.Fatalf("outcome = %+v err=%v, want failed", o, err)
		}
		if _, err := os.Stat(tar); !os.IsNotExist(err) {
			t.Fatal("tar must be deleted on terminal failure too")
		}
	})
}

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
