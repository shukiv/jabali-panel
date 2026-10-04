// Package frontcontroller is the grammar of the GH #1999 "front controller"
// nginx rule: the PHP script, and the query string passed to it, that a
// domain's `location /` falls back to when a request matches no file or
// folder:
//
//	location / { try_files $uri $uri/ <script>?<query>; }
//
// Existing files and folders are still served first, so static assets keep
// working, unlike a catch-all rewrite. The panel validates a rule with this
// grammar when it is saved, and the agent validates the rendered fallback again
// before writing the vhost, so a value outside it never reaches nginx. The
// grammar keeps the fallback a single nginx token: no whitespace, `;`, braces,
// quotes or comment marker, and only the nginx variables listed in Variables.
package frontcontroller

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DefaultScript and DefaultQuery are what every PHP vhost falls back to
// without a front controller rule.
const (
	DefaultScript = "/index.php"
	DefaultQuery  = "$query_string"
)

// Default is the fallback a PHP vhost renders without a rule.
var Default = Fallback(DefaultScript, DefaultQuery)

// Variables are the nginx variables a query may use, without the `$`.
var Variables = []string{"args", "document_uri", "is_args", "query_string", "request_uri", "uri"}

const (
	maxScriptLen = 200
	maxQueryLen  = 256
)

// scriptRE is a .php path from the docroot. A visitor can already request any
// such path directly, so routing unknown paths to it adds no reach.
var scriptRE = regexp.MustCompile(`^/[A-Za-z0-9_.\-/]*\.php$`)

// ValidateScript checks the script path.
func ValidateScript(s string) error {
	if s == "" {
		return errors.New("script is required (e.g. /index.php)")
	}
	if len(s) > maxScriptLen {
		return fmt.Errorf("script is longer than %d characters", maxScriptLen)
	}
	if !scriptRE.MatchString(s) {
		return errors.New("script must be a path from the site root ending in .php, using letters, digits and _ . - / only (e.g. /index.php)")
	}
	if strings.Contains(s, "..") || strings.Contains(s, "//") {
		return errors.New("script may not contain .. or //")
	}
	return nil
}

func isLiteral(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("_.-=&/%", c) >= 0
}

func isNameChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// ValidateQuery checks the query string template. It may be empty (the script
// gets no query string). Variables are written $name and must be one of
// Variables; the name ends at the first character that cannot be part of a
// variable name, so "$urix" is the unknown variable urix, not $uri plus "x".
func ValidateQuery(q string) error {
	if len(q) > maxQueryLen {
		return fmt.Errorf("query is longer than %d characters", maxQueryLen)
	}
	for i := 0; i < len(q); {
		c := q[i]
		if c == '$' {
			j := i + 1
			for j < len(q) && isNameChar(q[j]) {
				j++
			}
			name := q[i+1 : j]
			if !knownVariable(name) {
				if name == "" {
					return errors.New("query has a $ with no variable name after it")
				}
				return fmt.Errorf("query uses $%s; allowed variables are %s", name, variableList())
			}
			i = j
			continue
		}
		if !isLiteral(c) {
			return fmt.Errorf("query may contain only letters, digits, _ . - = & / %% and the variables %s", variableList())
		}
		i++
	}
	return nil
}

func knownVariable(name string) bool {
	for _, v := range Variables {
		if v == name {
			return true
		}
	}
	return false
}

func variableList() string {
	out := make([]string, len(Variables))
	for i, v := range Variables {
		out[i] = "$" + v
	}
	return strings.Join(out, " ")
}

// Validate checks a script and query pair.
func Validate(script, query string) error {
	if err := ValidateScript(script); err != nil {
		return err
	}
	return ValidateQuery(query)
}

// Fallback is the try_files fallback for a script and query: "script?query",
// or the script alone when the query is empty.
func Fallback(script, query string) string {
	if query == "" {
		return script
	}
	return script + "?" + query
}

// Parse splits a rendered fallback back into script and query and validates
// both. The agent uses it at its trust boundary.
func Parse(fallback string) (script, query string, err error) {
	script, query, _ = strings.Cut(fallback, "?")
	if err := Validate(script, query); err != nil {
		return "", "", err
	}
	return script, query, nil
}
