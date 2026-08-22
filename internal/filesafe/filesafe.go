// Package filesafe provides secure file path validation and access control
// for user-submitted file operations. This is the shared contract between the API
// handler (pre-accept gate) and the agent (defense-in-depth before filesystem access).
//
// Design principles:
//   - Path traversal prevention via filepath.Clean(), filepath.EvalSymlinks(), and prefix validation
//   - Symlink escape detection via os.Lstat() + ModeSymlink check
//   - TOCTOU mitigation using O_NOFOLLOW syscall flag on filesystem operations
//   - chown race prevention via 0600 temp file creation before chown sequencing
//   - Null-byte and control character rejection in paths
//   - Allow-list is closed-set: only absolute paths within owned docroots allowed
//   - No subprocess calls for path validation; all validation is in-process
package filesafe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Error codes for structured error reporting (suitable for API "error" field).
const (
	ErrCodeEmpty           = "empty"
	ErrCodeTooLong         = "too_long"         // >4096 bytes
	ErrCodeNotAbsolute     = "not_absolute"     // path not absolute
	ErrCodeContainsNull    = "contains_null"    // NUL byte in path
	ErrCodeContainsControl = "contains_control" // control character in path
	ErrCodeTraversal       = "path_traversal"   // .. or other traversal attempt
	ErrCodeSymlinkEscape   = "symlink_escape"   // symlink points outside scope
	ErrCodeNotInScope      = "not_in_scope"     // path not within owned docroots
	ErrCodeSymlinkLoop     = "symlink_loop"     // circular symlink chain
	ErrCodeBadCharacters   = "bad_characters"   // invalid filename characters
	ErrCodeDenied          = "path_denied"      // path within an explicit deny prefix (GH #1184 admin FM)
	ErrCodeReadOnly        = "read_only"        // mutating op on a path outside the write allow-list (JAB-358)
)

// DefaultAdminDeniedPrefixes is the hard deny-list for the GH #1184 admin File
// Manager (a root-scoped Scope). These paths are blocked for ALL operations —
// read, write, chmod, delete, rename — enforced agent-side so the panel can
// never be talked into touching them. Rationale per path family:
//   - credential/key stores that must never leave the box or be altered via a UI
//   - jabali's own control-plane state + binaries + sockets (self-protection:
//     an admin FM must not be able to corrupt the panel/agent that runs it, or
//     re-chmod /etc/jabali, which is a known SSH-lockout footgun)
// Deliberately full-block (not chmod-only): if a path is dangerous to re-perm
// it is dangerous to overwrite too, so one list covers both. Admins retain root
// SSH for anything intentionally excluded here.
var DefaultAdminDeniedPrefixes = []string{
	"/etc/shadow",
	"/etc/gshadow",
	"/etc/ssh",         // host keys + sshd_config (SSH-lockout footgun)
	"/root/.ssh",       // root authorized_keys
	"/etc/sudoers",     // and /etc/sudoers.d via prefix boundary below
	"/etc/sudoers.d",
	"/etc/jabali",      // panel/agent config + secrets + the 0755-perms lockout
	"/etc/letsencrypt", // live/archive/keys/accounts — cert private keys
	"/usr/local/bin/jabali-agent",
	"/usr/local/bin/jabali-panel",
	"/usr/local/bin/jabali",
	"/run/jabali",   // agent + panel unix sockets
	"/run/jabali-kratos",
	"/var/lib/jabali-uploads", // agent↔panel handoff staging
	// JAB-358: control-plane secret + state stores outside /etc/jabali that the
	// original deny-list missed. Blocked for read too — these hold the panel DB
	// password, session/JWT keys, mail creds, and restic repo state.
	"/etc/jabali-panel",  // bulwark session/JWT keys, restic repo password, panel env
	"/var/lib/jabali-panel",
	"/run/jabali-panel", // panel api + sso sockets
	"/run/jabali-bulwark",
}

// DefaultAdminMutableRoots is the closed WRITE allow-list for the admin File
// Manager (JAB-358). The admin scope may READ anywhere on the filesystem (minus
// DeniedPrefixes), but every MUTATING operation — write, mkdir, delete, rename,
// move, copy, chmod, extract, archive-create — is confined to these tenant /
// application data roots. This replaces the old "write everywhere except a
// deny-list" model: a deny-list can never enumerate every path through which
// root later executes, loads, or trusts a file (/etc/cron.d, systemd units,
// /etc/passwd, PAM, the dynamic loader, package hooks, …), so writes are a
// closed allow-list instead. System/config paths stay read-only; admins use
// root SSH for anything outside these roots.
var DefaultAdminMutableRoots = []string{
	"/home",    // tenant home trees — the admin FM's primary use
	"/var/www", // web content roots
	"/srv",     // some deployments serve tenant content here
	"/tmp",
	"/var/tmp",
}

