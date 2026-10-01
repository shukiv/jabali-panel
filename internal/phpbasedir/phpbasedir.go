// Package phpbasedir validates and expands a domain's own open_basedir (GH
// #1701 slice 3). The agent sends it to PHP through fastcgi_param
// PHP_ADMIN_VALUE, which replaces the pool's open_basedir for that request, so
// the value is a SECURITY boundary: a bad entry can widen the tenant's PHP jail
// onto another tenant's home. panel-api validates a value before it is stored
// (Normalize); panel-agent expands the tokens and checks every resulting path
// again before it renders the vhost (Expand). Both use this package so the
// rules have a single source of truth.
//
// A stored value is a colon-separated list of entries. Each entry is a token
// or an absolute path:
//
//	{WEBSPACEROOT}  the owner's home, /home/<user> (the pool default's root)
//	{DOCROOT}       the domain's document root
//	{TMP}           /tmp and /var/tmp
//
// Expand always appends SystemEntries: the pool default carries both, and
// without them a domain value would break MariaDB over localhost (GH #112) and
// the WordPress cache purge (JAB-199) without anything reporting it.
package phpbasedir

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// The tokens a stored value may hold.
const (
	TokenWebspaceRoot = "{WEBSPACEROOT}"
	TokenDocRoot      = "{DOCROOT}"
	TokenTmp          = "{TMP}"
)

// MaxLen caps a stored value; MaxEntries caps its entries.
const (
	MaxLen     = 1024
	MaxEntries = 16
)

// TmpDirs is what {TMP} expands to: the pool default's temp entries.
var TmpDirs = []string{"/tmp", "/var/tmp"}

// SystemEntries are appended to every domain value. Both are in the pool
// default (install/php/jabali-php-pool.conf.tmpl).
var SystemEntries = []string{"/run/mysqld/mysqld.sock", "/run/jabali-wp-purge"}

// pathRE bounds a path entry. Anything outside it is refused, notably "$"
// (nginx interpolates it inside the quoted fastcgi_param value), quotes,
// backslashes, ";", braces, whitespace and control characters.
var pathRE = regexp.MustCompile(`^/[A-Za-z0-9._@+/-]*$`)

// ownerRE is the system username rule (user.create).
var ownerRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// deniedRoots may not be an entry, nor be inside one, even for an admin:
// PHP reading them is never needed to serve a site.
var deniedRoots = []string{"/root", "/proc", "/sys"}

// Home is the owner's home directory, the pool default's root.
func Home(owner string) string { return "/home/" + owner }

func within(p, root string) bool {
	return p == root || strings.HasPrefix(p, root+"/")
}

// checkPath validates one absolute path entry for owner. tenant restricts it
// to the owner's home.
func checkPath(p, owner string, tenant bool) error {
	if !pathRE.MatchString(p) {
		return fmt.Errorf("%q is not an absolute path made of letters, digits and . _ @ + - /", p)
	}
	if path.Clean(p) != p {
		return fmt.Errorf("%q is not a clean path (no trailing /, no // or . or .. segments)", p)
	}
	if p == "/" {
		return fmt.Errorf(`"/" would lift the restriction entirely`)
	}
	home := Home(owner)
	if tenant {
		if !within(p, home) {
			return fmt.Errorf("%q is outside your home directory %s", p, home)
		}
		return nil
	}
	// open_basedir admits everything under an entry, so an entry above the
	// owner's home, or inside any other home, opens other tenants' files.
	if within(home, p) && p != home {
		return fmt.Errorf("%q contains other users' home directories", p)
	}
	if within(p, "/home") && !within(p, home) {
		return fmt.Errorf("%q is inside another user's home directory", p)
	}
	for _, r := range deniedRoots {
		if within(p, r) || within(r, p) {
			return fmt.Errorf("%q gives PHP access to %s", p, r)
		}
	}
	return nil
}

func validOwner(owner string) error {
	if !ownerRE.MatchString(owner) {
		return fmt.Errorf("invalid owner username %q", owner)
	}
	return nil
}

func splitEntries(value string) ([]string, error) {
	if len(value) > MaxLen {
		return nil, fmt.Errorf("open_basedir is longer than %d characters", MaxLen)
	}
	entries := strings.Split(value, ":")
	if len(entries) > MaxEntries {
		return nil, fmt.Errorf("open_basedir has more than %d entries", MaxEntries)
	}
	for _, e := range entries {
		if e == "" {
			return nil, fmt.Errorf("open_basedir has an empty entry")
		}
	}
	return entries, nil
}

// Normalize validates a stored per-domain open_basedir for a domain owned by
// owner and returns its canonical form: entries in order, duplicates dropped.
// tenant restricts path entries to the owner's home, so a tenant can only
// narrow the pool default (their home plus the temp directories). An admin may
// add other paths, but never "/", another user's home, or anything above one.
func Normalize(value, owner string, tenant bool) (string, error) {
	if err := validOwner(owner); err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("open_basedir is empty; clear the setting to inherit the pool default instead")
	}
	entries, err := splitEntries(value)
	if err != nil {
		return "", err
	}
	var out []string
	seen := map[string]bool{}
	for _, e := range entries {
		switch e {
		case TokenWebspaceRoot, TokenDocRoot, TokenTmp:
		default:
			if err := checkPath(e, owner, tenant); err != nil {
				return "", err
			}
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return strings.Join(out, ":"), nil
}

// Expand turns a stored value into the open_basedir PHP receives: tokens
// replaced (docRoot for {DOCROOT}), every path checked again, SystemEntries
// appended, duplicates dropped. It applies the admin rules, since the agent
// cannot tell who stored the value; panel-api already applied the tenant ones.
// On any failure the caller pins the pool default instead.
func Expand(value, owner, docRoot string) (string, error) {
	if err := validOwner(owner); err != nil {
		return "", err
	}
	entries, err := splitEntries(value)
	if err != nil {
		return "", err
	}
	var out []string
	seen := map[string]bool{}
	add := func(p string) error {
		if err := checkPath(p, owner, false); err != nil {
			return err
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
		return nil
	}
	for _, e := range entries {
		var paths []string
		switch e {
		case TokenWebspaceRoot:
			paths = []string{Home(owner)}
		case TokenDocRoot:
			if !within(docRoot, Home(owner)) {
				return "", fmt.Errorf("document root %q is outside %s", docRoot, Home(owner))
			}
			paths = []string{docRoot}
		case TokenTmp:
			paths = TmpDirs
		default:
			paths = []string{e}
		}
		for _, p := range paths {
			if err := add(p); err != nil {
				return "", err
			}
		}
	}
	for _, p := range SystemEntries {
		if err := add(p); err != nil {
			return "", err
		}
	}
	return strings.Join(out, ":"), nil
}

// SafePinValue reports whether an open_basedir read from a rendered pool file
// can be pinned on a vhost as is: a colon-separated list of clean absolute
// paths, nothing nginx or PHP_ADMIN_VALUE would read as syntax.
func SafePinValue(v string) bool {
	if v == "" || len(v) > 4096 {
		return false
	}
	for _, e := range strings.Split(v, ":") {
		if !pathRE.MatchString(e) || path.Clean(e) != e {
			return false
		}
	}
	return true
}
