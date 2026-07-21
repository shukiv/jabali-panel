// Backup "content" is what a single account_backup run captures: the whole
// account, files only, databases only, or specific home folders (GH #294).
// It is a first-class domain value shared by the on-demand create path
// (internal/api) AND the scheduled fan-out (internal/backupscheduler), so it
// lives in models — the one package both import — to keep a single source of
// truth for the valid set and the list-trimming rule (GH #454).
package models

const (
	BackupContentFull     = "full"
	BackupContentFiles    = "files"
	BackupContentDatabase = "database"
	BackupContentFolders  = "folders"
)

// ValidBackupContent reports whether c is an accepted content selector. The
// empty string is accepted and means "full" (the pre-#294 default), so a bad
// value can't reach the agent.
func ValidBackupContent(c string) bool {
	switch c {
	case "", BackupContentFull, BackupContentFiles, BackupContentDatabase, BackupContentFolders:
		return true
	}
	return false
}

// NormalizeBackupContent maps the empty string to the explicit "full" default
// so persisted rows and wire params never carry an ambiguous "".
func NormalizeBackupContent(c string) string {
	if c == "" {
		return BackupContentFull
	}
	return c
}

// ApplyBackupContent trims the per-stage item lists to match the content
// selection, mirroring what the agent's stage gating expects: the agent skips
// the home stage only for content=database, but iterates whatever database and
// mailbox lists it is handed — so the CALLER must empty the lists the selected
// content excludes. "files"/"folders" -> home only (no db, no mail);
// "database" -> databases only (no mail; agent drops home); else (full) -> all.
// Returned slices are the inputs or nil; callers pass fresh slices.
func ApplyBackupContent(content string, dbs, mbs, pgDbs []string) ([]string, []string, []string) {
	switch content {
	case BackupContentFiles, BackupContentFolders:
		return nil, nil, nil
	case BackupContentDatabase:
		return dbs, nil, pgDbs
	default:
		return dbs, mbs, pgDbs
	}
}