// ValidationError is the error type returned by validators.
type ValidationError struct {
	Code   string // One of ErrCode* constants
	Detail string // Human-readable explanation
}

func (e *ValidationError) Error() string {
	return e.Code + ": " + e.Detail
}

// Scope represents a validated security context for file operations.
// All paths must resolve within the owned docroots.
type Scope struct {
	UserID        string
	Username      string
	OwnedDocroots []string
	// DeniedPrefixes are absolute path prefixes that are blocked for ALL
	// operations even when within OwnedDocroots (GH #1184 admin FM deny-list).
	// Empty for ordinary tenant scopes — a nil/empty list is a pure no-op, so
	// this is backward-compatible with every existing NewScope caller. The
	// check runs in verifyInScope, i.e. on BOTH the cleaned path and the
	// symlink-resolved path, so a symlink from an allowed dir into a denied
	// one is caught.
	DeniedPrefixes []string
	// MutableRoots is the closed WRITE allow-list (JAB-358). When non-empty, it
	// is the set of docroots that WriteScope narrows to for every mutating
	// operation, even though reads may range across all of OwnedDocroots (the
	// admin FM reads "/" but writes only these safe data roots). Empty is a pure
	// no-op: an ordinary tenant scope leaves it nil and its writes stay bounded
	// by OwnedDocroots alone, so this is backward-compatible with every existing
	// NewScope caller. Only NewAdminScope populates it, replacing the old "write
	// anywhere on / except a deny-list" model — a deny-list can never name every
	// path root later trusts, so admin writes are allow-listed.
	MutableRoots []string
	// writeNarrowed marks a scope produced by WriteScope: its OwnedDocroots are
	// the write allow-list, so a docroot miss means "read-only under the admin
	// File Manager" (ErrCodeReadOnly) rather than a plain not-in-scope.
	writeNarrowed bool
	// resolveCache caches EvalSymlinks results to detect symlink loops
	resolveCache map[string]string
}

// NewScope creates a new validated scope for file operations.
// ownedDocroots must be non-empty and will be normalized.
func NewScope(userID, username string, ownedDocroots []string) (*Scope, error) {
	if username == "" {
		return nil, &ValidationError{
			Code:   ErrCodeEmpty,
			Detail: "username cannot be empty",
		}
	}

	if len(ownedDocroots) == 0 {
		return nil, &ValidationError{
			Code:   ErrCodeEmpty,
			Detail: "ownedDocroots cannot be empty",
		}
	}

	// Normalize all docroots
	normalized := make([]string, len(ownedDocroots))
	for i, root := range ownedDocroots {
		root = filepath.Clean(root)
		if !filepath.IsAbs(root) {
			return nil, &ValidationError{
				Code:   ErrCodeNotAbsolute,
				Detail: fmt.Sprintf("docroot %q must be absolute", root),
			}
		}
		normalized[i] = root
	}

	return &Scope{
		UserID:        userID,
		Username:      username,
		OwnedDocroots: normalized,
		resolveCache:  make(map[string]string),
	}, nil
}

