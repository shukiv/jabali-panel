package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Only an entry whose username is a valid account name maps to an inner
// archive; any other name is skipped before a path is built from it, so none
// can name a file outside stage/users.
func TestContainerInnerArchives_UsesOnlyValidAccountNames(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, fullRestoreStagePrefix+"x")
	for _, f := range []string{
		filepath.Join(stage, "users", "alice.tar.zst"),
		filepath.Join(stage, "system.tar.zst"),
		filepath.Join(root, "outside.tar.zst"),           // what "../../outside" would name
		filepath.Join(stage, "users", "Mallory.tar.zst"), // not a valid account name
	} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var man fullContainerManifest
	if err := json.Unmarshal([]byte(`{"run_id":"r1","entries":[
		{"label":"system"},
		{"label":"user","username":"alice"},
		{"label":"user","username":"../../outside"},
		{"label":"user","username":"Mallory"},
		{"label":"user","username":""},
		{"label":"user","username":"bob"}
	]}`), &man); err != nil {
		t.Fatal(err)
	}

	users, system := containerInnerArchives(stage, man)
	if system != filepath.Join(stage, "system.tar.zst") {
		t.Fatalf("system inner = %q", system)
	}
	if len(users) != 1 || users[0].Username != "alice" || users[0].InnerPath != filepath.Join(stage, "users", "alice.tar.zst") {
		t.Fatalf("users = %+v, want only alice", users)
	}
}
