// Package cronvalidate provides secure parsing and validation of user-submitted
// cron commands and schedules. This is the shared contract between the API
// handler (pre-accept gate) and the agent (defense-in-depth before render).
//
// Design principles:
//   - Reject on ANY unescaped shell metacharacter before parsing (metachar defense).
//   - Use pure-Go tokenizer (github.com/google/shlex) for shell-aware parsing.
//   - Return parsed argv slice so caller feeds directly to systemd ExecStart.
//   - Allow-list is closed-set: wp, php, and versioned php<X.Y> only, with
//     strict path validation.
//   - No subprocess calls; all validation is in-process.
package cronvalidate

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/google/shlex"
	"github.com/robfig/cron/v3"
)

// Error codes for structured error reporting (suitable for API "error" field).
const (
	ErrCodeEmpty               = "empty"
	ErrCodeTooLong             = "too_long"           // >1024 bytes
	ErrCodeBinaryNotAllowed    = "binary_not_allowed" // not wp/php/php<X.Y>
	ErrCodeMetacharReject      = "metachar_reject"    // shell metacharacters
	ErrCodeBadPathArg          = "bad_path_arg"       // --path= missing, not absolute, traversal, or not in owned docroot
	ErrCodeBadScheduleSyntax   = "bad_schedule_syntax"
	ErrCodeScheduleTooFrequent = "schedule_too_frequent" // < 1 min step
	ErrCodeInvalidName         = "invalid_name"          // control characters or invalid name
)

// ValidationError is the error type returned by validators.
type ValidationError struct {
	Code   string // One of ErrCode* constants
	Detail string // Human-readable explanation
}

func (e *ValidationError) Error() string {
	return e.Code + ": " + e.Detail
}

// Command represents a validated, parsed command ready for systemd ExecStart rendering.
type Command struct {
	// Argv is the fully-parsed argv slice, ready to be single-quoted and
	// emitted as ExecStart= in a systemd unit file.
	Argv []string

	// Kind distinguishes the validated command shape. Empty ("") is the
	// default wp/php exec form (ValidateCommand). KindHTTPTrigger is a
	// constrained curl/wget self-domain HTTP ping (ValidateHTTPTrigger),
	// whose Argv is rewritten to invoke the rebind-safe wrapper
	// `jabali cron http-trigger <url>` instead of the raw curl/wget.
	Kind string

	// URL is the validated http(s) target, set only for Kind==KindHTTPTrigger.
	URL string
}

// KindHTTPTrigger marks a Command produced by ValidateHTTPTrigger.
const KindHTTPTrigger = "http_trigger"

// metacharSet is the set of bytes that must be rejected in the raw command
// string unless they appear inside matched quotes. These are shell metacharacters
// that could enable injection if present unquoted.
var metacharSet = map[byte]bool{
	'&':    true, // background / AND
	'|':    true, // pipe / OR
	';':    true, // statement separator
	'$':    true, // variable expansion
	'`':    true, // backtick substitution
	'(':    true, // subshell
	')':    true,
	'<':    true, // input redirection
	'>':    true, // output redirection
	'\\':   true, // escape
	'\n':   true, // newline
	'\x00': true, // NUL byte
	'{':    true, // brace expansion
	'}':    true,
	'*':    true, // glob (allowed inside quotes; will check below)
	'?':    true, // glob (allowed inside quotes; will check below)
}

// hasUnquotedMetachar scans s for any metachar outside balanced quotes.
// Returns true if a forbidden metachar is found outside quotes.
// Single quotes and double quotes are recognized; content inside is skipped.
func hasUnquotedMetachar(s string) bool {
	for i := 0; i < len(s); i++ {
		ch := s[i]

		// Skip single-quoted strings: consume until next unescaped '
		if ch == '\'' {
			i++
			for i < len(s) && s[i] != '\'' {
				i++
			}
			// i is now at closing ' or past end; next iteration increments again
			continue
		}

		// Skip double-quoted strings: consume until next unescaped "
		if ch == '"' {
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++ // skip escaped char
				}
				i++
			}
			continue
		}

		// Outside quotes: check for metacharacters
		if metacharSet[ch] {
			return true
		}
	}
	return false
}

