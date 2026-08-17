// bandwidth.scan_day — runs goaccess against /var/log/nginx/<domain>-access.log.1
// (yesterday's rotated nginx log; logrotate's delaycompress keeps it
// uncompressed) and emits per-domain {bytes_total, requests_total}.
//
// Why .log.1 specifically: rotation runs daily ~00:00 UTC, so a
// scheduled run at ~00:30 UTC sees the prior-day file fully written.
// Scanning the live access.log instead would either double-count
// today (running totals + tomorrow's rerun) or miss the last hour of
// activity (if scanned before midnight).
//
// goaccess output: `-o json` yields a structured object
// whose `general.bandwidth` (total bytes) and `general.total_requests`
// (incl. failed) are what we need. We don't need the full hosts/urls
// breakdown for this M13.1 scope; agent returns just the totals so the
// panel can build per-user / per-month aggregations from cheap rows.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type bandwidthScanDayParams struct {
	// LogDir overrides the default /var/log/nginx for tests. Empty =
	// production default.
	LogDir string `json:"log_dir,omitempty"`
}

type bandwidthDomainStats struct {
	Domain        string `json:"domain"`
	BytesTotal    uint64 `json:"bytes_total"`
	RequestsTotal uint64 `json:"requests_total"`
	// Day is the date the traffic occurred (YYYY-MM-DD UTC). Computed
	// as `now-1d` since logrotate runs at 00:00 UTC and we scan .log.1.
	Day string `json:"day"`
}

type bandwidthScanDayResponse struct {
	Stats   []bandwidthDomainStats `json:"stats"`
	Skipped []string               `json:"skipped,omitempty"`
}

// goaccessGeneral mirrors the slice of goaccess `-o json`
// output we care about. goaccess ships a stable schema for this
// section across 1.5+; the rest of the JSON document is hosts/urls
// breakdowns we don't need.
type goaccessJSON struct {
	General struct {
		// Some goaccess versions emit numbers as strings inside the
		// JSON; tolerate both via json.Number.
		Bandwidth     json.Number `json:"bandwidth"`
		TotalRequests json.Number `json:"total_requests"`
	} `json:"general"`
}

func bandwidthScanDayHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p bandwidthScanDayParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
		}
	}
	logDir := p.LogDir
	if logDir == "" {
		logDir = "/var/log/nginx"
	}

	// Yesterday in UTC. logrotate's `daily` rotates on calendar day
	// transition; a scan at 00:30 UTC sees yesterday's full file.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	// Rotated logs are <domain>-access.log.1 thanks to delaycompress.
	pattern := filepath.Join(logDir, "*-access.log.1")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("glob %s: %v", pattern, err),
		}
	}

	stats := make([]bandwidthDomainStats, 0, len(matches))
	skipped := []string{}

	for _, file := range matches {
		base := filepath.Base(file)
		// "<domain>-access.log.1" → "<domain>"
		name := strings.TrimSuffix(base, "-access.log.1")
		if name == "" || name == base {
			skipped = append(skipped, base)
			continue
		}

		bytesTotal, reqsTotal, err := scanLogFile(ctx, file)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (err: %v)", base, err))
			continue
		}

		stats = append(stats, bandwidthDomainStats{
			Domain:        name,
			BytesTotal:    bytesTotal,
			RequestsTotal: reqsTotal,
			Day:           yesterday,
		})
	}

	return bandwidthScanDayResponse{Stats: stats, Skipped: skipped}, nil
}

// goaccessScanArgs builds the goaccess CLI args for a single access log.
// Extracted so the flag set is unit-testable without the binary present.
//
// goaccess has NO --output-format flag; JSON-to-stdout is `-o json` (`-o`
// takes a format keyword or a filename, e.g. `-o csv`, `-o out.json`).
// `--output-format=json` fatals on goaccess 1.9.x ("unknown option") and
// silently broke the M13.1 bandwidth scan on hosts shipping that version.
func goaccessScanArgs(path string) []string {
	return []string{
		path,
		"--log-format=COMBINED",
		"-o", "json",
		"--no-html-last-updated",
		"--ignore-crawlers",
	}
}

func scanLogFile(ctx context.Context, path string) (uint64, uint64, error) {
	// Bound the goaccess invocation to 90s to prevent a runaway scan
	// holding the agent socket open across timer ticks.
	scanCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := execCommandContext(scanCtx, "goaccess", goaccessScanArgs(path)...)
	out, err := cmd.Output()
	if err != nil {
		// goaccess exits non-zero on parse errors; emit the file basename
		// + truncated stderr so the operator can spot which log is bad.
		var msg string
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			s := string(ee.Stderr)
			if len(s) > 200 {
				s = s[:200]
			}
			msg = fmt.Sprintf("goaccess exit: %v stderr=%s", err, s)
		} else {
			msg = fmt.Sprintf("goaccess: %v", err)
		}
		return 0, 0, fmt.Errorf("%s", msg)
	}

	var doc goaccessJSON
	if err := json.Unmarshal(out, &doc); err != nil {
		return 0, 0, fmt.Errorf("parse goaccess json: %w", err)
	}
	bytesTotal, _ := doc.General.Bandwidth.Int64()
	reqsTotal, _ := doc.General.TotalRequests.Int64()
	if bytesTotal < 0 {
		bytesTotal = 0
	}
	if reqsTotal < 0 {
		reqsTotal = 0
	}
	return uint64(bytesTotal), uint64(reqsTotal), nil
}

func init() {
	Default.Register("bandwidth.scan_day", bandwidthScanDayHandler)
}
