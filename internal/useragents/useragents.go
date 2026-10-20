// Package useragents holds the rules for the admin's server-wide list of
// blocked user agents: panel-api checks the setting with them, and the agent
// checks again before it writes the list into nginx config.
//
// An entry is matched as a case-insensitive substring of the User-Agent
// header. The allowed characters are the ones user-agent product tokens use;
// none of them can end a quoted nginx string or a regex group, so an entry
// cannot change the config around it.
package useragents

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxBlocked is the most entries the list may hold.
const MaxBlocked = 200

var blockedRe = regexp.MustCompile(`^[A-Za-z0-9 ._/+-]{3,128}$`)

// Valid reports whether s may be an entry: 3 to 128 letters, digits, spaces
// and . _ / + -.
func Valid(s string) bool { return blockedRe.MatchString(s) }

// Normalize trims each entry, drops empty ones and case-insensitive
// duplicates (keeping the first spelling), and checks what is left.
func Normalize(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		if !Valid(e) {
			return nil, fmt.Errorf("%q: an entry is 3-128 letters, digits, spaces and . _ / + -", e)
		}
		k := strings.ToLower(e)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	if len(out) > MaxBlocked {
		return nil, fmt.Errorf("%d entries; at most %d", len(out), MaxBlocked)
	}
	return out, nil
}
