package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"regexp"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// cronTailLogParams is the input for cron.tail_log command.
type cronTailLogParams struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	JobID    string `json:"job_id"`
	Lines    *int   `json:"lines,omitempty"` // Defaults to 50 if not provided
	// RunAsRoot: an admin's root job (JAB-293). Its unit is a system one, so
	// its output is in the system journal, not a user's.
	RunAsRoot bool `json:"run_as_root,omitempty"`
}

// cronTailLogResponse is the output from cron.tail_log.
type cronTailLogResponse struct {
	Log string `json:"log"`
}

func cronTailLogHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cronTailLogParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate inputs
	if p.Username == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "username required",
		}
	}
	if p.JobID == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "job_id required",
		}
	}
	// The job id names the unit whose log is read; hold it to the ULID shape
	// cron.apply enforces rather than trusting the caller.
	if !ulidRE.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be a 26-char ULID"}
	}

	// Default lines to 50 if not provided
	lines := 50
	if p.Lines != nil {
		lines = *p.Lines
	}

	if p.RunAsRoot {
		return cronTailRootLog(ctx, p.JobID, lines)
	}

	// Resolve user's UID
	u, err := user.Lookup(p.Username)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("user %s not found: %v", p.Username, err),
		}
	}

	// Read as root, limited to the tenant's entries by the journal's trusted
	// _UID field (GH #2120). `journalctl --user` as the tenant saw only the
	// tenant's own journal file, which journald keeps only when the journal is
	// stored persistently: with a volatile journal the log was empty. The
	// job's own lines carry _SYSTEMD_USER_UNIT; the user manager's
	// "Starting…" / "Finished…" lines about it carry USER_UNIT.
	unit := fmt.Sprintf("jabali-cron-%s.service", p.JobID)
	owner := "_UID=" + u.Uid
	cmd := execCommandContext(ctx, "journalctl",
		owner, "_SYSTEMD_USER_UNIT="+unit, "+", owner, "USER_UNIT="+unit,
		"-n", strconv.Itoa(lines),
		// short-iso (not cat): the log is useless for "did my hourly job run
		// at :00" questions without timestamps (GH #856). The hostname/unit
		// prefix it adds is stripped per line below.
		"-o", "short-iso",
		"--no-pager",
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// No entries yet is not an error: return what there is.
	_ = cmd.Run()
	return &cronTailLogResponse{
		Log: strings.TrimSpace(stripJournalPrefix(stdout.String())),
	}, nil
}

// cronTailRootLog reads a root job's log from the system journal: the same
// short-iso lines, manager lines included, as a tenant job's log.
func cronTailRootLog(ctx context.Context, jobID string, lines int) (any, error) {
	// The job id names a unit in the system journal; hold it to the ULID
	// shape cron.apply enforces so no other unit's log can be asked for.
	if !ulidRE.MatchString(jobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be a 26-char ULID"}
	}
	scope := systemCronScope()
	cmd := scope.command(ctx, scope.journalctl,
		"-u", fmt.Sprintf("jabali-cron-%s.service", jobID),
		"-n", strconv.Itoa(lines),
		"-o", "short-iso",
		"--no-pager",
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	// No entries yet is not an error: return what there is.
	_ = cmd.Run()
	return &cronTailLogResponse{Log: strings.TrimSpace(stripJournalPrefix(stdout.String()))}, nil
}

// journalShortISORe matches one short-iso journal line:
// "2026-08-02T17:45:12+0300 host jabali-cron-x[123]: message". Capture the
// timestamp and the message; drop the hostname + unit[pid] noise — the drawer
// shows one job's log, so the unit adds nothing and steals width.
var journalShortISORe = regexp.MustCompile(`^(\S+) \S+ [^:\[]+\[\d+\]: (.*)$`)

// stripJournalPrefix rewrites "<ts> <host> <unit>[<pid>]: <msg>" lines to
// "<ts> <msg>". Non-matching lines (journalctl notices, "-- Boot --" markers)
// pass through untouched.
func stripJournalPrefix(log string) string {
	lines := strings.Split(log, "\n")
	for i, l := range lines {
		if m := journalShortISORe.FindStringSubmatch(l); m != nil {
			lines[i] = m[1] + " " + m[2]
		}
	}
	return strings.Join(lines, "\n")
}

func init() {
	Default.Register("cron.tail_log", cronTailLogHandler)
}
