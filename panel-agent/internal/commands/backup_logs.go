// backup.logs — return journalctl tail for a backup transient unit.
// Caller passes {job_id, kind}; we pick the right unit name and shell
// out to journalctl. ULID regex on job_id defends against shell metas
// even though we exec via os/exec (no shell).
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

var backupLogsJobIDRE = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

type backupLogsParams struct {
	JobID string `json:"job_id"`
	Kind  string `json:"kind"` // account_backup | system_backup | account_restore | system_restore
	// Lines is an optional tail size cap. Default 5000.
	Lines int `json:"lines,omitempty"`
}

type backupLogsResponse struct {
	Unit      string `json:"unit"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	LogText   string `json:"log_text"`
	FetchedAt string `json:"fetched_at"`
}

func unitForBackupKind(kind, jobID string) (string, bool) {
	switch kind {
	case "account_backup", "account_restore":
		return fmt.Sprintf("jabali-backup-%s.service", jobID), true
	case "system_backup", "system_restore":
		return fmt.Sprintf("jabali-system-backup-%s.service", jobID), true
	default:
		return "", false
	}
}

func backupLogsHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupLogsParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !backupLogsJobIDRE.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be a 26-char ULID"}
	}
	unit, ok := unitForBackupKind(p.Kind, p.JobID)
	if !ok {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "kind must be account_backup|account_restore|system_backup|system_restore"}
	}
	lines := p.Lines
	if lines <= 0 || lines > 20000 {
		lines = 5000
	}

	statusOut, _ := execCommandContext(ctx, "systemctl", "is-active", unit).Output()
	status := strings.TrimSpace(string(statusOut))

	// Primary log source: orchestrator-written file at
	// /var/lib/jabali-backups/logs/<job_id>.log. The orchestrator runs
	// in-process (no transient unit) so journalctl on a fictional
	// jabali-backup-<id>.service unit returns nothing — the file is
	// the only place stage events land.
	var logText string
	if data, err := os.ReadFile(backup.JobLogPath(p.JobID)); err == nil {
		logText = string(data)
	} else {
		// Fallback: tail the panel-agent journal for messages whose
		// printf went via fmt.Println("backup orchestrator failed:" …).
		// Better than (no log output) when the orchestrator died
		// before opening the file.
		journalArgs := []string{"-u", unit, "--no-pager", "-o", "cat", "-n", strconv.Itoa(lines)}
		out, _ := execCommandContext(ctx, "journalctl", journalArgs...).Output()
		logText = string(out)
	}

	if strings.TrimSpace(logText) == "" {
		// JAB-98: the per-job log file was pruned by backup retention (or
		// the orchestrator died before writing it) and the journal fallback
		// is empty. Return a clear message instead of a blank modal.
		logText = "(no log available \u2014 it may have expired by retention or the job predates per-job logging)"
	}

	resp := backupLogsResponse{
		Unit:      unit,
		Status:    status,
		LogText:   logText,
		FetchedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if status == "inactive" || status == "failed" {
		showOut, err := execCommandContext(ctx, "systemctl", "show", unit,
			"--property=ExecMainStatus", "--value").Output()
		if err == nil {
			if v, perr := strconv.Atoi(strings.TrimSpace(string(showOut))); perr == nil {
				resp.ExitCode = &v
			}
		}
	}
	return resp, nil
}

func init() {
	Default.Register("backup.logs", backupLogsHandler)
}
