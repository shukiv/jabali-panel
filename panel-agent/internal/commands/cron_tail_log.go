package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

	// Default lines to 50 if not provided
	lines := 50
	if p.Lines != nil {
		lines = *p.Lines
	}

	// Resolve user's UID
	u, err := user.Lookup(p.Username)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("user %s not found: %v", p.Username, err),
		}
	}

	uid, _ := strconv.Atoi(u.Uid)
	runtimeDir := fmt.Sprintf("/run/user/%d", uid)

	// Query journalctl for the service logs
	cmd := exec.CommandContext(ctx, "sudo", "-u", p.Username)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("XDG_RUNTIME_DIR=%s", runtimeDir),
	)
	cmd.Args = append(cmd.Args,
		"journalctl", "--user",
		"-u", fmt.Sprintf("jabali-cron-%s.service", p.JobID),
		"-n", fmt.Sprintf("%d", lines),
		// short-iso (not cat): the log is useless for "did my hourly job run
		// at :00" questions without timestamps (GH #856). The hostname/unit
		// prefix it adds is stripped per line below.
		"-o", "short-iso",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// If journalctl fails (e.g., no logs yet), return empty log or error
		// Most common: "No entries found" which is not an error condition
		return &cronTailLogResponse{
			Log: stripJournalPrefix(stdout.String()),
		}, nil
	}

	return &cronTailLogResponse{
		Log: strings.TrimSpace(stripJournalPrefix(stdout.String())),
	}, nil
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