// ValidateCommand parses and validates a user-submitted command string.
// The command must be either:
//   - wp <subcommand> --path=<abs-docroot> [args...]
//   - php <abs-docroot>/<file>.php [args...]
//
// Both forms require the absolute path to resolve within an owned docroot.
// All shell metacharacters are rejected unless inside quotes (and glob chars
// are still rejected in pre-shlex scan). Returns a Command with parsed argv
// on success, or a ValidationError with a code suitable for API responses.
func ValidateCommand(raw string, ownedDocroots []string, ownedHome string) (*Command, error) {
	// Empty or whitespace-only
	if strings.TrimSpace(raw) == "" {
		return nil, &ValidationError{
			Code:   ErrCodeEmpty,
			Detail: "command cannot be empty",
		}
	}

	// Too long (guard against resource exhaustion)
	if len(raw) > 1024 {
		return nil, &ValidationError{
			Code:   ErrCodeTooLong,
			Detail: fmt.Sprintf("command exceeds 1024 bytes (%d bytes)", len(raw)),
		}
	}

	// Reject ALL control characters (newline, CR, NUL, etc.) outright —
	// INCLUDING inside quotes. hasUnquotedMetachar deliberately skips quoted
	// spans, but a newline inside a quoted arg survives shlex.Split as a token,
	// and the agent emits each token single-quoted into a systemd ExecStart=.
	// systemd parses unit files LINE BY LINE (not shell-style), so a literal
	// newline breaks out of ExecStart= and injects attacker-controlled unit
	// directives — letting a tenant bypass the wp/php allowlist and run
	// arbitrary commands as their own uid (outside the SSH sandbox) via the
	// generated user timer. Control chars have no legitimate place in a cron
	// command, so reject them at the source. (\t is allowed: it cannot break
	// a unit line.)
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c < 0x20 && c != '\t' {
			return nil, &ValidationError{
				Code:   ErrCodeMetacharReject,
				Detail: "command contains control characters (newline/CR/NUL/etc.) which are not allowed",
			}
		}
	}

	// PRIMARY DEFENSE: reject unquoted metacharacters before parsing.
	if hasUnquotedMetachar(raw) {
		return nil, &ValidationError{
			Code: ErrCodeMetacharReject,
			Detail: "command contains shell metacharacters; " +
				"allowed: & | ; $ ` ( ) < > \\ newline NUL { } * ? only inside single/double quotes",
		}
	}

	// Parse with shlex. If shlex itself fails (e.g., unclosed quote), reject.
	argv, err := shlex.Split(raw)
	if err != nil {
		return nil, &ValidationError{
			Code:   ErrCodeMetacharReject,
			Detail: fmt.Sprintf("failed to parse command: %v", err),
		}
	}

	if len(argv) == 0 {
		return nil, &ValidationError{
			Code:   ErrCodeEmpty,
			Detail: "command parsed to empty argv",
		}
	}

	// First token must be wp, php, or a versioned php<X.Y> (or full paths).
	// A versioned interpreter (e.g. `php8.5 script.php`) lets a cron job pin a
	// specific PHP runtime instead of the user's default bare `php`. The
	// per-user .jabali/bin/php<X.Y> wrappers (GH #256) already resolve these
	// on the cron PATH, so the same arg rules as `php` apply (GH #299).
	binary := argv[0]
	switch {
	case binary == "wp":
		return validateWPCommand(argv, ownedDocroots)
	case binary == "php" || strings.HasSuffix(binary, "/php"):
		return validatePHPCommand(argv, ownedDocroots)
	case isVersionedPHP(binary):
		// Versioned interpreter is BARE only (php8.5, not a path): selecting an
		// installed version never needs a path, and isVersionedPHP requires the
		// token to start with "php", so a slash-bearing path never matches.
		return validatePHPCommand(argv, ownedDocroots)
	case isPythonBinary(binary):
		// GH #1435: python / python3 / python<X.Y> (bare) or an absolute path to
		// one (a virtualenv's bin/python, a pyenv build). Must run an absolute
		// .py file the tenant owns — no `-c`/`-m` arbitrary code.
		return validatePythonCommand(argv, scriptRoots(ownedDocroots, ownedHome))
	case isNodeBinary(binary):
		// GH #1435: node / nodejs (bare) or an absolute path to one. Must run an
		// absolute .js/.mjs/.cjs file the tenant owns — no `-e`/`-p` inline code.
		return validateNodeCommand(argv, scriptRoots(ownedDocroots, ownedHome))
	default:
		return nil, &ValidationError{
			Code: ErrCodeBinaryNotAllowed,
			Detail: fmt.Sprintf(
				"first token must be 'wp', 'php', 'php<X.Y>', 'python[3][.Y]', or 'node', got %q",
				binary,
			),
		}
	}
}

