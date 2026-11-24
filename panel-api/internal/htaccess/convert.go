package htaccess

import (
	"strings"
)

// Convert parses an Apache .htaccess file and returns the typed rules it can
// represent plus warnings/notes for everything it cannot.
//
// basePath is the URL path the .htaccess governs (the docroot-relative
// directory, e.g. "/" for the docroot or "/blog/" for a subdir). Apache
// matches RewriteRule patterns relative to that directory; nginx matches the
// full URI, so basePath (overridden by a RewriteBase directive if present) is
// prepended when rebasing patterns. An empty basePath is treated as "/".
func Convert(htaccess, basePath string) Result {
	var res Result

	base := normalizeBase(basePath)
	st := &state{base: base, noted: map[string]bool{}}

	skipDepth := 0 // inside a scoping section (<Files>/<Directory>/…) we skip

	for _, ln := range logicalLines(htaccess) {
		// Container tags. <IfModule>/<IfVersion> are mere guards: drop the
		// wrapper, process the contents. Scoping containers (<Files>,
		// <Directory>, <Location>, <Limit>, <RequireAll>, …) restrict their
		// contents to specific paths/methods; applying those contents
		// globally would change scope, so we SKIP the whole block and warn
		// (fail-closed). Unknown container tags are treated as scoping.
		if strings.HasPrefix(ln.text, "<") {
			lower := strings.ToLower(ln.text)
			switch {
			case strings.HasPrefix(lower, "<ifmodule"), strings.HasPrefix(lower, "<ifversion"),
				strings.HasPrefix(lower, "</ifmodule"), strings.HasPrefix(lower, "</ifversion"):
				// guard wrapper — ignore the line, keep processing contents
			case strings.HasPrefix(lower, "</"):
				if skipDepth > 0 {
					skipDepth--
				}
			default:
				if skipDepth == 0 {
					res.warnSec(ln.num, ln.text, "scoped container (e.g. <Files>/<Directory>/<Limit>) is not converted — its contents apply to specific paths/methods; recreate them as per-path rules in the panel")
				}
				skipDepth++
			}
			continue
		}
		if skipDepth > 0 {
			continue
		}

		fields := tokenize(ln.text)
		if len(fields) == 0 {
			continue
		}
		directive := fields[0]
		args := fields[1:]

		switch strings.ToLower(directive) {
		case "rewriteengine":
			st.rewriteEngineOff = len(args) > 0 && strings.EqualFold(args[0], "off")
		case "rewritebase":
			if len(args) > 0 {
				st.base = normalizeBase(args[0])
			}
		case "rewritecond":
			// Accumulate; consumed (or rejected) by the next RewriteRule.
			st.pendingConds = append(st.pendingConds, condLine{text: ln.text, num: ln.num})
		case "rewriterule":
			convertRewriteRule(&res, st, args, ln)
			st.pendingConds = nil
		case "redirect", "redirectpermanent", "redirecttemp":
			convertRedirect(&res, st, directive, args, ln)
		case "redirectmatch":
			convertRedirectMatch(&res, st, args, ln)
		case "header":
			convertHeader(&res, args, ln)
		case "php_value", "php_flag":
			convertPHP(&res, directive, args, ln)
		case "options":
			convertOptions(&res, args, ln)
		case "order", "allow", "deny", "require":
			// Access-control directives are buffered and emitted together at
			// the end so Order + Allow/Deny semantics resolve as a unit.
			st.access = append(st.access, accessLine{directive: strings.ToLower(directive), args: args, num: ln.num})
		case "addoutputfilterbytype", "addoutputfilter", "setoutputfilter":
			// Apache gzip (mod_deflate). nginx compresses server-wide, so
			// these need no per-site rule — collapse to one reassuring note.
			if argsMentionDeflate(args) {
				noteOnce(&res, st, "gzip", "gzip/DEFLATE compression directives skipped — nginx already compresses responses server-wide, so no per-site rule is needed")
			} else {
				res.warn(ln.num, ln.text, "output filter not supported by the Rule Builder")
			}
		case "expiresactive", "expiresbytype", "expiresdefault":
			// mod_expires browser caching. nginx serves static assets with its
			// own cache headers; configure caching in the panel if needed.
			noteOnce(&res, st, "expires", "browser-cache directives (mod_expires) skipped — nginx sets cache headers for static assets; adjust caching in the panel if needed")
		case "addtype", "addcharset", "adddefaultcharset":
			noteOnce(&res, st, "mime", "MIME-type / charset directives skipped — nginx serves these from its own mime.types")
		case "errordocument":
			// `ErrorDocument 404 /path` maps to nginx error_page, which the
			// domain's Raw directives accept (and, rendered before the
			// template's branded pages, override them).
			if len(args) == 2 && strings.HasPrefix(args[1], "/") && !strings.ContainsAny(args[1], " \t;{}\"'") {
				res.warn(ln.num, ln.text, "not a Rule Builder entry: add `error_page "+args[0]+" "+args[1]+";` under Raw directives")
			} else {
				res.warn(ln.num, ln.text, "directive not supported by the Rule Builder; configure it in the panel instead")
			}
		case "directoryindex", "setenv", "setenvif", "addhandler":
			res.warn(ln.num, ln.text, "directive not supported by the Rule Builder; configure it in the panel instead")
		default:
			res.warn(ln.num, ln.text, "unrecognized or unsupported directive")
		}
	}

	// Flush any pending RewriteCond that never reached a RewriteRule.
	if len(st.pendingConds) > 0 {
		for _, c := range st.pendingConds {
			res.warn(c.num, c.text, "RewriteCond with no following RewriteRule — ignored")
		}
	}

	// Resolve buffered access-control directives last (fail-closed).
	flushAccess(&res, st)

	return res
}

