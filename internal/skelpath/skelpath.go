// Package skelpath validates account-skeleton relative paths (GH #465).
// Lives at the repo root so BOTH panel-api (admin API, on write) and
// panel-agent (docroot apply) enforce the exact same rule — panel-agent
// cannot import panel-api/internal/models.
package skelpath

import (
	"errors"
	"path"
	"strings"
)

// MaxPathLen bounds a skeleton entry's relative path.
const MaxPathLen = 512

// Validate enforces a safe, normalized relative path: no traversal, no
// absolute path, no backslash/NUL, no empty/./.. segments, path.Clean==input.
func Validate(p string) error {
	switch {
	case p == "":
		return errors.New("path required")
	case len(p) > MaxPathLen:
		return errors.New("path too long")
	case strings.ContainsRune(p, 0):
		return errors.New("path contains NUL")
	case strings.ContainsRune(p, '\\'):
		return errors.New("path must use forward slashes")
	case strings.HasPrefix(p, "/"):
		return errors.New("path must be relative (no leading slash)")
	case path.Clean(p) != p:
		return errors.New("path must be normalized (no ./, ../, //, or trailing slash)")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("path contains an empty or . / .. segment")
		}
	}
	return nil
}
