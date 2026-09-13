package domainops

import (
	"errors"
	"path/filepath"
	"strings"
)

// Document-root confinement (JAB-279). The REST create/edit handlers and the
// operator CLI all route through these predicates so the rule that a domain's
// document root stays inside its owner's home cannot drift between adapters —
// the same one-owner-per-policy shape as CheckOwnerEligible.
//
// The path is a policy input, not an identity: callers pass the already-resolved
// owner username and the domain name. An empty docRoot is valid — it means "use
// the canonical default" — so the leaf returns nil and the caller fills the
// default.
//
// Two confinements, matching the REST handler's existing split (GH #526):
//   - ValidateDocumentRoot: the ADMIN / operator floor — the path may sit
//     anywhere under /home/<user>/ and must contain no ".." traversal. The CLI
//     is an operator tool, so it uses this one (the same branch the REST admin
//     create path takes).
//   - ValidateTenantDocumentRoot: the stricter confinement a non-admin owner
//     gets — the cleaned path must stay inside the domain's OWN tree
//     /home/<user>/domains/<domain>/ (so an app may point at a sub- or the base
//     dir, never a sibling domain or elsewhere in the home).
var (
	// ErrDocRootOutsideHome means the document root is not under the owner's
	// /home/<user>/ tree.
	ErrDocRootOutsideHome = errors.New("domainops: document root outside owner home")
	// ErrDocRootTraversal means the document root contains a ".." sequence.
	ErrDocRootTraversal = errors.New("domainops: document root contains path traversal")
	// ErrDocRootOutsideDomain means the document root escapes the domain's own
	// /home/<user>/domains/<domain>/ tree (tenant confinement).
	ErrDocRootOutsideDomain = errors.New("domainops: document root outside domain directory")
)

// ValidateDocumentRoot is the operator/admin confinement: an empty path is the
// default (returns nil), otherwise the path must be under /home/<user>/ with no
// ".." traversal. It intentionally does NOT filepath.Clean the input — it
// mirrors the REST admin path exactly, which prefix-checks the raw string (so
// e.g. a "/home/<user>//x" double slash passes, as it does today). The
// prefix check runs before the traversal check, so a path outside the home is
// reported as outside-home even when it also contains "..".
func ValidateDocumentRoot(docRoot, username, domainName string) error {
	if docRoot == "" {
		return nil
	}
	if !strings.HasPrefix(docRoot, "/home/"+username+"/") {
		return ErrDocRootOutsideHome
	}
	if strings.Contains(docRoot, "..") {
		return ErrDocRootTraversal
	}
	return nil
}

// ValidateTenantDocumentRoot is the stricter non-admin confinement: an empty
// path is the default (nil), a ".." sequence is rejected, and the cleaned path
// must equal or sit under /home/<user>/domains/<domain>/.
func ValidateTenantDocumentRoot(docRoot, username, domainName string) error {
	if docRoot == "" {
		return nil
	}
	if strings.Contains(docRoot, "..") {
		return ErrDocRootTraversal
	}
	clean := filepath.Clean(docRoot)
	base := "/home/" + username + "/domains/" + domainName
	if clean != base && !strings.HasPrefix(clean, base+"/") {
		return ErrDocRootOutsideDomain
	}
	return nil
}
