// Package nginximport converts a pasted raw nginx snippet into jabali's typed
// NginxRule Rule Builder entries (models.NginxRule) — the same target as
// internal/htaccess, but for people migrating from an nginx-based panel rather
// than Apache. See GH #1624.
//
// It is a SHAPE MATCHER, not an nginx parser. It recognizes only a narrow,
// tenant-safe set of constructs and turns each into a typed rule:
//
//	rewrite <pat> <repl> [flag];                          -> rewrite
//	add_header <name> <value> [always];                   -> custom_header
//	location ~* \.(a|b)$ { deny all; }                    -> deny_paths
//	location ~* \.(a|b)$ { expires <dur>; }               -> static_cache
//
// Everything else — any other directive, any location whose matcher is not the
// extension-anchored regex above, any body directive other than the ones listed
// (root/alias/proxy_pass/fastcgi_pass/try_files/return/...) — is reported as a
// Warning and NEVER emitted as a rule. Security-relevant drops (proxy_pass,
// root, alias, fastcgi_pass, a deny that could not be represented) set Security.
//
// The converter emits candidate rules only; the API handler re-runs the tenant
// rule validator on each one and demotes any that would be rejected on save, so
// what the operator sees in the preview is exactly what will apply.
package nginximport

import (
	"regexp"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Warning records a snippet line/block that was NOT converted, and why.
type Warning struct {
	Line     int    `json:"line"`
	Source   string `json:"source"`
	Reason   string `json:"reason"`
	Security bool   `json:"security,omitempty"`
}

// Result is the outcome of converting one snippet.
type Result struct {
	Rules    []models.NginxRule `json:"rules"`
	Warnings []Warning          `json:"warnings"`
	Notes    []string           `json:"notes"`
}

func (r *Result) addRule(rule models.NginxRule)        { r.Rules = append(r.Rules, rule) }
func (r *Result) addNote(note string)                  { r.Notes = append(r.Notes, note) }
func (r *Result) warn(line int, source, reason string) { r.Warnings = append(r.Warnings, Warning{line, source, reason, false}) }
func (r *Result) warnSec(line int, src, reason string) { r.Warnings = append(r.Warnings, Warning{line, src, reason, true}) }

// locHeaderRE matches the ONLY location form we accept: an extension-anchored,
// case-insensitive-or-not regex location — `location ~* \.(a|b)$ {` (the `\.`
// may appear as a literal `.` in some pastes). The alternation group is captured
// so we can lift the extensions; the charset here is deliberately loose
// ([^)]* ) because the tenant rule validator is the real gate on each extension.
var locHeaderRE = regexp.MustCompile(`^location\s+~\*?\s+\\?\.\(([^)]*)\)\$\s*\{?$`)

// securityDirectives are body/standalone directives whose silent drop is worth
// flagging red — they route, read files, or proxy.
var securityDirectives = map[string]struct{}{
	"proxy_pass": {}, "fastcgi_pass": {}, "root": {}, "alias": {},
	"return": {}, "rewrite": {}, "include": {}, "try_files": {},
}

// Convert parses a raw nginx snippet into typed rules + warnings for the rest.
func Convert(snippet string) Result {
	res := Result{Rules: []models.NginxRule{}, Warnings: []Warning{}, Notes: []string{}}
	lines := strings.Split(snippet, "\n")
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		clean := stripComment(raw)
		if strings.TrimSpace(clean) == "" {
			continue
		}
		lineNo := i + 1

		// A location block: collect from here until braces balance (may be on
		// the same line, e.g. `location ~* \.(env)$ { deny all; }`).
		if firstToken(clean) == "location" {
			block, endIdx := collectBlock(lines, i)
			convertLocation(&res, block, lineNo)
			// A directive tacked onto the block's closing-brace line (e.g.
			// `} proxy_pass ...;`) must NOT vanish: collectBlock consumed the
			// whole closing line and convertLocation truncated the body at the
			// last `}`, so anything after it would be dropped with no warning.
			// Re-classify each trailing statement so it becomes at least a
			// warning (it can still never become a rule — same guards apply).
			for _, stmt := range strings.Split(afterLastBrace(stripComment(lines[endIdx])), ";") {
				if s := strings.TrimSpace(stmt); s != "" {
					convertDirective(&res, s, endIdx+1, s)
				}
			}
			i = endIdx
			continue
		}

		// A stray closing brace (defensive — collectBlock consumes real ones).
		if strings.TrimSpace(clean) == "}" {
			continue
		}

		// Single-statement directive line.
		stmt := strings.TrimSpace(clean)
		if !strings.HasSuffix(stmt, ";") {
			res.warn(lineNo, trimLong(raw), "not a complete `directive ...;` statement — skipped")
			continue
		}
		convertDirective(&res, strings.TrimSuffix(stmt, ";"), lineNo, raw)
	}
	return res
}

