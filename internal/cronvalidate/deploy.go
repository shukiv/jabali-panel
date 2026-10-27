package cronvalidate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/google/shlex"
)

// Deploy commands (GH #2277, ADR-0178): what a Git deployment runs in the new
// release before it goes live, one command per line. They follow the cron
// contract, with paths relative to the release instead of absolute ones:
//
//   - php <file> [args...]       a file inside the release (php artisan migrate)
//   - php<X.Y> <file> [args...]  the same on another installed PHP version
//   - wp [args...]               wp-cli; --path, when given, inside the release
//
// No PHP option comes before the file: -n, -c and -d would load PHP without
// PHP Defense, and -r/-R/-B/-E run inline code. Shell metacharacters are
// refused outside quotes, control characters everywhere. Nothing runs through
// a shell: each command is executed as its argv, in the release directory.
// The panel checks the commands when they are saved; the deployment checks
// them again before running them.

const (
	maxDeployCommands     = 20
	maxDeployCommandBytes = 4096
)

// deployPathRE is a path relative to the release.
var deployPathRE = regexp.MustCompile(`^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+)*$`)

// ValidateDeployCommands checks a deployment's commands, one per line. Blank
// lines and lines starting with '#' are skipped; no command at all is valid.
func ValidateDeployCommands(raw string) ([]*Command, error) {
	if len(raw) > maxDeployCommandBytes {
		return nil, &ValidationError{
			Code:   ErrCodeTooLong,
			Detail: fmt.Sprintf("deploy commands exceed %d bytes (%d bytes)", maxDeployCommandBytes, len(raw)),
		}
	}
	cmds := []*Command{}
	for i, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if len(cmds) >= maxDeployCommands {
			return nil, &ValidationError{
				Code:   ErrCodeTooLong,
				Detail: fmt.Sprintf("too many deploy commands (max %d)", maxDeployCommands),
			}
		}
		c, err := validateDeployCommand(trimmed)
		if err != nil {
			if ve, ok := err.(*ValidationError); ok {
				return nil, &ValidationError{Code: ve.Code, Detail: fmt.Sprintf("line %d: %s", i+1, ve.Detail)}
			}
			return nil, err
		}
		cmds = append(cmds, c)
	}
	return cmds, nil
}

// NormalizeDeployCommands is the executable lines of validated deploy
// commands, one per line: what the panel stores and the deployment runs.
func NormalizeDeployCommands(raw string) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, trimmed)
	}
	return strings.Join(lines, "\n")
}

func validateDeployCommand(raw string) (*Command, error) {
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return nil, &ValidationError{Code: ErrCodeMetacharReject, Detail: "command contains control characters"}
		}
	}
	if hasUnquotedMetachar(raw) {
		return nil, &ValidationError{
			Code:   ErrCodeMetacharReject,
			Detail: "command contains shell metacharacters; & | ; $ ` ( ) < > \\ { } * ? are allowed only inside quotes",
		}
	}
	argv, err := shlex.Split(raw)
	if err != nil {
		return nil, &ValidationError{Code: ErrCodeMetacharReject, Detail: fmt.Sprintf("failed to parse command: %v", err)}
	}
	if len(argv) == 0 {
		return nil, &ValidationError{Code: ErrCodeEmpty, Detail: "command parsed to empty argv"}
	}
	switch bin := argv[0]; {
	case bin == "php" || isVersionedPHP(bin):
		return validateDeployPHP(argv)
	case bin == "wp":
		return validateDeployWP(argv)
	default:
		return nil, &ValidationError{
			Code:   ErrCodeBinaryNotAllowed,
			Detail: fmt.Sprintf("a deploy command starts with 'php', 'php<X.Y>' or 'wp', got %q", bin),
		}
	}
}

