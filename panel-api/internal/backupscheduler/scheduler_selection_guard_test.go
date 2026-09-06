package backupscheduler

import (
	"os"
	"strings"
	"testing"
)

// The scheduler was the third copy of the backup content-selection logic
// (JAB-324). It now selects through backupmetadata.SelectAll like the admin and
// tenant handlers, so a scheduled backup can no longer diverge on which content
// it picks or how it classifies a lookup failure. Guard by source.
func TestSchedulerSelection_NoResurrectedHelpers(t *testing.T) {
	src, err := os.ReadFile("scheduler.go")
	if err != nil {
		t.Fatalf("read scheduler.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "backupmetadata.SelectAll(") {
		t.Errorf("scheduler.go must resolve backup content through backupmetadata.SelectAll")
	}
	for _, banned := range []string{
		"func userDatabases(",
		"func userPostgresDatabases(",
		"func userDatabasesByEngine(",
		"func userDockerApps(",
		"func userMailboxes(",
	} {
		if strings.Contains(s, banned) {
			t.Errorf("a scheduler backup-selection helper was resurrected: %q — route through backupmetadata.SelectAll instead", banned)
		}
	}
}
