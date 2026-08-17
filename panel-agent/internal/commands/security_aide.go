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
	"fmt"
	"io"
	osexec "os/exec"
	"os"
	"strings"
	"time"
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

func mwAideStatusHandler(_ context.Context, _ json.RawMessage) (any, error) {
	resp := aideStatusResponse{Sample: []aideSampleRow{}}

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

	// Parse the last report — `aide --check` appends to aide.report.log
	// on every timer run. We tail the last block, look for the
	// "AIDE found differences" header + "added entries" / "changed
	// entries" / "removed entries" counters.
	if f, err := os.Open(aideReportPath); err == nil {
		defer f.Close()
		// GH #678: aide --check APPENDS each run to the report log, so the LATEST
		// report is at the END. Seek to the last 4 MiB (not the first) so we parse
		// the newest block, not a stale report frozen at the front of the log.
		const window = int64(4 * 1024 * 1024)
		if fi, sErr := f.Stat(); sErr == nil && fi.Size() > window {
			_, _ = f.Seek(fi.Size()-window, io.SeekStart)
		}
		buf := make([]byte, window)
		n, _ := f.Read(buf)
		data := string(buf[:n])
		// parseAideReport captures the FIRST report block it sees, so isolate the
		// LAST one — each AIDE report starts with a "Start timestamp:" line.
		if idx := strings.LastIndex(data, "Start timestamp:"); idx >= 0 {
			if nl := strings.LastIndexByte(data[:idx], '\n'); nl >= 0 {
				idx = nl + 1
			}
			data = data[idx:]
		}
		parseAideReport(data, &resp)
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

	return resp, nil
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
	b, err := os.ReadFile(aideReportPath)
	if err != nil {
		return map[string]any{"report": "", "available": false, "reason": err.Error()}, nil
	}
	if len(b) > maxReport {
		b = b[len(b)-maxReport:]
	}
	return map[string]any{"report": string(b), "available": true}, nil
}

func mwAideCheckHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	if _, err := os.Stat("/usr/bin/aide"); err != nil {
		return nil, mwInternal("aide binary not found", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	cmd := execCommandContext(ctx, "/usr/bin/aide", "--config", "/etc/aide/aide.conf", "--check")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, mwInternal("aide stdout pipe", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, mwInternal("aide start", err)
	}
	out, _ := io.ReadAll(stdout)
	if err := cmd.Wait(); err != nil {
		// AIDE returns non-zero when diffs are found — not an error.
		if ee, ok := err.(*osexec.ExitError); ok && ee.ExitCode() < 8 {
			// fall through — diffs are expected output
		} else {
			return nil, mwInternal("aide wait", err)
		}
	}

	resp := aideStatusResponse{Enabled: true, Sample: []aideSampleRow{}}
	parseAideReport(string(out), &resp)
	if st, err := os.Stat(aideDBPath); err == nil {
		resp.DBAgeSeconds = int64(time.Since(st.ModTime()).Seconds())
	}
	return resp, nil
}

// mwAideRebuildHandler re-initialises the AIDE database from the current
// filesystem state (aideinit -y -f), replacing the integrity baseline. With
// dry_run it returns the plan without touching the DB. Gitea #561 — the GUI
// rebuild action dispatches this; the CLI `jabali aide rebuild` still execs
// aideinit directly on the host.
func mwAideRebuildHandler(ctx context.Context, payload json.RawMessage) (any, error) {
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	_ = json.Unmarshal(payload, &req)
	if req.DryRun {
		return map[string]any{"dry_run": true, "plan": "/usr/sbin/aideinit -y -f"}, nil
	}
	if _, err := os.Stat("/usr/sbin/aideinit"); err != nil {
		return nil, mwInternal("aideinit not found (install aide-common)", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	out, err := execCommandContext(ctx, "/usr/sbin/aideinit", "-y", "-f").CombinedOutput()
	if err != nil {
		tail := string(out)
		if len(tail) > 500 {
			tail = tail[len(tail)-500:]
		}
		return nil, mwInternal("aideinit failed: "+tail, err)
	}
	resp := map[string]any{"rebuilt": true}
	if st, serr := os.Stat(aideDBPath); serr == nil {
		resp["db_mtime"] = st.ModTime().UTC().Format(time.RFC3339)
	}
	return resp, nil
}

func init() {
	Default.Register("security.aide.report", mwAideReportHandler)
	Default.Register("security.aide.status", mwAideStatusHandler)
	Default.Register("security.aide.check", mwAideCheckHandler)
	Default.Register("security.aide.rebuild", mwAideRebuildHandler)
}