// scriptRoots is the containment set for python/node scripts: the account's
// docroots PLUS its home directory. Unlike wp/php (docroot-only), a Python or
// Node project's entry file (manage.py, a script) usually lives under the home
// but outside any web docroot, so the home is the right ownership boundary.
// ownedHome may be "" (e.g. a context with no resolved account) — then only the
// docroots apply.
func scriptRoots(ownedDocroots []string, ownedHome string) []string {
	if ownedHome == "" {
		return ownedDocroots
	}
	roots := make([]string, 0, len(ownedDocroots)+1)
	roots = append(roots, ownedDocroots...)
	roots = append(roots, ownedHome)
	return roots
}

// isVersionedPHP reports whether name is a versioned PHP CLI binary like
// "php8.5" (php<MAJOR>.<MINOR>, digits only). Used so a cron command can pin a
// specific interpreter; the per-user .jabali/bin/php<X.Y> wrappers (GH #256,
// cPanel ea-phpNN style) resolve these on the cron PATH.
func isVersionedPHP(name string) bool {
	rest, ok := strings.CutPrefix(name, "php")
	if !ok {
		return false
	}
	dot := strings.IndexByte(rest, '.')
	if dot <= 0 || dot == len(rest)-1 {
		return false
	}
	return isAllDigits(rest[:dot]) && isAllDigits(rest[dot+1:])
}

// isAllDigits reports whether s is non-empty and all ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// baseName returns the last path element of name (the interpreter basename),
// so an absolute interpreter path (a virtualenv's bin/python) is matched by the
// same rules as a bare name.
func baseName(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// isPythonBinary reports whether name is an allowed Python interpreter:
// bare `python`, `python3`, or versioned `python<X>`/`python<X.Y>` (e.g.
// python3.11), or an absolute path whose basename is one of those (a
// virtualenv or pyenv interpreter — GH #1435). This mirrors php's acceptance
// of both a bare name and an absolute `/…/php` path.
func isPythonBinary(name string) bool {
	base := baseName(name)
	return base == "python" || isVersionedPython(base)
}

// isVersionedPython reports whether name is `python<version>` where version is
// digits with at most one dot (python3, python3.11, python2.7). Bare "python"
// is handled separately by isPythonBinary.
func isVersionedPython(name string) bool {
	rest, ok := strings.CutPrefix(name, "python")
	if !ok || rest == "" {
		return false
	}
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		return dot > 0 && dot < len(rest)-1 && isAllDigits(rest[:dot]) && isAllDigits(rest[dot+1:])
	}
	return isAllDigits(rest)
}

// isNodeBinary reports whether name is `node` or `nodejs`, bare or as an
// absolute path's basename (GH #1435).
func isNodeBinary(name string) bool {
	base := baseName(name)
	return base == "node" || base == "nodejs"
}

// pythonStandaloneFlags are introspection-only Python flags that need no script.
var pythonStandaloneFlags = map[string]bool{
	"-V": true, "--version": true,
	"-h": true, "--help": true,
}

// isInlineCodePythonFlag reports whether a token makes Python run code that is
// not an owned script file: `-c` (run code string), `-m` (run an installed
// module, not a file), and `-` (read the program from stdin). Glued forms like
// `-cCODE` are covered by the prefix check. Blocking these keeps a Python cron
// to "an absolute .py file the tenant owns", the same model as php (GH #1435).
func isInlineCodePythonFlag(a string) bool {
	return a == "-" || a == "-c" || strings.HasPrefix(a, "-c") ||
		a == "-m" || strings.HasPrefix(a, "-m")
}

