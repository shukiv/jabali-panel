// security_aide — M42 (ADR-0087) thin wrapper around AIDE. Two
// commands: status (read latest report + DB age) and check (manual
// trigger).
//
// AIDE's audit.log file IS the storage; we don't mirror events into
// MariaDB. Daily check via jabali-aide-check.timer; this surface is
// for the panel UI to peek between scheduled runs.

package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const aideCallTimeout = 15 * time.Second

type aideStatusResponse struct {
	Enabled         bool   `json:"enabled"`
	DBAgeSeconds    int64  `json:"db_age_seconds"`
	LastCheckTS     string `json:"last_check_ts,omitempty"`
	Summary         struct {
		Added   int `json:"added"`
		Changed int `json:"changed"`
		Removed int `json:"removed"`
	} `json:"summary"`
	Sample []aideSampleRow `json:"sample"`
	// Categories (GH #714) counts EVERY changed path by likely source, so the UI
	// can triage a large delta (package churn vs security-control tampering).
	Categories map[string]int `json:"categories,omitempty"`
	// Reason: human-readable when Enabled=false (e.g. "AIDE not
	// installed", "DB still building").
	Reason string `json:"reason,omitempty"`
	// Rebuild (GH #2079) is the baseline rebuild the panel started: running
	// now, or how the last one ended. Absent when none was started.
	Rebuild *aideRebuildStatus `json:"rebuild,omitempty"`
	// Check (GH #2079) is set while a check runs, or when the last one failed.
	Check *aideCheckStatus `json:"check,omitempty"`
	// BaselineRebuiltAt (GH #2079) is set instead of LastCheckTS when the
	// report log holds an aide --init: every aide run replaces the log, so
	// after a rebuild no check has run against the new baseline yet.
	BaselineRebuiltAt string `json:"baseline_rebuilt_at,omitempty"`
}

type aideSampleRow struct {
	Path       string `json:"path"`
	ChangeType string `json:"change_type"` // added|changed|removed
	Category   string `json:"category"`    // GH #714
	// Meta (GH #714): per-attribute old/new values from the report's "Detailed
	// information about changes" section, when present. Empty otherwise (defensive).
	Meta []aideAttrChange `json:"meta,omitempty"`
}

