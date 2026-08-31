package models

import (
	"sort"
	"strings"
)

// SSHLoginIgnoreAccounts is stored as a newline/comma-separated list of SSH
// usernames (GH #1310-adjacent, "drfeed spam"). These helpers are the single
// place that parses and re-serialises it, shared by the eventsource that drops
// ignored logins and the admin handler that edits the list.

// splitSSHIgnore tokenises the raw stored value on commas and newlines.
func splitSSHIgnore(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
}

// ParseSSHIgnoreAccounts returns the normalised list: trimmed, empties dropped,
// de-duplicated, sorted. Sorting keeps the stored form and the API response
// stable regardless of input order.
func ParseSSHIgnoreAccounts(raw string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, tok := range splitSSHIgnore(raw) {
		u := strings.TrimSpace(tok)
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// SSHIgnoreSet is the same normalised list as a lookup set (for the hot path in
// the ssh.login eventsource).
func SSHIgnoreSet(raw string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, u := range ParseSSHIgnoreAccounts(raw) {
		set[u] = struct{}{}
	}
	return set
}

// JoinSSHIgnoreAccounts serialises a list back to the stored form (normalised,
// newline-separated).
func JoinSSHIgnoreAccounts(accounts []string) string {
	return strings.Join(ParseSSHIgnoreAccounts(strings.Join(accounts, "\n")), "\n")
}