// collectBlock returns the joined text of a brace-delimited block starting at
// lines[start], and the index of its last line. Brace counting is naive (it does
// not track quotes) — acceptable because the only accepted block bodies
// (deny/expires/add_header) never contain a brace in a quoted value, and any
// weird input just falls through to an "unsupported" warning.
func collectBlock(lines []string, start int) (block string, end int) {
	depth := 0
	var b strings.Builder
	for i := start; i < len(lines); i++ {
		clean := stripComment(lines[i])
		b.WriteString(clean)
		b.WriteByte(' ')
		depth += strings.Count(clean, "{") - strings.Count(clean, "}")
		if strings.Contains(clean, "{") && depth <= 0 {
			return b.String(), i
		}
		if depth <= 0 && strings.Contains(clean, "}") {
			return b.String(), i
		}
	}
	return b.String(), len(lines) - 1
}

// convertLocation classifies a collected `location ... { ... }` block.
func convertLocation(res *Result, block string, lineNo int) {
	open := strings.Index(block, "{")
	if open < 0 {
		res.warn(lineNo, trimLong(block), "location block is not closed — skipped")
		return
	}
	header := strings.TrimSpace(block[:open+1]) // include the `{` for the regex
	m := locHeaderRE.FindStringSubmatch(header)
	if m == nil {
		res.warnSec(lineNo, trimLong(header), "only `location ~* \\.(ext|ext)$` blocks are supported; this matcher may route or expose files — skipped")
		return
	}
	exts := splitExtensions(m[1])

	// Body: between the first `{` and the last `}`.
	body := block[open+1:]
	if c := strings.LastIndex(body, "}"); c >= 0 {
		body = body[:c]
	}

	var deny bool
	var duration string
	for _, stmt := range strings.Split(body, ";") {
		s := strings.TrimSpace(stmt)
		if s == "" {
			continue
		}
		switch tok := firstToken(s); tok {
		case "deny":
			if strings.TrimSpace(strings.TrimPrefix(s, "deny")) == "all" {
				deny = true
			} else {
				res.warn(lineNo, trimLong(s), "only `deny all;` is supported inside a location — skipped block")
				return
			}
		case "expires":
			duration = strings.TrimSpace(strings.TrimPrefix(s, "expires"))
		case "add_header":
			// A caching add_header (e.g. Cache-Control) is intentionally dropped:
			// the panel sets Cache-Control via `expires` and manages security
			// headers itself (an add_header inside a location would suppress
			// them — JAB-70). Note it rather than importing it.
			res.addNote("dropped an `add_header` inside a location block (line " + strconv.Itoa(lineNo) + "); the panel manages response headers for cached files")
		default:
			sec := isSecurityDirective(tok)
			msg := "directive `" + tok + "` is not supported inside a location — skipped block"
			if sec {
				res.warnSec(lineNo, trimLong(s), msg)
			} else {
				res.warn(lineNo, trimLong(s), msg)
			}
			return
		}
	}

	switch {
	case deny && duration == "":
		res.addRule(models.NginxRule{Type: "deny_paths", Extensions: exts})
	case !deny && duration != "":
		res.addRule(models.NginxRule{Type: "static_cache", Extensions: exts, Duration: duration})
	case deny && duration != "":
		res.warn(lineNo, trimLong(header), "location mixes deny and expires — split into separate rules; skipped")
	default:
		res.warn(lineNo, trimLong(header), "location has no supported action (expected `deny all;` or `expires ...;`) — skipped")
	}
}