// state threads parser context across lines.
type state struct {
	base             string
	rewriteEngineOff bool
	pendingConds     []condLine
	access           []accessLine
	noted            map[string]bool // dedup keys for noteOnce
}

type condLine struct {
	text string
	num  int
}

type accessLine struct {
	directive string // order | allow | deny | require
	args      []string
	num       int
}

// line is one logical .htaccess line (after comment-stripping and
// backslash-continuation joining) with its original 1-based line number.
type line struct {
	text string
	num  int
}

// logicalLines splits the file into logical lines: strips comments and blank
// lines, and joins Apache backslash line-continuations. The reported line
// number is the line where the (possibly continued) directive starts.
func logicalLines(s string) []line {
	raw := strings.Split(s, "\n")
	var out []line
	var buf strings.Builder
	startNum := 0
	continuing := false

	for i, r := range raw {
		t := strings.TrimRight(r, "\r")
		// A '#' comment: Apache treats a line whose first non-blank char is
		// '#' as a comment. Inline '#' is NOT a comment in .htaccess, so only
		// drop whole-line comments.
		if !continuing && strings.HasPrefix(strings.TrimSpace(t), "#") {
			continue
		}
		if !continuing {
			if strings.TrimSpace(t) == "" {
				continue
			}
			startNum = i + 1
		}
		if strings.HasSuffix(t, "\\") {
			buf.WriteString(strings.TrimSuffix(t, "\\"))
			continuing = true
			continue
		}
		buf.WriteString(t)
		out = append(out, line{text: strings.TrimSpace(buf.String()), num: startNum})
		buf.Reset()
		continuing = false
	}
	if buf.Len() > 0 {
		out = append(out, line{text: strings.TrimSpace(buf.String()), num: startNum})
	}
	return out
}

// tokenize splits a directive line into fields, honoring double and single
// quotes (Apache allows quoted args, e.g. Header set X "a b").
func tokenize(s string) []string {
	var fields []string
	var cur strings.Builder
	inQuote := rune(0)
	flush := func() {
		if cur.Len() > 0 {
			fields = append(fields, cur.String())
			cur.Reset()
		}
	}
	for _, c := range s {
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '"' || c == '\'':
			inQuote = c
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return fields
}

// normalizeBase returns a base path that starts with "/" and ends with "/".
// "" and "/" both normalize to "/".
func normalizeBase(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// noteOnce adds an informational note for a category only the first time it is
// seen, so a 20-line gzip/expires block produces one note, not 20 warnings.
func noteOnce(res *Result, st *state, key, msg string) {
	if st.noted[key] {
		return
	}
	st.noted[key] = true
	res.addNote(msg)
}

// argsMentionDeflate reports whether any token is DEFLATE/gzip (the compression
// filter), vs some other output filter we don't recognize.
func argsMentionDeflate(args []string) bool {
	for _, a := range args {
		if strings.EqualFold(a, "deflate") || strings.EqualFold(a, "gzip") {
			return true
		}
	}
	return false
}
