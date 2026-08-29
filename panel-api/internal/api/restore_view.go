package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// restore_view — GH #1044. Restore jobs share the backup_jobs table + the
// backup/restore lists. Two small pure helpers make a restore row read as a
// restore rather than a backup: derive the job's Content (so the label reads
// "Database Restore", not the generic "Account Restore") and render the stored
// restore result as a log (so the Logs action shows what actually happened
// instead of "no log available" — a restore never writes the agent's backup
// log file).

// isRestoreKind reports whether a backup_jobs row is a restore (as opposed to a
// backup). Restores carry no restic snapshot and never write the agent's backup
// log file, so several handlers branch on this.
func isRestoreKind(kind string) bool {
	return kind == models.BackupJobKindAccountRestore || kind == models.BackupJobKindSystemRestore
}

// restoreContentFromSelective maps a selective restore's requested scope to the
// same Content vocabulary a backup uses, so the shared label logic can describe
// it. databases-only -> "database", home-only -> "files", anything mixed (or
// mailbox/DNS-only, which have no dedicated content bucket) -> "full".
func restoreContentFromSelective(databases, mailboxes, dnsDomains, domains []string, home bool) string {
	onlyDatabases := len(databases) > 0 && !home && len(mailboxes) == 0 && len(dnsDomains) == 0 && len(domains) == 0
	if onlyDatabases {
		return models.BackupContentDatabase
	}
	// Home and per-domain docroots (GH #1359) are both file restores.
	onlyFiles := (home || len(domains) > 0) && len(databases) == 0 && len(mailboxes) == 0 && len(dnsDomains) == 0
	if onlyFiles {
		return models.BackupContentFiles
	}
	return models.BackupContentFull
}

// restoreStage mirrors the agent's account-restore result stages, stored in a
// full restore job's ManifestJSON.
type restoreStage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// formatRestoreLog renders a restore job's stored result into a plain-text log
// body matching the backup.logs `log_text` shape, so the existing Logs modal
// shows it unchanged. Selective restores store {applied,skipped,warnings} in
// WarningsJSON; full restores store the agent's stages in ManifestJSON. When
// neither is present the restore is still running or was interrupted before it
// sealed — say that rather than borrow the backup-retention message.
func formatRestoreLog(job *models.BackupJob) string {
	if job == nil {
		return ""
	}
	if len(job.WarningsJSON) > 0 {
		var out selectiveRestoreOutcome
		if err := json.Unmarshal(job.WarningsJSON, &out); err == nil {
			if s := renderSelectiveOutcome(out); s != "" {
				return s
			}
		}
	}
	if len(job.ManifestJSON) > 0 {
		var res struct {
			Stages []restoreStage `json:"stages"`
		}
		if err := json.Unmarshal(job.ManifestJSON, &res); err == nil && len(res.Stages) > 0 {
			var b strings.Builder
			for _, s := range res.Stages {
				line := fmt.Sprintf("[%s] %s", strings.ToUpper(s.Status), s.Name)
				if s.Error != "" {
					line += ": " + s.Error
				}
				b.WriteString(line)
				b.WriteString("\n")
			}
			return strings.TrimRight(b.String(), "\n")
		}
	}
	if job.ErrorText != "" {
		return "restore failed: " + job.ErrorText
	}
	return "no recorded outcome yet — the restore may still be running or was interrupted before it finished."
}

// renderSelectiveOutcome turns the applied/skipped/warnings triple into labelled
// text lines. Returns "" when all three are empty so the caller can fall through.
func renderSelectiveOutcome(out selectiveRestoreOutcome) string {
	if len(out.Applied) == 0 && len(out.Skipped) == 0 && len(out.Warnings) == 0 {
		return ""
	}
	var b strings.Builder
	for _, a := range out.Applied {
		b.WriteString("[APPLIED] " + a + "\n")
	}
	for _, s := range out.Skipped {
		b.WriteString("[SKIPPED] " + s + "\n")
	}
	for _, w := range out.Warnings {
		b.WriteString("[WARNING] " + w + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
