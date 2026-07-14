package plesk

import (
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// multiSpaceGap matches the column gap (2+ spaces) between a key and its
// value in Plesk's aligned `--info` output.
var multiSpaceGap = regexp.MustCompile(`\s{2,}`)

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
		// Two Plesk --info formats coexist: `Key: value` (colon-separated,
		// e.g. subscription/domain --info) and `Key<2+ spaces>value`
		// (column-aligned, multi-word keys, e.g. service_plan --info). Split
		// on whichever delimiter appears FIRST so an aligned line whose value
		// itself contains a colon ("... by size: 10240 KB") keys correctly.
		key, val := "", ""
		colon := strings.IndexByte(trimmed, ':')
		gap := multiSpaceGap.FindStringIndex(trimmed)
		switch {
		case colon >= 0 && (gap == nil || colon < gap[0]):
			key, val = trimmed[:colon], strings.TrimSpace(trimmed[colon+1:])
		case gap != nil:
			key, val = trimmed[:gap[0]], strings.TrimSpace(trimmed[gap[1]:])
		default:
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

// sqlQuote single-quotes a value for safe interpolation into a `plesk db`
// SQL statement (doubles embedded single quotes). Source-provided names
// (domains) never reach the psa SQL unescaped.
func sqlQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