// validatePythonCommand requires an absolute .py file inside the account's
// docroots or home, and rejects inline-code / module-exec flags.
func validatePythonCommand(argv []string, scriptRoots []string) (*Command, error) {
	if len(argv) < 2 {
		return nil, &ValidationError{
			Code:   ErrCodeBadPathArg,
			Detail: "python command requires an argument (an absolute .py file)",
		}
	}
	for _, a := range argv[1:] {
		if isInlineCodePythonFlag(a) {
			return nil, &ValidationError{
				Code:   ErrCodeBinaryNotAllowed,
				Detail: "python inline-code flags (-c/-m/-) are not allowed; a cron command must run an absolute .py file inside your account",
			}
		}
	}
	if pythonStandaloneFlags[argv[1]] {
		return &Command{Argv: argv}, nil
	}
	pathArg, ok := firstNonFlagArg(argv)
	if !ok {
		return &Command{Argv: argv}, nil
	}
	if err := validateScriptFile(pathArg, ".py", "python", scriptRoots); err != nil {
		return nil, err
	}
	return &Command{Argv: argv}, nil
}

// nodeStandaloneFlags are introspection-only Node flags that need no script.
var nodeStandaloneFlags = map[string]bool{
	"-v": true, "--version": true,
	"-h": true, "--help": true,
}

// isInlineCodeNodeFlag reports whether a token makes Node run inline code
// instead of a script file: `-e`/`--eval` (evaluate) and `-p`/`--print`
// (evaluate + print), including glued `-eCODE` (GH #1435).
func isInlineCodeNodeFlag(a string) bool {
	for _, f := range []string{"-e", "--eval", "-p", "--print"} {
		if a == f || strings.HasPrefix(a, f) {
			return true
		}
	}
	return false
}

var nodeScriptExts = []string{".js", ".mjs", ".cjs"}

// validateNodeCommand requires an absolute .js/.mjs/.cjs file inside the
// account's docroots or home, and rejects inline-code flags.
func validateNodeCommand(argv []string, scriptRoots []string) (*Command, error) {
	if len(argv) < 2 {
		return nil, &ValidationError{
			Code:   ErrCodeBadPathArg,
			Detail: "node command requires an argument (an absolute .js file)",
		}
	}
	for _, a := range argv[1:] {
		if isInlineCodeNodeFlag(a) {
			return nil, &ValidationError{
				Code:   ErrCodeBinaryNotAllowed,
				Detail: "node inline-code flags (-e/--eval/-p/--print) are not allowed; a cron command must run an absolute .js file inside your account",
			}
		}
	}
	if nodeStandaloneFlags[argv[1]] {
		return &Command{Argv: argv}, nil
	}
	pathArg, ok := firstNonFlagArg(argv)
	if !ok {
		return &Command{Argv: argv}, nil
	}
	if err := validateScriptFileMultiExt(pathArg, nodeScriptExts, "node", scriptRoots); err != nil {
		return nil, err
	}
	return &Command{Argv: argv}, nil
}

// firstNonFlagArg returns the first argv element after argv[0] that is not a
// -flag, and whether one was found.
func firstNonFlagArg(argv []string) (string, bool) {
	for i := 1; i < len(argv); i++ {
		if strings.HasPrefix(argv[i], "-") {
			continue
		}
		return argv[i], true
	}
	return "", false
}

// validateScriptFile checks pathArg is an absolute path with the given
// extension, inside one of scriptRoots. label names the interpreter for errors.
func validateScriptFile(pathArg, ext, label string, scriptRoots []string) error {
	return validateScriptFileMultiExt(pathArg, []string{ext}, label, scriptRoots)
}

func validateScriptFileMultiExt(pathArg string, exts []string, label string, scriptRoots []string) error {
	if !filepath.IsAbs(pathArg) {
		return &ValidationError{
			Code:   ErrCodeBadPathArg,
			Detail: fmt.Sprintf("%s script path must be absolute, got %q", label, pathArg),
		}
	}
	okExt := false
	for _, e := range exts {
		if strings.HasSuffix(pathArg, e) {
			okExt = true
			break
		}
	}
	if !okExt {
		return &ValidationError{
			Code:   ErrCodeBadPathArg,
			Detail: fmt.Sprintf("%s script path must end in %s, got %q", label, strings.Join(exts, "/"), pathArg),
		}
	}
	return validatePathArg(pathArg, scriptRoots)
}

