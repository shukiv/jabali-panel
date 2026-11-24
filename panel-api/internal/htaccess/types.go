// Package htaccess converts an Apache .htaccess file into jabali's typed
// NginxRule Rule Builder entries (models.NginxRule), compiled by
// internal/nginxrules and gated by nginx -t. It NEVER emits raw nginx
// directives — tenant input becomes a constrained, whitelisted set of typed
// rules. See ADR-0130 and plans/htaccess-converter.md.
//
// Two invariants are load-bearing (and tested):
//
//  1. Fail-closed. If an access-control / auth / conditional block cannot be
//     FULLY represented, the converter emits NOTHING for it plus a warning —
//     never the permissive half. Dropping a RewriteCond while keeping its
//     RewriteRule, or mis-mapping Order, can turn a deny into an allow.
//  2. Front-controller is a no-op. The default vhost `location /` already
//     does `try_files $uri $uri/ /index.php?$query_string`, so the canonical
//     WordPress/Drupal/Laravel `RewriteCond !-f/!-d -> index.php [L]` block is
//     recognized and SKIPPED with a note, not translated.
package htaccess

import (
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/redirects"
)

// Warning records a line the converter could not represent as a typed rule.
// Security-relevant gaps (an unconvertible deny/forbid/auth) set Security so
// callers can surface them prominently — a dropped deny is more dangerous
// than a dropped redirect.
type Warning struct {
	Line     int    `json:"line"`
	Source   string `json:"source"`
	Reason   string `json:"reason"`
	Security bool   `json:"security,omitempty"`
}

// Result is the outcome of converting one .htaccess file.
type Result struct {
	// Rules are the typed Rule Builder entries to merge into a domain's
	// NginxRules. Always safe to compile (typed, whitelisted).
	Rules []models.NginxRule `json:"rules"`
	// Warnings list lines that were NOT converted (and why). Nothing is
	// dropped silently.
	Warnings []Warning `json:"warnings"`
	// Notes are informational (e.g. a front-controller block intentionally
	// skipped because the default routing already covers it).
	Notes []string `json:"notes"`
	// Redirects are page redirects for the domain's Redirects list (mod_alias
	// Redirect lines), kept apart from Rules: exact nginx locations instead
	// of rewrite rules, and their own, larger cap.
	Redirects []models.PageRedirect `json:"redirects"`

	redirectPaths map[string]bool
}

// addRedirect appends pr unless an earlier redirect already matches one of its
// paths (Apache applies the first; nginx would refuse the duplicate location).
func (r *Result) addRedirect(pr models.PageRedirect) bool {
	paths := redirects.ExactSources(pr)
	for _, p := range paths {
		if r.redirectPaths[p] {
			return false
		}
	}
	if r.redirectPaths == nil {
		r.redirectPaths = map[string]bool{}
	}
	for _, p := range paths {
		r.redirectPaths[p] = true
	}
	r.Redirects = append(r.Redirects, pr)
	return true
}

func (r *Result) addRule(rule models.NginxRule) { r.Rules = append(r.Rules, rule) }
func (r *Result) addNote(note string)           { r.Notes = append(r.Notes, note) }
func (r *Result) warn(line int, source, reason string) {
	r.Warnings = append(r.Warnings, Warning{Line: line, Source: source, Reason: reason})
}
func (r *Result) warnSec(line int, source, reason string) {
	r.Warnings = append(r.Warnings, Warning{Line: line, Source: source, Reason: reason, Security: true})
}
