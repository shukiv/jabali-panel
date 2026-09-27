package dbops

import "testing"

func TestBackupAndRestoreDatabaseCommand(t *testing.T) {
	for _, tc := range []struct{ engine, backup, restore string }{
		{"postgres", "db.postgres.backup", "db.postgres.restore"},
		{"mariadb", "db.backup", "db.restore"},
		{"", "db.backup", "db.restore"},
	} {
		if got := BackupDatabaseCommand(tc.engine); got != tc.backup {
			t.Errorf("BackupDatabaseCommand(%q) = %s, want %s", tc.engine, got, tc.backup)
		}
		if got := RestoreDatabaseCommand(tc.engine); got != tc.restore {
			t.Errorf("RestoreDatabaseCommand(%q) = %s, want %s", tc.engine, got, tc.restore)
		}
	}
}
