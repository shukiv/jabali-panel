// Package phpenv validates and renders per-domain environment variables
// (GH #1332 item 14). These become nginx fastcgi_param directives in the PHP
// location, so key validation is a SECURITY boundary: a fastcgi_param named
// PHP_ADMIN_VALUE overrides the FPM pool's php_admin_value and can clear
// disable_functions / open_basedir (box-drilled). Both panel-api (request
// validation) and panel-agent (defense-in-depth + rendering) use this package
// so the denylist has a single source of truth.
package phpenv

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxVars caps the number of env vars per domain; MaxValueLen caps a value's
// length. Generous but bounded so the vhost can't be blown up.
const (
	MaxVars     = 100
	MaxValueLen = 4096
)

// keyRE bounds a key to a conventional env-var identifier.
var keyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// denyKeys are keys that must never be delivered as fastcgi_param. The first
// three are the sandbox-escape vector (they override php_admin_value); the rest
// are CGI/meta variables that route the request or PHP relies on — letting a
// tenant set them breaks the app or worse.
var denyKeys = map[string]bool{
	"PHP_VALUE":            true,
	"PHP_ADMIN_VALUE":      true,
	"PHP_ADMIN_FLAG":       true,
	"SCRIPT_FILENAME":      true,
	"SCRIPT_NAME":          true,
	"DOCUMENT_ROOT":        true,
	"DOCUMENT_URI":         true,
	"REQUEST_URI":          true,
	"REQUEST_METHOD":       true,
	"REQUEST_SCHEME":       true,
	"QUERY_STRING":         true,
	"CONTENT_TYPE":         true,
	"CONTENT_LENGTH":       true,
	"PATH_INFO":            true,
	"PATH_TRANSLATED":      true,
	"GATEWAY_INTERFACE":    true,
	"SERVER_PROTOCOL":      true,
	"SERVER_SOFTWARE":      true,
	"SERVER_NAME":          true,
	"SERVER_ADDR":          true,
	"SERVER_PORT":          true,
	"REMOTE_ADDR":          true,
	"REMOTE_PORT":          true,
	"REMOTE_USER":          true,
	"HTTPS":                true,
	"REDIRECT_STATUS":      true,
	"FCGI_ROLE":            true,
	"ORIG_SCRIPT_FILENAME": true,
	"ORIG_SCRIPT_NAME":     true,
	"ORIG_PATH_INFO":       true,
	"ORIG_PATH_TRANSLATED": true,
}

// ValidKey reports whether key is a safe env-var name to deliver as a
// fastcgi_param. Case-insensitive against the denylist; HTTP_-prefixed keys are
// rejected (they masquerade as client request headers).
func ValidKey(key string) error {
	if !keyRE.MatchString(key) {
		return fmt.Errorf("invalid env var name %q (must match [A-Za-z_][A-Za-z0-9_]*, <=64 chars)", key)
	}
	up := strings.ToUpper(key)
	if denyKeys[up] {
		return fmt.Errorf("env var name %q is reserved and cannot be set", key)
	}
	if strings.HasPrefix(up, "HTTP_") {
		return fmt.Errorf("env var name %q may not start with HTTP_ (collides with request headers)", key)
	}
	return nil
}

// ValidValue reports whether value is safe to render into the double-quoted
// nginx fastcgi_param string. Control characters could break out of the line;
// the caller must still Escape() the value when rendering.
func ValidValue(value string) error {
	if len(value) > MaxValueLen {
		return fmt.Errorf("env var value too long (max %d bytes)", MaxValueLen)
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return fmt.Errorf("env var value contains control characters")
	}
	return nil
}

// Escape renders value safe inside an nginx double-quoted string: backslash and
// double-quote are escaped. Callers must have already run ValidValue (which
// rejects newlines) — this is the second layer.
func Escape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}