// NewAdminScope creates a root-scoped Scope for the GH #1184 admin File
// Manager: OwnedDocroots is "/" so READS may range across the whole
// filesystem, but every path in deniedPrefixes is blocked for all operations
// AND every MUTATING operation is additionally confined to mutableRoots
// (JAB-358). Pass DefaultAdminDeniedPrefixes and DefaultAdminMutableRoots
// unless a caller has a reason to differ. This is a privileged constructor —
// the panel gates it behind admin auth AND a default-off server setting; the
// deny-list plus the write allow-list here are the last line of defence.
//
// A non-empty mutableRoots is required: passing an empty list would leave the
// admin scope able to write anywhere on "/" (the very footgun JAB-358 closes),
// so it is rejected rather than silently degrading to the old behaviour.
func NewAdminScope(userID, username string, deniedPrefixes, mutableRoots []string) (*Scope, error) {
	if username == "" {
		return nil, &ValidationError{Code: ErrCodeEmpty, Detail: "username cannot be empty"}
	}
	if len(mutableRoots) == 0 {
		return nil, &ValidationError{Code: ErrCodeEmpty, Detail: "admin scope requires a non-empty write allow-list (mutableRoots)"}
	}
	denied := make([]string, 0, len(deniedPrefixes))
	for _, p := range deniedPrefixes {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) {
			return nil, &ValidationError{Code: ErrCodeNotAbsolute, Detail: fmt.Sprintf("denied prefix %q must be absolute", p)}
		}
		denied = append(denied, p)
	}
	mutable := make([]string, 0, len(mutableRoots))
	for _, p := range mutableRoots {
		p = filepath.Clean(p)
		if !filepath.IsAbs(p) {
			return nil, &ValidationError{Code: ErrCodeNotAbsolute, Detail: fmt.Sprintf("mutable root %q must be absolute", p)}
		}
		if p == "/" {
			// "/" as a mutable root would re-open whole-filesystem writes and
			// defeat the allow-list entirely; reject it explicitly.
			return nil, &ValidationError{Code: ErrCodeNotInScope, Detail: `"/" is not a valid mutable root`}
		}
		mutable = append(mutable, p)
	}
	return &Scope{
		UserID:         userID,
		Username:       username,
		OwnedDocroots:  []string{"/"},
		DeniedPrefixes: denied,
		MutableRoots:   mutable,
		resolveCache:   make(map[string]string),
	}, nil
}

// NewScopeForTest creates a Scope for testing with a temporary docroot.
// Useful for testing symlink escape detection and other filesafe behavior
// without requiring real user accounts.
func NewScopeForTest(userID, username, docroot string) *Scope {
	return &Scope{
		UserID:        userID,
		Username:      username,
		OwnedDocroots: []string{docroot},
		resolveCache:  make(map[string]string),
	}
}

// Clean validates and cleans a path, returning the canonical form.
// The path must be absolute and resolve within the owned docroots.
// This is a pre-access gate; use before any filesystem operation.
func (s *Scope) Clean(pathStr string) (string, error) {
	if err := s.validatePathString(pathStr); err != nil {
		return "", err
	}

	// Clean the path (resolves . and .. in a path-safe manner)
	cleaned := filepath.Clean(pathStr)

	// Verify cleaned path is still absolute
	if !filepath.IsAbs(cleaned) {
		return "", &ValidationError{
			Code:   ErrCodeNotAbsolute,
			Detail: fmt.Sprintf("path %q cleaned to non-absolute %q", pathStr, cleaned),
		}
	}

	// Verify path is within scope
	if err := s.verifyInScope(cleaned); err != nil {
		return "", err
	}

	return cleaned, nil
}

// Resolve validates and resolves a path, following symlinks up to 40 levels deep.
// Returns the absolute canonical path (all symlinks resolved).
// Detects symlink loops and escape attempts.
// Use before actual filesystem access to ensure path is real and in-scope.
func (s *Scope) Resolve(pathStr string) (string, error) {
	if err := s.validatePathString(pathStr); err != nil {
		return "", err
	}

	// Start with cleaned path
	cleaned := filepath.Clean(pathStr)
	if !filepath.IsAbs(cleaned) {
		return "", &ValidationError{
			Code:   ErrCodeNotAbsolute,
			Detail: fmt.Sprintf("path %q cleaned to non-absolute %q", pathStr, cleaned),
		}
	}

	// Check in-scope before resolution (guard against symlink bombs on the way)
	if err := s.verifyInScope(cleaned); err != nil {
		return "", err
	}

	// SECURITY (Gitea #424): canonicalize by resolving the NEAREST EXISTING
	// ancestor and re-appending the non-existent tail, failing CLOSED on any
	// EvalSymlinks error other than "doesn't exist". The previous code did
	// EvalSymlinks(cleaned) and, on ANY error (ENOENT for every new-file create
	// and the .tmp.<rand> name), silently fell back to the LOGICAL path — so a
	// tenant-planted PARENT symlink was never collapsed and verifyInScope passed
	// on a prefix that didn't reflect where the bytes actually land.
	resolved, err := s.resolveNearest(cleaned)
	if err != nil {
		return "", err
	}

	// Verify resolved path is still in scope (symlink escape detection)
	if err := s.verifyInScope(resolved); err != nil {
		return "", err
	}

	// Check for symlink loops by doing a final lstat and comparing
	// (EvalSymlinks detects loops via iteration limit, but we add defense-in-depth)
	fi, err := os.Lstat(resolved)
	if err == nil && (fi.Mode()&os.ModeSymlink) != 0 {
		// Final path is itself a symlink (EvalSymlinks may not have followed it)
		return "", &ValidationError{
			Code:   ErrCodeSymlinkEscape,
			Detail: fmt.Sprintf("resolved path %q is itself a symlink", resolved),
		}
	}

	return resolved, nil
}

