// security_audit — M39 (ADR-0085) reads auditd events for the narrow
// rules installed by install_audit_exec(). Two commands:
//
//	security.audit.recent  — most recent N events tagged jabali_susp_exec / jabali_web_exec
//	security.audit.by_user — same, filtered to a single uid/auid
//
// We grep audit.log directly rather than shelling to ausearch; ausearch
// has a known parsing bug with ENRICHED log format and returns "<no matches>"
// even when events exist. Direct grep is also faster for the narrow key set.
//
// auditd's audit.log is the store; events are NOT mirrored into MariaDB.
// The narrow key filters keep read-time grep cheap.
package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	osexec "os/exec"
	"os/user"
	"strconv"
	"strings"
	"time"
)

const auditCallTimeout = 15 * time.Second
const auditLogPath = "/var/log/audit/audit.log"

// auditUnsetAuid is the kernel sentinel for "no login UID" — set on web
// workers (PHP-FPM), cron jobs, and any process that never went through
// a PAM login session.
const auditUnsetAuid = 4294967295

// auditEvent is the parsed shape returned to panel-api. Field set
// matches the panel-ui ExecAudit table columns.
type auditEvent struct {
	Timestamp string `json:"ts"`
	Auid      int    `json:"auid"`
	Username  string `json:"username,omitempty"`
	Comm      string `json:"comm,omitempty"`
	Exe       string `json:"exe,omitempty"`
	PPID      int    `json:"ppid,omitempty"`
	PID       int    `json:"pid,omitempty"`
}

type auditRecentRequest struct {
	Limit int `json:"limit,omitempty"`
}

type auditByUserRequest struct {
	Username string `json:"username"`
	Limit    int    `json:"limit,omitempty"`
	// Since: "recent" (last 10 min), "today", "yesterday", or RFC3339 timestamp.
	Since string `json:"since,omitempty"`
}

type auditEventsResponse struct {
	Events []auditEvent `json:"events"`
}

func mwAuditRecentHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req auditRecentRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, mwInvalidArg("malformed JSON body")
		}
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	events, err := runAuditLog(ctx, "", "recent", limit)
	if err != nil {
		return nil, err
	}
	return auditEventsResponse{Events: events}, nil
}

func mwAuditByUserHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req auditByUserRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, mwInvalidArg("malformed JSON body")
	}
	if !validAuditUsername(req.Username) {
		return nil, mwInvalidArg("invalid username")
	}
	u, err := user.Lookup(req.Username)
	if err != nil {
		return nil, mwInvalidArg(fmt.Sprintf("unknown user: %s", req.Username))
	}
	limit := req.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	since := req.Since
	if since == "" {
		since = "recent"
	}
	events, err := runAuditLog(ctx, u.Uid, since, limit)
	if err != nil {
		return nil, err
	}
	return auditEventsResponse{Events: events}, nil
}

// runAuditLog greps auditLogPath for all jabali audit keys and returns
// the most recent `limit` events. uidFilter (when non-empty) restricts
// results to a specific numeric UID (matched against both auid and uid
// fields so PHP-FPM web workers are included).
//
// This replaces the previous ausearch shell-out which returned "<no matches>"
// against ENRICHED-format audit logs despite events being present.
func runAuditLog(ctx context.Context, uidFilter, since string, limit int) ([]auditEvent, error) {
	ctx, cancel := context.WithTimeout(ctx, auditCallTimeout)
	defer cancel()

	cmd := execCommandContext(ctx, "grep", "-hE",
		`key="jabali_(susp|web)_exec|jabali_bin_tamper"`, auditLogPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, mwInternal("audit grep stdout pipe", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, mwInternal("audit grep start", err)
	}

	var sinceTime time.Time
	if since != "" && since != "all" {
		now := time.Now().UTC()
		switch since {
		case "recent":
			sinceTime = now.Add(-10 * time.Minute)
		case "today":
			sinceTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		case "yesterday":
			y := now.AddDate(0, 0, -1)
			sinceTime = time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, time.UTC)
		default:
			if t, err := time.Parse(time.RFC3339, since); err == nil {
				sinceTime = t.UTC()
			}
		}
	}

	events := parseAuditLog(stdout, uidFilter, sinceTime, limit)

	if err := cmd.Wait(); err != nil {
		// grep exits 1 when no lines match — not an error for us.
		if exitErr, ok := err.(*osexec.ExitError); ok && exitErr.ExitCode() == 1 {
			return events, nil
		}
		return nil, mwInternal("audit grep wait", err)
	}
	return events, nil
}