// validateWPCommand checks a wp command has required --path=<docroot> argument.
func validateWPCommand(argv []string, ownedDocroots []string) (*Command, error) {
	// Find --path= argument (may be single token or two: --path, <path>)
	var pathArg string

	for i, arg := range argv {
		if arg == "--path" && i+1 < len(argv) {
			// Two-token form: --path <path>
			pathArg = argv[i+1]
			break
		} else if strings.HasPrefix(arg, "--path=") {
			// Single-token form: --path=<path>
			pathArg = arg[7:] // skip "--path="
			break
		}
	}

	if pathArg == "" {
		return nil, &ValidationError{
			Code:   ErrCodeBadPathArg,
			Detail: "wp command requires --path=<abs-docroot> or --path <abs-docroot>",
		}
	}

	if err := validatePathArg(pathArg, ownedDocroots); err != nil {
		return nil, err
	}

	return &Command{Argv: argv}, nil
}

// validatePHPCommand checks the first argument is an absolute path ending in .php
// within an owned docroot.

// Common PHP CLI flags that don't require file arguments
var phpStandaloneFlags = map[string]bool{
	"-v": true, "--version": true,
	"-m": true, "--modules": true,
	"-i": true, "--info": true,
	"-h": true, "--help": true,
}

// isInlineCodePHPFlag reports whether a PHP CLI token executes inline code
// rather than naming a script file: -r/-R (run code), -B/-E (begin/end code),
// in both separated (`-r`) and glued (`-rCODE`) forms (GH #440).
func isInlineCodePHPFlag(a string) bool {
	for _, f := range []string{"-r", "-R", "-B", "-E"} {
		if a == f || strings.HasPrefix(a, f) {
			return true
		}
	}
	return false
}

func validatePHPCommand(argv []string, ownedDocroots []string) (*Command, error) {
	if len(argv) < 2 {
		return nil, &ValidationError{
			Code:   ErrCodeBadPathArg,
			Detail: "php command requires an argument",
		}
	}

	// Reject inline-code PHP flags anywhere in the command (GH #440): -r/-R
	// (run code), -B/-E (begin/end code), including glued forms like
	// `-rCODE`. These execute arbitrary PHP that never lives in the tenant's
	// docroot and never passes the owned-path check, bypassing the cron
	// command model (allowlisted binary + absolute .php in an owned docroot).
	for _, a := range argv[1:] {
		if isInlineCodePHPFlag(a) {
			return nil, &ValidationError{
				Code:   ErrCodeBinaryNotAllowed,
				Detail: "php inline-code flags (-r/-R/-B/-E) are not allowed; a cron command must run an absolute .php file inside your docroot",
			}
		}
	}

	// Check for harmless standalone flags (introspection only: -v/-m/-i/-h)
	if phpStandaloneFlags[argv[1]] {
		return &Command{Argv: argv}, nil
	}

	// Find the script path - skip over flags
	var pathArg string
	pathArgIndex := -1
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		// Skip known flags
		if strings.HasPrefix(arg, "-") {
			continue
		}
		pathArg = arg
		pathArgIndex = i
		break
	}

	// If no path argument found, might be all flags
	if pathArgIndex == -1 {
		return &Command{Argv: argv}, nil
	}

	// Must be absolute path
	if !filepath.IsAbs(pathArg) {
		return nil, &ValidationError{
			Code: ErrCodeBadPathArg,
			Detail: fmt.Sprintf(
				"php path must be absolute, got %q",
				pathArg,
			),
		}
	}

	// Must end in .php
	if !strings.HasSuffix(pathArg, ".php") {
		return nil, &ValidationError{
			Code: ErrCodeBadPathArg,
			Detail: fmt.Sprintf(
				"php path must end in .php, got %q",
				pathArg,
			),
		}
	}

	if err := validatePathArg(pathArg, ownedDocroots); err != nil {
		return nil, err
	}

	return &Command{Argv: argv}, nil
}

