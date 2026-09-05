package commands

import (
	"context"
	"encoding/json"
	"testing"
)

// GH #1408 slice 2: cleanup_stage is a root-privileged RemoveAll. It must refuse
// any path that isn't a fullrestore- stage directly under the uploads root — no
// traversal, no sibling dir, no nested path, no other-root path.
func TestSystemFullbackupCleanupStage_RefusesOutOfRoot(t *testing.T) {
	bad := []string{
		"/etc",
		"/",
		restoreUploadsRoot,                              // the root itself (no fullrestore- prefix)
		restoreUploadsRoot + "/notfullrestore",          // wrong prefix
		restoreUploadsRoot + "/../etc",                  // traversal (clean != input)
		restoreUploadsRoot + "/fullrestore-x/sub",       // nested (parent != root)
		"/tmp/fullrestore-x",                            // right prefix, wrong root
		restoreUploadsRoot + "/rup-abc.tar.zst",         // an upload, not a stage
		"",                                              // empty
	}
	for _, p := range bad {
		raw, _ := json.Marshal(map[string]string{"stage": p})
		if _, err := systemFullbackupCleanupStageHandler(context.Background(), raw); err == nil {
			t.Errorf("cleanup_stage must REFUSE %q", p)
		}
	}
}
