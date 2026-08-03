package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// appInstallPath joins docroot + subdirectory and REFUSES a join that
// escapes the (already-validated) docroot. The subdirectory is validated
// panel-side at create, but the agent runs as root and per CONVENTIONS
// re-checks every boundary: a traversal subdirectory ("../..") fed to an
// installer would write outside the docroot, and fed to a deleter would
// `rm -rf` the join target as the OS user — up to and including their
// whole home. Mirrors the containment check wordpress_delete.go carries.
func appInstallPath(docroot, subdirectory string) (string, error) {
	sub := strings.Trim(subdirectory, "/")
	p := filepath.Join(docroot, sub)
	rel, err := filepath.Rel(docroot, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("subdirectory escapes docroot: %q", subdirectory)
	}
	return p, nil
}
