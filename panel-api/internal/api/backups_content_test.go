package api

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #454: the account-backup create must persist the chosen content on the job
// row so the backup list shows the real Type. An empty request means "full";
// everything else passes through. Before the fix the create left Content unset,
// so GORM applied the column default 'full' and every backup showed as
// "Account Backup" regardless of the files/database choice.
func TestNormalizeBackupContent(t *testing.T) {
	if got := normalizeBackupContent(""); got != models.BackupContentFull {
		t.Errorf("empty should normalize to %q, got %q", models.BackupContentFull, got)
	}
	for _, c := range []string{"full", "files", "database", "folders"} {
		if got := normalizeBackupContent(c); got != c {
			t.Errorf("%q should pass through unchanged, got %q", c, got)
		}
	}
}
