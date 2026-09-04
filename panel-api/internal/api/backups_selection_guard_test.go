package api

import (
	"os"
	"strings"
	"testing"
)

// After JAB-324 the account-backup content selection lives in exactly one
// place — backupmetadata.SelectAll — so the admin and tenant handlers can no
// longer drift on how a lookup failure is handled (the tenant copy used to
// swallow the error silently and drop the category from the backup). This
// source guard fails CI if a per-adapter copy is reintroduced on either config.
func TestBackupSelection_NoResurrectedHelpers(t *testing.T) {
	src, err := os.ReadFile("backups.go")
	if err != nil {
		t.Fatalf("read backups.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "backupmetadata.SelectAll(") {
		t.Errorf("backups.go must resolve backup content through backupmetadata.SelectAll")
	}
	for _, banned := range []string{
		"func (cfg BackupHandlerConfig) allUserDatabases",
		"func (cfg BackupHandlerConfig) allUserDatabasesByEngine",
		"func (cfg BackupHandlerConfig) allUserMailboxes",
		"func (cfg BackupHandlerConfig) allUserDockerApps",
		"func (cfg MeBackupsHandlerConfig) allUserDatabases",
		"func (cfg MeBackupsHandlerConfig) allUserMailboxes",
		"func (cfg MeBackupsHandlerConfig) allUserDockerApps",
		"func serverLevelDockerSlugs",
	} {
		if strings.Contains(s, banned) {
			t.Errorf("a per-adapter backup-selection helper was resurrected: %q — route through backupmetadata.SelectAll instead", banned)
		}
	}
}