// WriteScope returns the scope that every MUTATING file operation must run
// against (JAB-358). When the scope carries a write allow-list (the admin File
// Manager), it narrows OwnedDocroots down to MutableRoots and marks the copy
// writeNarrowed. Because every escape-proof op resolves relative to a base fd
// derived from OwnedDocroots (baseFor → openat2 RESOLVE_BENEATH), narrowing the
// docroots is what actually CONFINES the write in the kernel: a path outside a
// safe data root is refused, and — critically — a relative symlink planted
// inside a safe root whose target climbs out via ".." (e.g. ~/x -> ../../etc/
// cron.d) is rejected with EXDEV mid-walk, which a logical prefix check alone
// can never guarantee against a concurrent swap. Reads keep using the original
// scope (OwnedDocroots="/"), so the admin FM still browses the whole tree.
//
// For an ordinary tenant scope (no MutableRoots) it returns the scope
// unchanged, so tenant file ops are byte-for-byte identical to before.
func (s *Scope) WriteScope() *Scope {
	if len(s.MutableRoots) == 0 {
		return s
	}
	return &Scope{
		UserID:         s.UserID,
		Username:       s.Username,
		OwnedDocroots:  s.MutableRoots,
		DeniedPrefixes: s.DeniedPrefixes,
		MutableRoots:   s.MutableRoots,
		writeNarrowed:  true,
		resolveCache:   make(map[string]string),
	}
}

// Open opens a file at the given path with the given flags and perms.
// Uses O_NOFOLLOW to prevent TOCTOU symlink attacks.
// Path is validated and resolved before opening.
func (s *Scope) Open(pathStr string, flags int, perm os.FileMode) (*os.File, error) {
	// Validate first
	cleanPath, err := s.Clean(pathStr)
	if err != nil {
		return nil, err
	}

	// Add O_NOFOLLOW to prevent TOCTOU symlink race
	// If the final component is a symlink, open() will fail with ELOOP
	flags |= syscall.O_NOFOLLOW

	// Use os.OpenFile which ultimately calls open(2) with our flags
	return os.OpenFile(cleanPath, flags, perm)
}

// ChownToUser changes the owner of the file at pathStr to the given uid/gid.
// Uses O_NOFOLLOW internally to prevent symlink-based chown races.
// Recommended: create temp file with 0600, then chown, then move into place.
func (s *Scope) ChownToUser(pathStr string, uid, gid int) error {
	// Validate first
	cleanPath, err := s.Clean(pathStr)
	if err != nil {
		return err
	}

	// Lstat without following symlinks to ensure target is not a symlink
	fi, err := os.Lstat(cleanPath)
	if err != nil {
		return err
	}
	if (fi.Mode() & os.ModeSymlink) != 0 {
		return &ValidationError{
			Code:   ErrCodeSymlinkEscape,
			Detail: fmt.Sprintf("cannot chown symlink %q", cleanPath),
		}
	}

	return os.Chown(cleanPath, uid, gid)
}

// --- Private helpers ---

// validatePathString performs pre-path checks: null bytes, control chars, length.
func (s *Scope) validatePathString(pathStr string) error {
	if pathStr == "" {
		return &ValidationError{
			Code:   ErrCodeEmpty,
			Detail: "path cannot be empty",
		}
	}

	if len(pathStr) > 4096 {
		return &ValidationError{
			Code:   ErrCodeTooLong,
			Detail: fmt.Sprintf("path exceeds 4096 bytes (%d bytes)", len(pathStr)),
		}
	}

	// Reject null bytes
	if strings.ContainsRune(pathStr, '\x00') {
		return &ValidationError{
			Code:   ErrCodeContainsNull,
			Detail: "path contains NUL byte",
		}
	}

	// Reject control characters (ASCII 0-31 except tab, lf, cr are still bad)
	for _, r := range pathStr {
		if r < 32 {
			return &ValidationError{
				Code:   ErrCodeContainsControl,
				Detail: fmt.Sprintf("path contains control character (0x%02x)", r),
			}
		}
	}

	// Reject .. traversal attempts (defense-in-depth; filepath.Clean will also handle)
	if strings.Contains(pathStr, "..") {
		return &ValidationError{
			Code:   ErrCodeTraversal,
			Detail: "path contains '..' traversal sequence",
		}
	}

	return nil
}