// convertDirective handles a single `name args` statement (the trailing `;`
// already removed).
func convertDirective(res *Result, stmt string, lineNo int, raw string) {
	args := tokenize(stmt)
	if len(args) == 0 {
		return
	}
	switch args[0] {
	case "rewrite":
		// rewrite <pattern> <replacement> [flag]
		if len(args) < 3 || len(args) > 4 {
			res.warn(lineNo, trimLong(raw), "rewrite must be `rewrite <pattern> <replacement> [flag]` — skipped")
			return
		}
		rule := models.NginxRule{Type: "rewrite", Pattern: args[1], Replacement: args[2]}
		if len(args) == 4 {
			rule.Flag = args[3]
		}
		res.addRule(rule)
	case "add_header":
		// add_header <name> <value> [always]
		if len(args) < 3 || len(args) > 4 || (len(args) == 4 && args[3] != "always") {
			res.warn(lineNo, trimLong(raw), "add_header must be `add_header <name> <value> [always]` — skipped")
			return
		}
		rule := models.NginxRule{Type: "custom_header", Name: args[1], Value: args[2]}
		if len(args) == 4 {
			always := true
			rule.Always = &always
		}
		res.addRule(rule)
	default:
		sec := isSecurityDirective(args[0])
		msg := "directive `" + args[0] + "` is not one of the importable kinds (rewrite, add_header, deny/expires location) — skipped"
		if sec {
			res.warnSec(lineNo, trimLong(raw), msg)
		} else {
			res.warn(lineNo, trimLong(raw), msg)
		}
	}
}

// --- small helpers -------------------------------------------------------

func isSecurityDirective(tok string) bool {
	_, ok := securityDirectives[tok]
	return ok
}

// stripComment removes an unquoted `#` comment to end of line.
func stripComment(line string) string {
	inS, inD := false, false
	for i, c := range line {
		switch c {
		case '\'':
			if !inD {
				inS = !inS
			}
		case '"':
			if !inS {
				inD = !inD
			}
		case '#':
			if !inS && !inD {
				return line[:i]
			}
		}
	}
	return line
}

// afterLastBrace returns the part of a line following its last `}` (empty if
// there is none). Used to recover a directive tacked onto a block's closing line.
func afterLastBrace(line string) string {
	if i := strings.LastIndex(line, "}"); i >= 0 {
		return line[i+1:]
	}
	return ""
}

func firstToken(s string) string {
	f := strings.Fields(strings.TrimSpace(s))
	if len(f) == 0 {
		return ""
	}
	return f[0]
}

// tokenize splits a statement on whitespace, keeping single/double-quoted spans
// together and stripping the surrounding quotes.
func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	inS, inD, started := false, false, false
	flush := func() {
		if started {
			out = append(out, cur.String())
			cur.Reset()
			started = false
		}
	}
	for _, c := range s {
		switch {
		case c == '\'' && !inD:
			inS = !inS
			started = true
		case c == '"' && !inS:
			inD = !inD
			started = true
		case (c == ' ' || c == '\t') && !inS && !inD:
			flush()
		default:
			cur.WriteRune(c)
			started = true
		}
	}
	flush()
	return out
}

// splitExtensions turns "env|sql|bak" into ["env","sql","bak"] (empty entries
// dropped). Validation of each extension happens in the API handler.
func splitExtensions(group string) []string {
	out := []string{}
	for _, e := range strings.Split(group, "|") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// trimLong caps an echoed source string so a giant pasted line can't bloat the
// response.
func trimLong(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
