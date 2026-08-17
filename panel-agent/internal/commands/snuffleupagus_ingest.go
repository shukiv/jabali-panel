// Package commands — snuffleupagus_ingest.go (M41 Wave D, ADR-0088)
//
// snuffleupagus.fetch_incidents — pull-mode journalctl reader called by
// the panel-api event source goroutine every minute. Returns Snuffleupagus
// log lines emitted since `since` (RFC3339 timestamp string).
//
// Snuffleupagus default log target is syslog with identifier "snuffleupagus";
// each line shape is roughly:
//   [snuffleupagus][<ip>][<feature>][<action>] <details>
// where feature ∈ {disabled_function, eval, ini_protection, …} and action
// ∈ {dropped, simulated_dropped, logged}. We extract feature+action best-
// effort and pass the raw line through; panel-api stores raw verbatim so
// log-shape drift in upstream Snuffleupagus doesn't lose data.
package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type snuffleupagusFetchParams struct {
	Since string `json:"since"` // RFC3339; empty == last 1m
	Limit int    `json:"limit"` // 0 → 500
}

type snuffleupagusIncidentDTO struct {
	Ts         time.Time `json:"ts"`
	RuleName   string    `json:"rule_name"`
	Action     string    `json:"action"` // block | simulated_block | log
	SourceIP   string    `json:"source_ip,omitempty"`
	RequestURI string    `json:"request_uri,omitempty"`
	PhpVersion string    `json:"php_version,omitempty"`
	Raw        string    `json:"raw"`
}

type snuffleupagusFetchResponse struct {
	Incidents []snuffleupagusIncidentDTO `json:"incidents"`
	NextSince time.Time                  `json:"next_since"`
}

// journalctl line shape (with --output=json):
//   {"__REALTIME_TIMESTAMP":"1714650000000000",
//    "MESSAGE":"[snuffleupagus][1.2.3.4][disabled_function][dropped] ..."}
var (
	// quotedTargetRe extracts the first single-quoted token from a
	// Snuffleupagus message body. The body shape varies by feature:
	//   "Aborted execution on call of the function 'system' in ..."
	//   "Tried to set 'disable_functions' to ..."
	//   "The file '/foo' is not in the wrappers whitelist ..."
	// in every case the first 'X' is the actionable target.
	quotedTargetRe = regexp.MustCompile(`'([^']+)'`)
	// requestURIRe extracts the script path from the trailing
	// "in <path> on line <n>" segment. Snuffleupagus emits the absolute
	// filesystem path (NOT the HTTP URI), so we strip the standard
	// /home/<user>/domains/<domain>/public_html docroot prefix below
	// to surface the URI-shaped portion in the UI.
	requestURIRe = regexp.MustCompile(` in (/[^ ]+) on line `)
	docrootRe    = regexp.MustCompile(`^/home/[^/]+/domains/[^/]+/public_html`)
)

var snufLineRe = regexp.MustCompile(
	`\[snuffleupagus\](?:\[([^\]]+)\])?(?:\[([^\]]+)\])?(?:\[([^\]]+)\])?\s*(.*)`,
)

func snuffleupagusFetchHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p snuffleupagusFetchParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("malformed params: %v", err),
			}
		}
	}
	if p.Limit <= 0 || p.Limit > 5000 {
		p.Limit = 500
	}

	since := p.Since
	if since == "" {
		since = time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339)
	}

	// `journalctl --identifier snuffleupagus --since <ts> --output json`.
	// Snuffleupagus log_media defaults to syslog identifier "snuffleupagus".
	cmd := execCommandContext(ctx,
		"journalctl",
		"--identifier", "snuffleupagus",
		"--since", since,
		"--output", "json",
		"--no-pager",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("journalctl pipe: %v", err)}
	}
	if err := cmd.Start(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("journalctl start: %v", err)}
	}

	resp := snuffleupagusFetchResponse{
		Incidents: []snuffleupagusIncidentDTO{},
		NextSince: time.Now().UTC(),
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // long URIs
	for scanner.Scan() {
		if len(resp.Incidents) >= p.Limit {
			break
		}
		var entry struct {
			RealtimeUS string `json:"__REALTIME_TIMESTAMP"`
			Message    string `json:"MESSAGE"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		inc, ok := parseSnuffleupagusLine(entry.RealtimeUS, entry.Message)
		if !ok {
			continue
		}
		resp.Incidents = append(resp.Incidents, inc)
	}
	_ = cmd.Wait()
	if len(resp.Incidents) > 0 {
		// next_since = last incident's ts so the panel can dedupe.
		resp.NextSince = resp.Incidents[len(resp.Incidents)-1].Ts.Add(time.Microsecond)
	}
	return resp, nil
}

func parseSnuffleupagusLine(realtimeUS, msg string) (snuffleupagusIncidentDTO, bool) {
	if !strings.Contains(msg, "[snuffleupagus]") {
		return snuffleupagusIncidentDTO{}, false
	}
	m := snufLineRe.FindStringSubmatch(msg)
	if m == nil {
		return snuffleupagusIncidentDTO{}, false
	}
	// m[1] = ip, m[2] = feature, m[3] = action, m[4] = details.
	ip := m[1]
	feature := m[2]
	action := m[3]
	details := m[4]

	// Map Snuffleupagus action verb → our enum.
	var act string
	switch strings.ToLower(action) {
	case "dropped", "drop":
		act = "block"
	case "simulated_drop", "simulated_dropped", "simulation":
		act = "simulated_block"
	default:
		act = "log"
	}

	// Rule name extraction: the actionable signal lives in the first
	// quoted token of details (the function being blocked, the ini key
	// being set, the file being written). Fall back to the feature name
	// only when no quoted target is present.
	rule := "snuffleupagus:unknown"
	if feature != "" {
		rule = "sp." + feature
		if m := quotedTargetRe.FindStringSubmatch(details); m != nil {
			rule = "sp." + feature + ":" + m[1]
		}
	}

	// Extract request URI when Snuffleupagus emitted one. v0.13 logs
	// 'in REQUEST_URI: /path' for non-CLI invocations.
	uri := ""
	if m := requestURIRe.FindStringSubmatch(msg); m != nil {
		uri = m[1]
		// Strip docroot prefix to leave the URL-relative script path.
		uri = docrootRe.ReplaceAllString(uri, "")
		// CLI invocations log "Command line code" -- not a path. Drop.
		if !strings.HasPrefix(uri, "/") {
			uri = ""
		}
	}

	ts := parseRealtime(realtimeUS)
	return snuffleupagusIncidentDTO{
		Ts:         ts,
		RuleName:   rule,
		Action:     act,
		SourceIP:   ip,
		RequestURI: uri,
		Raw:        msg,
	}, true
}

func parseRealtime(realtimeUS string) time.Time {
	if realtimeUS == "" {
		return time.Now().UTC()
	}
	// __REALTIME_TIMESTAMP is microseconds since epoch as a string.
	var us int64
	if _, err := fmt.Sscanf(realtimeUS, "%d", &us); err != nil {
		return time.Now().UTC()
	}
	return time.Unix(us/1_000_000, (us%1_000_000)*1000).UTC()
}

func init() {
	Default.Register("snuffleupagus.fetch_incidents", snuffleupagusFetchHandler)
}