type aideAttrChange struct {
	Attr string `json:"attr"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// parseAideDetail extracts per-file old/new attribute values from the report's
// "Detailed information about changes:" section (AIDE emits it unless
// report_summarize_changes=yes, which jabali does not set). Defensive: returns
// an empty map if the section is absent or the format differs. Handles both the
// `old | new` (AIDE 0.18+) and `old , new` separators.
func parseAideDetail(text string) map[string][]aideAttrChange {
	out := map[string][]aideAttrChange{}
	idx := strings.Index(text, "Detailed information about changes:")
	if idx < 0 {
		return out
	}
	sc := bufio.NewScanner(strings.NewReader(text[idx:]))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	known := map[string]bool{
		"size": true, "sha256": true, "sha512": true, "perm": true, "perms": true,
		"mtime": true, "ctime": true, "uid": true, "gid": true, "inode": true,
		"linkname": true, "links": true, "linkcount": true, "b_count": true, "bcount": true,
	}
	// split returns the old|new (or old,new) halves of a value line, or false.
	split := func(val string) (string, string, bool) {
		if i := strings.Index(val, "|"); i >= 0 {
			return strings.TrimSpace(val[:i]), strings.TrimSpace(val[i+1:]), true
		}
		if i := strings.LastIndex(val, ","); i >= 0 {
			return strings.TrimSpace(val[:i]), strings.TrimSpace(val[i+1:]), true
		}
		return "", "", false
	}
	var cur string
	var lastIdx = -1 // index into out[cur] of the attr being (continued)
	for sc.Scan() {
		t := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(t, "File:") || strings.HasPrefix(t, "Directory:") || strings.HasPrefix(t, "Entry:") {
			cur = strings.TrimSpace(t[strings.Index(t, ":")+1:])
			lastIdx = -1
			continue
		}
		if cur == "" {
			continue
		}
		ci := strings.Index(t, ":")
		// An attr line is "<Attr> : old | new". A continuation line (a wrapped
		// long value, e.g. base64 SHA256) has NO ':' but still has the '|'/','
		// separator — append its halves to the attr we're mid-parsing.
		if ci < 0 {
			if lastIdx >= 0 && lastIdx < len(out[cur]) {
				if oc, nc, ok := split(t); ok {
					out[cur][lastIdx].Old += oc
					out[cur][lastIdx].New += nc
				}
			}
			continue
		}
		attr := strings.TrimSpace(t[:ci])
		if !known[strings.ToLower(attr)] {
			lastIdx = -1
			continue
		}
		oldV, newV, ok := split(strings.TrimSpace(t[ci+1:]))
		if !ok {
			lastIdx = -1
			continue
		}
		out[cur] = append(out[cur], aideAttrChange{Attr: attr, Old: oldV, New: newV})
		lastIdx = len(out[cur]) - 1
	}
	return out
}

// classifyAidePath buckets a changed path by likely owner/source so a big AIDE
// delta is triageable. "unknown" + "security-control" are the ones to scrutinize;
// "system-package" is expected churn after apt/kernel updates.
func classifyAidePath(path string) string {
	switch {
	case strings.HasPrefix(path, "/etc/apparmor.d/") ||
		strings.HasPrefix(path, "/etc/nftables") ||
		strings.HasPrefix(path, "/etc/audit/") ||
		strings.HasPrefix(path, "/etc/crowdsec/") ||
		strings.HasPrefix(path, "/etc/snuffleupagus") ||
		strings.HasPrefix(path, "/etc/aide"):
		return "security-control"
	case strings.HasPrefix(path, "/etc/jabali") ||
		strings.HasPrefix(path, "/opt/jabali") ||
		strings.HasPrefix(path, "/var/lib/jabali") ||
		strings.HasPrefix(path, "/usr/local/bin/jabali") ||
		strings.HasPrefix(path, "/usr/local/libexec/jabali"):
		return "jabali-managed"
	case strings.HasPrefix(path, "/etc/letsencrypt/") ||
		strings.HasPrefix(path, "/etc/ssl/"):
		return "certificate"
	case strings.HasPrefix(path, "/usr/") || strings.HasPrefix(path, "/lib") ||
		strings.HasPrefix(path, "/bin/") || strings.HasPrefix(path, "/sbin/") ||
		strings.HasPrefix(path, "/etc/apt/") || strings.HasPrefix(path, "/var/lib/dpkg") ||
		strings.HasPrefix(path, "/etc/alternatives/"):
		return "system-package"
	case strings.HasPrefix(path, "/var/log/") || strings.HasPrefix(path, "/var/cache/") ||
		strings.HasPrefix(path, "/var/lib/") || strings.HasPrefix(path, "/run/"):
		return "log-state"
	case strings.HasPrefix(path, "/home/"):
		return "user-content"
	default:
		return "unknown"
	}
}

const (
	aideDBPath     = "/var/lib/aide/aide.db"
	aideMarkerPath = "/var/lib/aide/.jabali-installed"
	aideReportPath = "/var/log/aide/aide.report.log"
)

func mwAideStatusHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	resp := aideStatusResponse{Sample: []aideSampleRow{}}
	resp.Rebuild = readAideRebuildStatus(ctx)
	resp.Check = readAideCheckStatus(ctx)

	if _, err := os.Stat("/usr/bin/aide"); err != nil {
		resp.Enabled = false
		resp.Reason = "AIDE binary not found"
		return resp, nil
	}

	if st, err := os.Stat(aideDBPath); err == nil {
		resp.Enabled = true
		resp.DBAgeSeconds = int64(time.Since(st.ModTime()).Seconds())
	} else {
		resp.Enabled = false
		if _, e := os.Stat("/var/lib/aide/.init-in-progress"); e == nil {
			resp.Reason = "AIDE DB still building (initial init in progress)"
		} else {
			resp.Reason = "AIDE DB missing — run 'aide --init'"
		}
		return resp, nil
	}

	// Parse the last report: the "AIDE found differences" header and the
	// added / changed / removed counters at its top, then every entry.
	if data, err := readLastAideReport(aideReportPath); err == nil {
		applyAideReport(data, &resp)
	}

	return resp, nil
}

// aideReportReadMax bounds how much of the last report the status reads.
const aideReportReadMax = 64 << 20

// readLastAideReport returns the last report in the report log, from its
// "Start timestamp:" line (or the whole log when it has none), up to
// aideReportReadMax.
//
// GH #678 read the last 4 MiB, on the belief that each check appends to the
// log; the newest report is then at the end. But aide opens report_url=file:
// with truncation, so the log holds one report (the daily unit appends aide's
// stdout copy after it), and its summary and entries are at the TOP. A report
// over 4 MiB (148k changes on .60 made 19.5 MB) then read as 0 changes: the
// page said no tamper was detected, and aide.tamper.detected never fired.
func readLastAideReport(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	var off, start int64
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "Start timestamp:") {
			start = off
		}
		off += int64(len(sc.Bytes())) + 1
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(f, aideReportReadMax))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// applyAideReport fills resp from the tail of the report log.
func applyAideReport(data string, resp *aideStatusResponse) {
	// parseAideReport captures the FIRST report block it sees, so isolate the
	// LAST one — each AIDE report starts with a "Start timestamp:" line.
	if idx := strings.LastIndex(data, "Start timestamp:"); idx >= 0 {
		if nl := strings.LastIndexByte(data[:idx], '\n'); nl >= 0 {
			idx = nl + 1
		}
		data = data[idx:]
	}
	parseAideReport(data, resp)
	// GH #2079: an --init summary ("AIDE successfully initialized database." in
	// 0.19, "AIDE initialized database at …" in 0.18) is not a check.
	if strings.Contains(data, "initialized database") {
		resp.BaselineRebuiltAt = resp.LastCheckTS
		resp.LastCheckTS = ""
		return
	}
	// GH #714: attach per-file old/new metadata from the detail section.
	detail := parseAideDetail(data)
	if len(detail) > 0 {
		for i := range resp.Sample {
			if m, ok := detail[resp.Sample[i].Path]; ok {
				resp.Sample[i].Meta = m
			}
		}
	}
}

// parseAideReport extracts summary counts + sample paths from an
// aide.report.log. AIDE 0.18 / 0.19 emit several plain-text report
// formats depending on report_url and report_summarize_changes.
// Variants seen in the wild include:
//
//   "Added entries:" section header, then lines like
//      f++++++++++++++++++++++ZZZZ : /etc/example
//      f++++++++++++++++++++++ZZZZ /etc/example
//   or just
//      added: /etc/example
//
//   "Changed entries:" section, lines like
//      f   ...mc.. .C..       : /etc/bar
//      f =....TS....C.....    /etc/bar
//
// Rather than coupling to a single layout, we classify each line by
// its leading token: a token of the form `^[fdlcbDpsSh][+\-=. ]{N}.*`
// (file/dir/link/etc with the attribute string) tells us what kind of
// change it is — `+++` = added, `---` = removed, anything else with
// `.`/`=`/`m`/`c`/`s` etc = changed. Section headers are still parsed
// when present; they're an additional signal, not the only one.
//
// Sample is capped at 50 rows.
func parseAideReport(text string, resp *aideStatusResponse) {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var lastTimestamp string
	var section string // "added" | "removed" | "changed" | ""
	var summarySeen bool

	classifyAttr := func(attr string) string {
		// Attribute string is the leading "f++++++++...ZZZZ" token
		// (or with `f .....mc..C..` etc). All-`+` after the type
		// char => added. All-`-` => removed. Anything else => changed.
		body := attr[1:] // drop leading f/d/l/...
		if body == "" {
			return "changed"
		}
		allPlus := true
		allMinus := true
		for _, r := range body {
			if r != '+' && r != 'Z' {
				allPlus = false
			}
			if r != '-' && r != 'Z' {
				allMinus = false
			}
		}
		if allPlus && !allMinus {
			return "added"
		}
		if allMinus && !allPlus {
			return "removed"
		}
		return "changed"
	}

	if resp.Categories == nil {
		resp.Categories = map[string]int{}
	}
	pushSample := func(kind, path string) {
		// Skip lines without an absolute path (defensive — AIDE always
		// emits absolute paths for system-file rules but the report
		// can also include rule-name aliases at the top).
		if !strings.HasPrefix(path, "/") {
			return
		}
		resp.Categories[classifyAidePath(path)]++
		if len(resp.Sample) < 300 {
			resp.Sample = append(resp.Sample, aideSampleRow{
				Path:       path,
				ChangeType: kind,
				Category:   classifyAidePath(path),
			})
		}
	}

	for scanner.Scan() {
		// GH #714: scan EVERY report row so Categories counts the full delta
		// (the Sample list itself is still capped inside pushSample).
		raw := scanner.Text()
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimLeft(line, " \t")

		// Start timestamp: "Start timestamp: 2026-04-30 04:30:42 +0000 …"
		if strings.HasPrefix(line, "Start timestamp:") {
			lastTimestamp = strings.TrimSpace(strings.TrimPrefix(line, "Start timestamp:"))
			continue
		}
		if strings.HasPrefix(trimmed, "Total number of entries:") {
			continue
		}

		// Summary counts. The first occurrence of each counter line
		// (in the Summary block) sets the count. Later occurrences of
		// the same prefix as a section header have NO numeric suffix
		// and won't match Sscanf — so we don't need to gate by section.
		if strings.HasPrefix(trimmed, "Added entries:") {
			var n int
			if _, err := fmt.Sscanf(trimmed, "Added entries:%d", &n); err == nil && !summarySeen {
				resp.Summary.Added = n
			} else {
				section = "added"
			}
			summarySeen = true
			continue
		}
		if strings.HasPrefix(trimmed, "Removed entries:") {
			var n int
			if _, err := fmt.Sscanf(trimmed, "Removed entries:%d", &n); err == nil && resp.Summary.Removed == 0 {
				resp.Summary.Removed = n
			} else {
				section = "removed"
			}
			continue
		}
		if strings.HasPrefix(trimmed, "Changed entries:") {
			var n int
			if _, err := fmt.Sscanf(trimmed, "Changed entries:%d", &n); err == nil && resp.Summary.Changed == 0 {
				resp.Summary.Changed = n
			} else {
				section = "changed"
			}
			continue
		}
		if trimmed == "Detailed information about changes:" {
			// Per-file detail block follows; don't try to mine paths
			// from it — the section list above gave us a clean sample.
			section = "detail"
			continue
		}

		// Skip horizontal rules + blanks.
		if trimmed == "" || strings.HasPrefix(trimmed, "---") {
			continue
		}
		// Skip per-file detail headers ("File: …").
		if strings.HasPrefix(trimmed, "File:") || strings.HasPrefix(trimmed, "Summary:") {
			continue
		}
		// Skip the detail block once we're inside it.
		if section == "detail" {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			continue
		}

		// Two common shapes for sample rows:
		//   1) "f++++++++++++ZZZZ : /etc/example"     (3 fields, ":" separator)
		//   2) "f++++++++++++ZZZZ /etc/example"       (2 fields)
		//   3) "added: /etc/example"                  (custom report_url format)
		//   4) "/etc/example"                         (rare, path-only)
		// Pick the path = last absolute-path-looking token.
		var path string
		for i := len(fields) - 1; i >= 0; i-- {
			if strings.HasPrefix(fields[i], "/") {
				path = fields[i]
				break
			}
		}
		if path == "" {
			continue
		}

		// Classify by leading attribute token (most reliable):
		first := fields[0]
		if len(first) >= 2 && strings.ContainsRune("fdlcbDpsSh", rune(first[0])) {
			pushSample(classifyAttr(first), path)
			continue
		}

		// Fallback: section context.
		switch section {
		case "added", "removed", "changed":
			pushSample(section, path)
			continue
		}

		// Last shot: literal "added:/removed:/changed:" prefixes from a
		// custom report_format. Section context already covers most of
		// this but explicit handling is cheap.
		switch {
		case strings.HasPrefix(trimmed, "added: "):
			pushSample("added", path)
		case strings.HasPrefix(trimmed, "removed: "):
			pushSample("removed", path)
		case strings.HasPrefix(trimmed, "changed: "):
			pushSample("changed", path)
		}
	}
	resp.LastCheckTS = lastTimestamp
	_ = section // section accumulates state across iterations; final value not used
}

// mwAideCheckHandler invokes `aide --check` synchronously. Times out
// at the agent's command timeout — operator should rely on the timer
// for full runs.
// mwAideReportHandler (GH #714) returns the raw AIDE report for full-diff
// export — the UI downloads it as a file. Capped at 8 MiB (a large delta can
// bloat the report); keeps the tail (newest block) when oversized.
func mwAideReportHandler(_ context.Context, _ json.RawMessage) (any, error) {
	const maxReport = 8 << 20
	report, err := readLastAideReport(aideReportPath)
	if err != nil {
		return map[string]any{"report": "", "available": false, "reason": err.Error()}, nil
	}
	// The summary and the entries come first; a report over the cap loses the
	// end of its details, not its top.
	if len(report) > maxReport {
		report = report[:maxReport]
	}
	return map[string]any{"report": report, "available": true}, nil
}

// aideCheckUnit is the daily check's unit (install/systemd); "Run check now"
// starts it too, so a manual check writes the report log the same way.
const aideCheckUnit = "jabali-aide-check.service"

// mwAideCheckHandler starts a check and returns. GH #2079: it ran aide --check
// inside the call, which takes minutes, longer than the panel's request may
// last, so the page got no answer while the check went on.
// security.aide.status reports the run.
func mwAideCheckHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	if aideUnitRunning(ctx, aideCheckUnit) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "a check is already running"}
	}
	out, err := execCommandContext(ctx, "systemctl", "start", "--no-block", aideCheckUnit).CombinedOutput()
	if err != nil {
		return nil, mwInternal("systemctl start "+aideCheckUnit+": "+strings.TrimSpace(string(out)), err)
	}
	return map[string]any{"started": true}, nil
}

// aideCheckStatus is the check part of security.aide.status. Failed means the
// last run of the check unit (manual or daily) failed.
type aideCheckStatus struct {
	Running bool `json:"running"`
	Failed  bool `json:"failed,omitempty"`
}

// readAideCheckStatus is nil when the check unit is idle and its last run (if
// any) succeeded.
func readAideCheckStatus(ctx context.Context) *aideCheckStatus {
	switch aideUnitState(ctx, aideCheckUnit) {
	case "active", "activating", "deactivating", "reloading":
		return &aideCheckStatus{Running: true}
	case "failed":
		return &aideCheckStatus{Failed: true}
	}
	return nil
}

// mwAideRebuildHandler re-initialises the AIDE database from the current
// filesystem state, replacing the integrity baseline. With dry_run it returns
// the plan without touching the DB. Gitea #561 — the GUI equivalent of
// `jabali aide rebuild --full`.
//
// GH #2079: this ran /usr/sbin/aideinit inside the call. aideinit runs aide as
// _aide, which can't open jabali's report log, so every rebuild failed with
// exit 17; and a rebuild takes minutes, longer than the panel's request may
// last. It now starts `jabali aide rebuild --full` (aide --init as root) in its
// own unit and returns; security.aide.status reports how it went.
func mwAideRebuildHandler(ctx context.Context, payload json.RawMessage) (any, error) {
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	_ = json.Unmarshal(payload, &req)
	if req.DryRun {
		return map[string]any{"dry_run": true, "plan": "/usr/bin/aide --init --config=/etc/aide/aide.conf (as root, in the background), then aide.db.new replaces aide.db"}, nil
	}
	if aideUnitRunning(ctx, agentwire.AideRebuildUnit) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "a baseline rebuild is already running"}
	}
	// Record the start before the unit runs: if the rebuild dies before it
	// writes anything (an older jabali without --result-file), the status
	// still says it stopped. The rebuild overwrites this record.
	started := time.Now().UTC().Format(time.RFC3339)
	if err := writeAideRebuildRecord(agentwire.AideRebuildResult{Running: true, StartedAt: started}); err != nil {
		return nil, mwInternal("record the rebuild start", err)
	}
	_ = execCommandContext(ctx, "systemctl", "reset-failed", agentwire.AideRebuildUnit).Run()
	out, err := execCommandContext(ctx, "systemd-run", //nolint:gosec // argv form, no shell
		"--quiet",
		"--collect",
		"--no-block",
		"--unit="+agentwire.AideRebuildUnit,
		"--description=Jabali AIDE baseline rebuild (GH #2079)",
		// The same footprint as the daily check (jabali-aide-check.service).
		"--property=Slice=jabali-maintenance.slice",
		"--property=Nice=19",
		"--property=IOSchedulingClass=idle",
		"/usr/local/bin/jabali", "aide", "rebuild", "--full", "--result-file", aideRebuildResultPath,
	).CombinedOutput()
	if err != nil {
		msg := "systemd-run: " + strings.TrimSpace(string(out))
		_ = writeAideRebuildRecord(agentwire.AideRebuildResult{
			Error: "the rebuild didn't start: " + msg, StartedAt: started,
			FinishedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return nil, mwInternal(msg, err)
	}
	return map[string]any{"started": true}, nil
}

// writeAideRebuildRecord replaces aideRebuildResultPath (root only, 0600).
func writeAideRebuildRecord(r agentwire.AideRebuildResult) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := aideRebuildResultPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, aideRebuildResultPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// aideRebuildResultPath is agentwire.AideRebuildResultPath; a var for tests.
var aideRebuildResultPath = agentwire.AideRebuildResultPath

// aideRebuildStatus is the rebuild part of security.aide.status. OK is set
// once a rebuild has ended; Error says why it failed.
type aideRebuildStatus struct {
	Running    bool   `json:"running"`
	OK         *bool  `json:"ok,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// aideUnitState is what `systemctl is-active` prints for unit. It exits
// non-zero for every state but active, so read the output, not the status.
func aideUnitState(ctx context.Context, unit string) string {
	out, _ := execCommandContext(ctx, "systemctl", "is-active", unit).Output()
	return strings.TrimSpace(string(out))
}

func aideUnitRunning(ctx context.Context, unit string) bool {
	switch aideUnitState(ctx, unit) {
	case "active", "activating", "deactivating", "reloading":
		return true
	}
	return false
}

// readAideRebuildStatus combines the rebuild unit's state with the result file
// the rebuild writes. nil when the panel never started a rebuild.
func readAideRebuildStatus(ctx context.Context) *aideRebuildStatus {
	running := aideUnitRunning(ctx, agentwire.AideRebuildUnit)
	var res agentwire.AideRebuildResult
	b, err := os.ReadFile(aideRebuildResultPath)
	haveFile := err == nil && json.Unmarshal(b, &res) == nil
	if running {
		st := &aideRebuildStatus{Running: true}
		// Until the new rebuild writes its start, the file holds the last one.
		if haveFile && res.Running {
			st.StartedAt = res.StartedAt
		}
		return st
	}
	if !haveFile {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return &aideRebuildStatus{OK: new(bool), Error: "can't read the rebuild result: " + err.Error()}
		}
		return nil
	}
	ok := res.OK && !res.Running
	st := &aideRebuildStatus{OK: &ok, Error: res.Error, StartedAt: res.StartedAt, FinishedAt: res.FinishedAt}
	if res.Running {
		// The unit is gone but the rebuild never recorded an end: it was
		// killed, the server restarted, or the command failed at once.
		st.Error = "the rebuild stopped before it finished; the baseline may not have been replaced (see the DB age)"
	}
	return st
}

func init() {
	Default.Register("security.aide.report", mwAideReportHandler)
	Default.Register("security.aide.status", mwAideStatusHandler)
	Default.Register("security.aide.check", mwAideCheckHandler)
	Default.Register("security.aide.rebuild", mwAideRebuildHandler)
}