func validateDeployPHP(argv []string) (*Command, error) {
	if len(argv) < 2 {
		return nil, &ValidationError{Code: ErrCodeBadPathArg, Detail: "php needs a file to run, e.g. php artisan migrate --force"}
	}
	if strings.HasPrefix(argv[1], "-") {
		return nil, &ValidationError{
			Code:   ErrCodeBinaryNotAllowed,
			Detail: fmt.Sprintf("no PHP option may come before the file (got %q): name the file first, e.g. php artisan migrate --force", argv[1]),
		}
	}
	if err := validateDeployPath(argv[1]); err != nil {
		return nil, err
	}
	return &Command{Argv: argv}, nil
}

func validateDeployWP(argv []string) (*Command, error) {
	for i, a := range argv[1:] {
		var p string
		switch {
		case a == "--path":
			if i+2 >= len(argv) {
				return nil, &ValidationError{Code: ErrCodeBadPathArg, Detail: "--path needs a value"}
			}
			p = argv[i+2]
		case strings.HasPrefix(a, "--path="):
			p = strings.TrimPrefix(a, "--path=")
		default:
			continue
		}
		if err := validateDeployPath(p); err != nil {
			return nil, err
		}
	}
	return &Command{Argv: argv}, nil
}

// validateDeployPath checks a path relative to the release: no leading '/',
// no '.' or '..' segment.
func validateDeployPath(p string) error {
	if strings.HasPrefix(p, "/") {
		return &ValidationError{Code: ErrCodeBadPathArg, Detail: fmt.Sprintf("%q: a path in a deploy command is relative to the release, not absolute", p)}
	}
	if len(p) > 255 || !deployPathRE.MatchString(p) {
		return &ValidationError{Code: ErrCodeBadPathArg, Detail: fmt.Sprintf("%q: a path may hold only letters, digits and . _ + - /", p)}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return &ValidationError{Code: ErrCodeBadPathArg, Detail: fmt.Sprintf("%q: a path may not contain . or .. segments", p)}
		}
	}
	return nil
}

// MaxDeploySharedPaths bounds a deployment's shared paths.
const MaxDeploySharedPaths = 20

// deployLayoutPathRE is a web directory or shared path, relative to the
// repository.
var deployLayoutPathRE = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// ValidateDeployLayout checks a deployment's web directory ("" is the
// repository's root) and shared paths, both relative to the repository. A
// shared path inside the web directory may not have a hidden segment: the
// site's vhost serves dotfiles, so a shared .env there would be public. The
// panel checks them when they are saved; the deployment checks them again.
func ValidateDeployLayout(webDir string, shared []string) error {
	if webDir != "" {
		if err := validateDeployLayoutPath(webDir); err != nil {
			return fmt.Errorf("web directory: %w", err)
		}
	}
	if len(shared) > MaxDeploySharedPaths {
		return fmt.Errorf("at most %d shared paths", MaxDeploySharedPaths)
	}
	for i, p := range shared {
		if err := validateDeployLayoutPath(p); err != nil {
			return fmt.Errorf("shared path %q: %w", p, err)
		}
		if p == ".git" || strings.HasPrefix(p, ".git/") {
			return fmt.Errorf("shared path %q: .git can't be shared", p)
		}
		if webDir != "" && (p == webDir || strings.HasPrefix(webDir, p+"/")) {
			return fmt.Errorf("shared path %q: the web directory can't be shared", p)
		}
		if webDir == "" || strings.HasPrefix(p, webDir+"/") {
			for _, seg := range strings.Split(p, "/") {
				if strings.HasPrefix(seg, ".") {
					return fmt.Errorf("shared path %q is inside the web directory, where a hidden file would be public: keep it outside the web directory", p)
				}
			}
		}
		for _, q := range shared[:i] {
			if p == q || strings.HasPrefix(p, q+"/") || strings.HasPrefix(q, p+"/") {
				return fmt.Errorf("shared paths %q and %q overlap", q, p)
			}
		}
	}
	return nil
}

func validateDeployLayoutPath(p string) error {
	if len(p) > 255 || !deployLayoutPathRE.MatchString(p) {
		return fmt.Errorf("a path relative to the repository, of letters, digits, '.', '_', '-' and '/'")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("no . or .. segments")
		}
	}
	return nil
}