// parseAuditLog reads SYSCALL lines from r, applying time and UID filters,
// and returns up to `limit` events newest-first.
//
// Username resolution: prefer auid (login UID from PAM session) but fall back
// to uid (effective UID) when auid == auditUnsetAuid — this covers PHP-FPM
// and other web workers that never go through a PAM login.
func parseAuditLog(r io.Reader, uidFilter string, since time.Time, limit int) []auditEvent {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	auidCache := map[int]string{}
	uidCache := map[int]string{}
	var events []auditEvent

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, "type=SYSCALL") {
			continue
		}
		ev := parseSyscallLine(line)

		// Time filter.
		if !since.IsZero() {
			if t, err := time.Parse(time.RFC3339, ev.Timestamp); err != nil || t.Before(since) {
				continue
			}
		}

		// UID filter — match against either auid or uid field.
		procUID := parseIntField(line, " uid=") // note leading space to avoid ppid= match
		if uidFilter != "" {
			filterUID, err := strconv.Atoi(uidFilter)
			if err != nil {
				continue
			}
			matchesAuid := ev.Auid == filterUID
			matchesUID := procUID == filterUID
			if !matchesAuid && !matchesUID {
				continue
			}
		}

		// Username resolution.
		if ev.Auid > 0 && ev.Auid != auditUnsetAuid {
			if u, ok := auidCache[ev.Auid]; ok {
				ev.Username = u
			} else if uu, err := user.LookupId(strconv.Itoa(ev.Auid)); err == nil {
				ev.Username = uu.Username
				auidCache[ev.Auid] = uu.Username
			}
		} else if procUID > 0 {
			if u, ok := uidCache[procUID]; ok {
				ev.Username = u
			} else if uu, err := user.LookupId(strconv.Itoa(procUID)); err == nil {
				ev.Username = uu.Username
				uidCache[procUID] = uu.Username
			}
		}

		events = append(events, ev)
	}

	// audit.log is oldest-first; trim from the head if oversized.
	if len(events) > limit {
		events = events[len(events)-limit:]
	}
	// Reverse so newest is first.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	return events
}

func parseSyscallLine(line string) auditEvent {
	ev := auditEvent{}
	if idx := strings.Index(line, "msg=audit("); idx >= 0 {
		tail := line[idx+len("msg=audit("):]
		if end := strings.Index(tail, ":"); end >= 0 {
			tsField := tail[:end]
			if dot := strings.Index(tsField, "."); dot >= 0 {
				if secs, err := strconv.ParseInt(tsField[:dot], 10, 64); err == nil {
					ev.Timestamp = time.Unix(secs, 0).UTC().Format(time.RFC3339)
				}
			}
		}
	}
	ev.Auid = parseIntField(line, "auid=")
	ev.PID = parseIntField(line, "pid=")
	ev.PPID = parseIntField(line, "ppid=")
	ev.Comm = parseQuotedField(line, "comm=")
	ev.Exe = parseQuotedField(line, "exe=")
	return ev
}

func parseIntField(line, prefix string) int {
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return 0
	}
	tail := line[idx+len(prefix):]
	end := strings.IndexAny(tail, " \t\n")
	if end < 0 {
		end = len(tail)
	}
	v, _ := strconv.Atoi(tail[:end])
	return v
}

func parseQuotedField(line, prefix string) string {
	idx := strings.Index(line, prefix)
	if idx < 0 {
		return ""
	}
	tail := line[idx+len(prefix):]
	if len(tail) == 0 {
		return ""
	}
	if tail[0] == '"' {
		end := strings.IndexByte(tail[1:], '"')
		if end < 0 {
			return ""
		}
		return tail[1 : 1+end]
	}
	end := strings.IndexAny(tail, " \t\n")
	if end < 0 {
		end = len(tail)
	}
	return tail[:end]
}

func validAuditUsername(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

func init() {
	Default.Register("security.audit.recent", mwAuditRecentHandler)
	Default.Register("security.audit.by_user", mwAuditByUserHandler)
}
