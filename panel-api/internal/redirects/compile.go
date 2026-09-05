// Package redirects compiles the structured redirect fields on a Domain
// into nginx directive strings the agent drops into a server {} block.
//
// Contract:
//
//   - Whole-domain redirect wins: if RedirectAllTo is set, page redirects
//     are ignored entirely (single `return N $url;` directive).
//   - Page redirects compile to `location = /src { return N "url"; }`
//     so unrelated paths still hit the docroot.
//   - Wildcard (prefix) redirects compile to `location ^~ /src { rewrite ... }`.
//   - Destination URLs emit as double-quoted nginx strings with embedded
//     quotes and backslashes escaped; nginx still interpolates $vars.
//   - Active is optional (nil means true); inactive entries are skipped.
//   - Wildcard only supports 301/302; 307/308 are rejected at the validator.
package redirects

import (
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// isActive returns true if a PageRedirect is active.
// nil Active field is treated as true for backwards compatibility.
func isActive(pr models.PageRedirect) bool {
	return pr.Active == nil || *pr.Active
}

// redirectAllCode maps a stored redirect_all_type to the numeric nginx return
// code emitted in `return <code> <url>;`. The value should already be numeric
// (the HTTP handler and, since JAB-318, the CLI store 301|302|307|308), but the
// old `jabali domain set` CLI persisted the literals "permanent"/"temporary" —
// neither a valid nginx code — so `return permanent …;` failed nginx -t and
// broke reload. Heal those existing rows (and any restored from a backup that
// carried the bad value) at render time rather than with a data migration; an
// already-numeric or unknown value passes through unchanged (unknown still fails
// nginx -t, but that is a value neither writer can produce).
func redirectAllCode(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "permanent":
		return "301"
	case "temporary":
		return "302"
	}
	return t
}

func Compile(d *models.Domain) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	if d.RedirectAllTo != nil && *d.RedirectAllTo != "" {
		code := "301"
		if d.RedirectAllType != nil {
			code = redirectAllCode(*d.RedirectAllType)
		}
		fmt.Fprintf(&b, "    return %s %s;\n", code, quoteNginxURL(*d.RedirectAllTo))
		return b.String()
	}
	for _, pr := range d.PageRedirects {
		// Skip inactive entries
		if !isActive(pr) {
			continue
		}

		if pr.Wildcard {
			// Wildcard (prefix) match: location ^~ /src, rewrite to strip prefix and redirect.
			// Use rewrite to capture the remainder after source and append to destination.
			// Supports 301/302 status codes via the rewrite flag.
			fmt.Fprintf(&b, "    location ^~ %s {\n", quoteNginxLocation(pr.Source))

			statusFlag := "permanent" // 301
			if pr.Type == "302" {
				statusFlag = "redirect" // 302
			}

			// Capture everything after the source prefix (with optional trailing slash handling)
			// and append it to the destination URL
			escapedSource := escapeRegex(pr.Source)
			fmt.Fprintf(&b, "        rewrite ^%s/?(.*)$ %s %s;\n",
				escapedSource, quoteNginxURL(pr.Destination+"/$1"), statusFlag)
			fmt.Fprintf(&b, "    }\n")
		} else {
			// Exact-match: location = /src, plain return
			fmt.Fprintf(&b, "    location = %s {\n        return %s %s;\n    }\n",
				quoteNginxLocation(pr.Source), pr.Type, quoteNginxURL(pr.Destination))
		}
	}
	return b.String()
}

// escapeRegex escapes special regex characters in a path so it's safe in rewrite patterns.
func escapeRegex(s string) string {
	// Escape regex metacharacters: . ^ $ * + ? { } [ ] ( ) | \
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, ".", `\.`)
	s = strings.ReplaceAll(s, "^", `\^`)
	s = strings.ReplaceAll(s, "$", `\$`)
	s = strings.ReplaceAll(s, "*", `\*`)
	s = strings.ReplaceAll(s, "+", `\+`)
	s = strings.ReplaceAll(s, "?", `\?`)
	s = strings.ReplaceAll(s, "{", `\{`)
	s = strings.ReplaceAll(s, "}", `\}`)
	s = strings.ReplaceAll(s, "[", `\[`)
	s = strings.ReplaceAll(s, "]", `\]`)
	s = strings.ReplaceAll(s, "(", `\(`)
	s = strings.ReplaceAll(s, ")", `\)`)
	s = strings.ReplaceAll(s, "|", `\|`)
	return s
}

func quoteNginxURL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func quoteNginxLocation(s string) string {
	if strings.ContainsAny(s, " \t\"'\\") {
		return quoteNginxURL(s)
	}
	return s
}
