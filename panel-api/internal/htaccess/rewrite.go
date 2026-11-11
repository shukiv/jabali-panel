package htaccess

import (
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// convertRewriteRule handles `RewriteRule pattern substitution [flags]`,
// taking st.pendingConds into account (fail-closed on anything but the
// recognized front-controller idiom).
func convertRewriteRule(res *Result, st *state, args []string, ln line) {
	if st.rewriteEngineOff {
		res.warn(ln.num, ln.text, "RewriteEngine is Off — rule ignored")
		return
	}
	if len(args) < 2 {
		res.warn(ln.num, ln.text, "malformed RewriteRule (need pattern and substitution)")
		return
	}
	pattern, sub := args[0], args[1]
	flags := parseRewriteFlags(args[2:])

	// Front-controller idiom: the conds test "not a real file / dir" and the
	// rule routes everything to a front controller. The default vhost
	// `location /` already does try_files -> index.php, so this is a no-op.
	if isFrontController(st.pendingConds, sub) {
		res.addNote("front-controller routing (e.g. WordPress/Laravel) detected and skipped — already handled by the default `location /` (try_files … /index.php)")
		return
	}

	// Any other RewriteCond makes the rule unrepresentable as a typed rule
	// (nginx `if` is the "if is evil" anti-pattern and would need raw
	// directives). Fail-closed: drop the rule, warn.
	if len(st.pendingConds) > 0 {
		res.warnSec(ln.num, ln.text, "RewriteRule has conditions (RewriteCond) that can't be expressed as a typed rule — dropped to avoid a partial, possibly unsafe translation")
		return
	}

	// [F] forbidden / [G] gone are access denials with no typed-rule
	// equivalent. Dropping them would make content MORE accessible, so warn
	// as security-relevant rather than emit anything.
	if flags.forbidden {
		res.warnSec(ln.num, ln.text, "RewriteRule [F] (forbid) has no typed-rule equivalent — not converted; restrict access via the panel")
		return
	}
	if flags.gone {
		res.warnSec(ln.num, ln.text, "RewriteRule [G] (gone/410) has no typed-rule equivalent — not converted")
		return
	}

	// "-" substitution means "match but do not rewrite" (apply flags only).
	// With no representable flag left (R/F/G handled above), it's a no-op —
	// e.g. WordPress's `RewriteRule ^index\.php$ - [L]`. Emit nothing.
	if sub == "-" {
		return
	}

	if flags.qsa {
		res.warn(ln.num, ln.text, "[QSA] (append query string) isn't represented — the original query string may be dropped; verify the rewrite in the panel")
	}

	nginxPattern, ok := rebasePattern(pattern, st.base)
	if !ok {
		res.warn(ln.num, ln.text, "could not rebase RewriteRule pattern to a full URI")
		return
	}

	replacement := rebaseSubstitution(sub, st.base)
	flag := "last"
	if flags.redirect {
		if flags.redirectCode == 301 {
			flag = "permanent"
		} else {
			flag = "redirect"
		}
	}

	res.addRule(models.NginxRule{
		Type:        "rewrite",
		Pattern:     nginxPattern,
		Replacement: replacement,
		Flag:        flag,
	})
}

// convertRedirect handles mod_alias `Redirect [status] /url-path target`
// (prefix match + append the remainder). RedirectPermanent/RedirectTemp are
// the fixed-status spellings.
func convertRedirect(res *Result, st *state, directive string, args []string, ln line) {
	code := 302
	switch strings.ToLower(directive) {
	case "redirectpermanent":
		code = 301
	case "redirecttemp":
		code = 302
	}
	// `Redirect [status] url-path url`
	if len(args) >= 3 {
		c, ok := parseRedirectStatus(args[0])
		if !ok {
			res.warn(ln.num, ln.text, "unsupported Redirect status (only 301/302/permanent/temp convert)")
			return
		}
		code = c
		args = args[1:]
	}
	if len(args) < 2 {
		res.warn(ln.num, ln.text, "malformed Redirect (need url-path and target)")
		return
	}
	urlPath, target := args[0], args[1]
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	flag := "redirect"
	if code == 301 {
		flag = "permanent"
	}
	// Prefix + append: /old/x -> target/x.
	res.addRule(models.NginxRule{
		Type:        "rewrite",
		Pattern:     "^" + regexEscapePath(urlPath) + "(.*)$",
		Replacement: strings.TrimRight(target, "/") + "$1",
		Flag:        flag,
	})
}

// convertRedirectMatch handles `RedirectMatch [status] regex target` (regex,
// not prefix). Backreferences in target use $1 in both Apache and nginx.
func convertRedirectMatch(res *Result, st *state, args []string, ln line) {
	code := 302
	if len(args) >= 3 {
		c, ok := parseRedirectStatus(args[0])
		if !ok {
			res.warn(ln.num, ln.text, "unsupported RedirectMatch status")
			return
		}
		code = c
		args = args[1:]
	}
	if len(args) < 2 {
		res.warn(ln.num, ln.text, "malformed RedirectMatch (need regex and target)")
		return
	}
	flag := "redirect"
	if code == 301 {
		flag = "permanent"
	}
	res.addRule(models.NginxRule{
		Type:        "rewrite",
		Pattern:     args[0],
		Replacement: args[1],
		Flag:        flag,
	})
}

// convertHeader handles `Header [always] set Name value`. Only the set/add
// form maps to add_header; unset/edit/append have no typed equivalent.
func convertHeader(res *Result, args []string, ln line) {
	i := 0
	always := false
	if i < len(args) && strings.EqualFold(args[i], "always") {
		always = true
		i++
	}
	if i >= len(args) {
		res.warn(ln.num, ln.text, "malformed Header directive")
		return
	}
	op := strings.ToLower(args[i])
	i++
	if op != "set" && op != "add" {
		res.warn(ln.num, ln.text, "only `Header set`/`Header add` convert (unset/edit/append have no typed equivalent)")
		return
	}
	if i+1 >= len(args) {
		res.warn(ln.num, ln.text, "Header set needs a name and value")
		return
	}
	name := args[i]
	if !validHeaderName(name) {
		res.warn(ln.num, ln.text, "header name has unexpected characters — not converted")
		return
	}
	value := strings.Join(args[i+1:], " ")
	alwaysPtr := &always
	res.addRule(models.NginxRule{
		Type:   "custom_header",
		Name:   name,
		Value:  value,
		Always: alwaysPtr,
	})
}

// convertPHP handles php_value/php_flag -> php_setting (fastcgi PHP_VALUE).
// A refused directive is dropped with a warning, never rewritten to some other
// value. A name set more than once keeps only its last value, as Apache does.
func convertPHP(res *Result, directive string, args []string, ln line) {
	if len(args) < 2 {
		res.warn(ln.num, ln.text, "malformed "+directive)
		return
	}
	name, value := args[0], args[1]
	// The FPM pool pins these as php_admin_value (ADR-0023); a fastcgi
	// PHP_VALUE cannot override a php_admin_value, so converting them would
	// silently do nothing. Tell the operator instead of emitting a dead rule.
	if poolPinnedPHP[strings.ToLower(name)] {
		res.warn(ln.num, ln.text, "`"+name+"` is enforced by the per-user FPM pool (php_admin_value) and cannot be changed from a site config — not converted")
		return
	}
	// The panel keeps these admin-only (models.PHPSensitiveDirectives); a site
	// rule must not set them, whatever the value.
	if isSensitivePHP(name) {
		res.warnSec(ln.num, ln.text, "`"+name+"` cannot be set from a site config on this server — not converted")
		return
	}
	// A path into the source control panel's own tree (cPanel's MultiPHP
	// editor writes /var/cpanel/php/sessions/… and /opt/cpanel/ea-php…
	// paths) does not exist here.
	if dir := sourcePanelPath(value); dir != "" {
		res.warn(ln.num, ln.text, "value points into "+dir+", a source control panel path that does not exist here — not converted")
		return
	}
	// cPanel repeats its MultiPHP block once per PHP handler module, so the
	// same name arrives more than once. Apache applies the last one; keep
	// only that.
	for i := len(res.Rules) - 1; i >= 0; i-- {
		if r := res.Rules[i]; r.Type == "php_setting" && strings.EqualFold(r.Name, name) {
			res.Rules = append(res.Rules[:i], res.Rules[i+1:]...)
			res.addNote("`" + name + "` is set more than once; the last value (line " + strconv.Itoa(ln.num) + ") is kept")
		}
	}
	// php_flag uses on/off; PHP_VALUE accepts those literally.
	res.addRule(models.NginxRule{
		Type:  "php_setting",
		Name:  name,
		Value: value,
	})
}

// isSensitivePHP reports whether name is one of models.PHPSensitiveDirectives,
// ignoring case.
func isSensitivePHP(name string) bool {
	for _, d := range models.PHPSensitiveDirectives {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}

// sourcePanelPaths are directory trees that belong to the source control
// panel and never exist on this server.
var sourcePanelPaths = []string{"/var/cpanel/", "/opt/cpanel/", "/usr/local/cpanel/"}

// sourcePanelPath returns the source-panel tree value points into ("" when
// none). A PHP path list (`.:/opt/cpanel/…`) counts if any entry does.
func sourcePanelPath(value string) string {
	for _, p := range sourcePanelPaths {
		if strings.Contains(value, p) {
			return p
		}
	}
	return ""
}

// poolPinnedPHP are settings the FPM pool fixes as php_admin_value, which a
// fastcgi PHP_VALUE cannot override (jabali-php-pool.conf.tmpl).
var poolPinnedPHP = map[string]bool{
	"open_basedir":      true,
	"disable_functions": true,
}

// convertOptions handles the common `Options -Indexes` (already nginx's
// default) and warns on directory-listing enablement and other Options.
func convertOptions(res *Result, args []string, ln line) {
	for _, a := range args {
		switch strings.ToLower(a) {
		case "-indexes":
			res.addNote("`Options -Indexes` is already the nginx default (directory listing is off)")
		case "+indexes", "indexes":
			res.warnSec(ln.num, ln.text, "`Options +Indexes` (directory listing) is intentionally not enabled")
		default:
			// FollowSymLinks, MultiViews, etc. — no nginx equivalent needed
			// or supported; silent-safe to note rather than warn loudly.
			res.warn(ln.num, ln.text, "Options "+a+" has no Rule Builder equivalent")
		}
	}
}

type rewriteFlags struct {
	last         bool
	redirect     bool
	redirectCode int
	forbidden    bool
	gone         bool
	qsa          bool
}

func parseRewriteFlags(toks []string) rewriteFlags {
	var f rewriteFlags
	if len(toks) == 0 {
		return f
	}
	// Flags are a single bracketed, comma-separated token: [R=301,L,NC].
	raw := strings.Join(toks, "")
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		switch {
		case part == "L":
			f.last = true
		case part == "F", part == "forbidden":
			f.forbidden = true
		case part == "G", part == "gone":
			f.gone = true
		case part == "QSA", part == "qsappend":
			f.qsa = true
		case part == "R", strings.HasPrefix(part, "R="), part == "redirect":
			f.redirect = true
			f.redirectCode = 302
			if strings.HasPrefix(part, "R=") {
				if c, err := strconv.Atoi(strings.TrimPrefix(part, "R=")); err == nil {
					f.redirectCode = c
				}
			}
		}
	}
	return f
}

func parseRedirectStatus(s string) (int, bool) {
	switch strings.ToLower(s) {
	case "permanent":
		return 301, true
	case "temp", "302", "seeother":
		return 302, true
	case "301":
		return 301, true
	}
	// Bare numeric codes other than 301/302 aren't representable as a
	// rewrite flag.
	if _, err := strconv.Atoi(s); err == nil {
		return 0, false
	}
	return 0, false
}

// isFrontController reports whether the pending conds + substitution form the
// canonical "route everything not a real file/dir to a front controller"
// idiom that the default `location /` already covers.
func isFrontController(conds []condLine, sub string) bool {
	if len(conds) == 0 {
		return false
	}
	sawNotFile := false
	sawNotDir := false
	for _, c := range conds {
		f := tokenize(c.text)
		// RewriteCond %{REQUEST_FILENAME} !-f   (or !-d)
		if len(f) >= 3 && strings.EqualFold(f[0], "rewritecond") &&
			strings.Contains(f[1], "REQUEST_FILENAME") {
			switch f[2] {
			case "!-f", "!-F":
				sawNotFile = true
				continue
			case "!-d", "!-D":
				sawNotDir = true
				continue
			}
		}
		// Any other condition disqualifies the idiom.
		return false
	}
	if !sawNotFile && !sawNotDir {
		return false
	}
	// Substitution must point at a front controller (index.php-ish), not an
	// arbitrary target.
	s := strings.TrimLeft(sub, "/")
	return strings.HasPrefix(s, "index.php") || s == "index.php" ||
		strings.HasSuffix(s, "/index.php")
}

// rebasePattern converts an Apache RewriteRule pattern (matched relative to
// the .htaccess directory, no leading slash) into a full-URI nginx regex
// anchored under base.
func rebasePattern(pattern, base string) (string, bool) {
	if pattern == "" {
		return "", false
	}
	neg := strings.HasPrefix(pattern, "!")
	if neg {
		// Negated patterns (!^…) have no typed-rule equivalent.
		return "", false
	}
	core := strings.TrimPrefix(pattern, "^")
	core = strings.TrimPrefix(core, "/")
	basePrefix := strings.TrimRight(base, "/") // "" for "/", "/blog" for "/blog/"
	return "^" + basePrefix + "/" + core, true
}

// rebaseSubstitution prepares an Apache substitution for use as an nginx
// rewrite replacement. Absolute URLs pass through; root-relative ("/x") pass
// through; bare relative ("x") is rebased under base.
func rebaseSubstitution(sub, base string) string {
	if strings.HasPrefix(sub, "http://") || strings.HasPrefix(sub, "https://") {
		return sub
	}
	if strings.HasPrefix(sub, "/") {
		return sub
	}
	return strings.TrimRight(base, "/") + "/" + sub
}

// validHeaderName reports whether s is an RFC 7230 header field-name (token
// chars only). Cheap defense-in-depth against directive injection via the
// header name (nginx -t catches the blatant cases; this rejects them early).
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_':
		default:
			return false
		}
	}
	return true
}

// regexEscapePath escapes regex metacharacters in a literal URL path so a
// mod_alias prefix is matched literally.
func regexEscapePath(p string) string {
	var b strings.Builder
	for _, c := range p {
		if strings.ContainsRune(`.+*?()|[]{}^$\`, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}