// validatePathArg validates that an absolute path:
//  1. Has no .. tokens
//  2. Is inside one of ownedDocroots (with / boundary check)
//  3. If it exists, verifies via EvalSymlinks; if not, that's OK (will be checked at runtime by cron-precheck)
func validatePathArg(pathStr string, ownedDocroots []string) error {
	// Belt-and-suspenders: reject .. anywhere in original string
	if strings.Contains(pathStr, "..") {
		return &ValidationError{
			Code:   ErrCodeBadPathArg,
			Detail: fmt.Sprintf("path contains '..': %q", pathStr),
		}
	}

	// Attempt to resolve symlinks if the path exists. If it doesn't exist, that's
	// OK for API validation (the cron-precheck helper in step 4 will verify at exec time).
	// If EvalSymlinks succeeds, we use the real path; otherwise we use the cleaned path.
	realPath := pathStr
	if resolved, err := filepath.EvalSymlinks(pathStr); err == nil {
		realPath = resolved
	}
	// If EvalSymlinks fails, we proceed with the cleaned path for containment check.
	realPath = filepath.Clean(realPath)

	// Ensure path is absolute
	if !filepath.IsAbs(realPath) {
		return &ValidationError{
			Code: ErrCodeBadPathArg,
			Detail: fmt.Sprintf(
				"path is not absolute: %q",
				pathStr,
			),
		}
	}

	// Ensure path is within one of the owned docroots (with / boundary check)
	found := false
	for _, docroot := range ownedDocroots {
		// Normalize both for comparison
		docroot = filepath.Clean(docroot)

		// Check if realPath is docroot itself or a descendant
		// We must ensure word-boundary: /home/shuki/x should NOT match /home/shukimalicious/x
		if realPath == docroot {
			found = true
			break
		}
		if strings.HasPrefix(realPath, docroot+"/") {
			found = true
			break
		}
	}

	if !found {
		return &ValidationError{
			Code: ErrCodeBadPathArg,
			Detail: fmt.Sprintf(
				"path %q is not inside owned docroots: %v",
				realPath,
				ownedDocroots,
			),
		}
	}

	return nil
}

// ValidateSchedule validates a 5-field POSIX cron expression.
// Rejects shortcuts (@hourly, @reboot, @every, etc.) and requires exactly 5 fields.
// Empty or whitespace-only expressions are rejected.
func ValidateSchedule(expr string) error {
	expr = strings.TrimSpace(expr)

	// Reject empty
	if expr == "" {
		return &ValidationError{
			Code:   ErrCodeBadScheduleSyntax,
			Detail: "schedule expression cannot be empty",
		}
	}

	// Reject shortcuts (start with @)
	if strings.HasPrefix(expr, "@") {
		return &ValidationError{
			Code: ErrCodeBadScheduleSyntax,
			Detail: fmt.Sprintf(
				"schedule shortcuts (@hourly, @daily, @reboot, @every) not allowed; " +
					"use 5-field cron syntax (e.g. '0 * * * *' for hourly)",
			),
		}
	}

	// Parse with robfig/cron using only 5 fields (no seconds, no special shortcuts)
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	_, err := parser.Parse(expr)
	if err != nil {
		return &ValidationError{
			Code: ErrCodeBadScheduleSyntax,
			Detail: fmt.Sprintf(
				"invalid cron syntax: %v (expected 5-field POSIX format)",
				err,
			),
		}
	}

	// TODO(v2): supplementary systemd-analyze calendar subprocess call as argv array
	// For v1 we rely on robfig/cron alone, which uses the same grammar as systemd's
	// OnCalendar= parsing and is pure-Go and injection-proof.

	return nil
}

// ValidateCronName validates a cron job name to prevent control character injection.
// Control characters could corrupt systemd unit files or logging output.
// Returns nil on valid names, or ValidationError with ErrCodeInvalidName.
func ValidateCronName(name string) error {
	if len(name) == 0 {
		return &ValidationError{
			Code:   ErrCodeEmpty,
			Detail: "cron name cannot be empty",
		}
	}

	if len(name) > 100 {
		return &ValidationError{
			Code:   ErrCodeTooLong,
			Detail: fmt.Sprintf("cron name exceeds 100 character limit (%d characters)", len(name)),
		}
	}

	// Reject all Unicode control characters (U+0000–U+001F, U+007F–U+009F)
	// These could corrupt systemd unit files, logging output, or shell parsing
	for _, r := range name {
		if unicode.IsControl(r) {
			return &ValidationError{
				Code:   ErrCodeInvalidName,
				Detail: "cron name contains invalid control characters",
			}
		}
	}

	return nil
}
