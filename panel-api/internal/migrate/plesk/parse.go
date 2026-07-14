package plesk

import (
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// parsePleskList turns `plesk bin subscription --list` output — one
// subscription name (the primary domain) per line — into account
// summaries. Blank lines and comment/banner lines are skipped. The
// subscription name is both the source-side ID and the primary domain.
func parsePleskList(out string) []migrate.AccountSummary {
	rows := []migrate.AccountSummary{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "#") {
			continue
		}
		rows = append(rows, migrate.AccountSummary{
			ID:     name,
			Login:  name,
			Domain: name,
		})
	}
	return rows
}

// parsePleskInfo parses the raw key/value output of `plesk bin <util>
// --info` into a map. Plesk prints `Key: value` (and some indented
// `Key   value`) lines; the first `:` or run of spaces after the key
// splits it. Blank lines, comment/banner lines, and section headers
// (lines ending in `:` with no value) are skipped. Keys are lowercased
// + trimmed so callers can look up case-insensitively.
//
// NOTE: coded against Plesk Obsidian CLI docs, not yet validated against
// a live Plesk host — callers treat a missing key as best-effort and
// record a manifest Warning rather than failing (mirrors the DA builders).
func parsePleskInfo(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, val := "", ""
		if i := strings.IndexByte(trimmed, ':'); i >= 0 {
			key, val = trimmed[:i], strings.TrimSpace(trimmed[i+1:])
		} else if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
			key, val = trimmed[:i], strings.TrimSpace(trimmed[i+1:])
		} else {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || val == "" {
			continue // section header or valueless line
		}
		if _, seen := out[key]; !seen {
			out[key] = val
		}
	}
	return out
}
