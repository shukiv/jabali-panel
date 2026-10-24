package backupwrapperhelpers

import (
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2245: every SFTP destination gets the ssh timeouts. One with a typed
// SFTP block gets them inside sftp.command; one stored as a bare URL (as
// `jabali backup destination create` writes it) gets them through
// sftp.args. Other kinds get no options.
func TestResticOptionsFor_EverySFTPDestinationIsBounded(t *testing.T) {
	bare := &models.BackupDestination{Kind: models.BackupDestinationKindSFTP, URL: "sftp:u@h:/r"}
	if got := ResticOptionsFor(bare); len(got) != 1 || got[0] != internalbackup.SFTPArgsFlag() {
		t.Errorf("bare-URL sftp destination: got %q, want only sftp.args", got)
	}

	typed := &models.BackupDestination{Kind: models.BackupDestinationKindSFTP, URL: "sftp:u@h:/r"}
	typed.ExtraOptions = []byte(`{"sftp":{"host":"h","user":"u","path":"/r","auth":"key","key_path":"/k"}}`)
	got := ResticOptionsFor(typed)
	if len(got) != 1 || !strings.HasPrefix(got[0], "sftp.command=") || !strings.Contains(got[0], "ConnectTimeout=30") {
		t.Errorf("typed sftp destination: got %q, want an sftp.command with the timeouts", got)
	}

	local := &models.BackupDestination{Kind: models.BackupDestinationKindLocal, URL: "/var/lib/jabali-backups/repo"}
	if got := ResticOptionsFor(local); got != nil {
		t.Errorf("local destination: got %q, want no options", got)
	}
}