// verifyInScope checks if a path is within one of the owned docroots.
// Uses word-boundary checking: /x must not match /xyz.
func (s *Scope) verifyInScope(pathStr string) error {
	// Path must be absolute
	if !filepath.IsAbs(pathStr) {
		return &ValidationError{
			Code:   ErrCodeNotAbsolute,
			Detail: fmt.Sprintf("path %q is not absolute", pathStr),
		}
	}

	// Check containment in owned docroots.
	inScope := false
	for _, docroot := range s.OwnedDocroots {
		docroot = filepath.Clean(docroot)

		// "/" (root scope, GH #1184 admin FM) contains every absolute path;
		// the naive docroot+"/" prefix would become "//" and match nothing.
		if docroot == "/" {
			inScope = true
			break
		}
		// Exact match (for root-level docroots)
		if pathStr == docroot {
			inScope = true
			break
		}
		// Prefix match with / boundary (no /x matching /xyz)
		if strings.HasPrefix(pathStr, docroot+"/") {
			inScope = true
			break
		}
	}
	if !inScope {
		if s.writeNarrowed {
			// A WriteScope miss: the path is readable (the admin FM browses "/")
			// but not writable — say so plainly instead of a bare not-in-scope.
			return &ValidationError{
				Code:   ErrCodeReadOnly,
				Detail: fmt.Sprintf("path %q is read-only in the admin File Manager; writable roots: %v", pathStr, s.OwnedDocroots),
			}
		}
		return &ValidationError{
			Code:   ErrCodeNotInScope,
			Detail: fmt.Sprintf("path %q is not within owned docroots: %v", pathStr, s.OwnedDocroots),
		}
	}

	// GH #1184 deny-list: block even in-scope paths that fall within a denied
	// prefix. Runs on both the cleaned path (Clean) and the symlink-resolved
	// path (Resolve), so a symlink from an allowed dir into a denied one is
	// rejected. Same word-boundary rule: /etc/ssh blocks /etc/ssh/* but not
	// /etc/sshfoo.
	for _, denied := range s.DeniedPrefixes {
		if pathStr == denied || strings.HasPrefix(pathStr, denied+"/") {
			return &ValidationError{
				Code:   ErrCodeDenied,
				Detail: fmt.Sprintf("path %q is within denied prefix %q", pathStr, denied),
			}
		}
	}

	return nil
}

// resolveNearest canonicalizes cleaned by EvalSymlinks-resolving its nearest
// EXISTING ancestor and re-joining the (guaranteed non-existent, hence
// symlink-free) tail. It fails CLOSED on any EvalSymlinks error other than the
// component simply not existing (ENOENT) — an EACCES/ELOOP is returned, never
// swallowed into a logical-path fallback (Gitea #424). The canonical ancestor
// must itself stay in scope, so a parent symlink pointing out of the docroot is
// rejected even when the final component does not yet exist.
func (s *Scope) resolveNearest(cleaned string) (string, error) {
	dir := cleaned
	var tailParts []string
	for {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if verr := s.verifyInScope(real); verr != nil {
				return "", verr
			}
			out := real
			for i := len(tailParts) - 1; i >= 0; i-- {
				out = filepath.Join(out, tailParts[i])
			}
			return out, nil
		}
		if !os.IsNotExist(err) {
			// EACCES, ELOOP (symlink loop / too many levels), etc. — fail closed.
			return "", &ValidationError{Code: ErrCodeSymlinkEscape, Detail: fmt.Sprintf("cannot resolve %q: %v", dir, err)}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached "/" with no existing ancestor — impossible for an in-scope
			// path (the docroot exists); fail closed.
			return "", &ValidationError{Code: ErrCodeNotInScope, Detail: fmt.Sprintf("no existing ancestor for %q", cleaned)}
		}
		tailParts = append(tailParts, filepath.Base(dir))
		dir = parent
	}
}
