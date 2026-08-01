package cpanel

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// phpIniPathDirectives are the PHP ini settings whose value is a
// filesystem path. A migrated cPanel ini that points one of these
// OUTSIDE the per-user FPM pool's open_basedir
// (/home/<u>:/run/mysqld/mysqld.sock:/tmp:/var/tmp, ADR-0023) breaks
// the site under jabali's jail — e.g.
//
//	session.save_path = "/var/cpanel/php/sessions/ea-php83"
//
// triggers an open_basedir PHP warning on every request and $_SESSION
// silently stops persisting (logins/carts break).
var phpIniPathDirectives = map[string]bool{
	"session.save_path":   true,
	"open_basedir":        true,
	"error_log":           true,
	"sys_temp_dir":        true,
	"upload_tmp_dir":      true,
	"extension_dir":       true,
	"soap.wsdl_cache_dir": true,
	"auto_prepend_file":   true,
	"auto_append_file":    true,
	"include_path":        true,
}

// phpIniDocrootDirectives name a file (or list of paths) that must actually
// RESOLVE, as opposed to merely sitting inside the open_basedir jail.
//
// JAB-211: cPanel security/caching plugins (MalCare, Wordfence, WP Rocket)
// write an absolute source-layout path into .user.ini —
//
//	auto_prepend_file = '/home/<user>/public_html/malcare-waf.php'
//
// On jabali the docroot is /home/<user>/domains/<domain>/public_html, so that
// path does not exist and PHP fatals with "Failed opening required" before
// running a line: the site serves a bare 500 while the restore summary stays
// green.
//
// pathInJail cannot catch this. The path IS under /home/<user>, so jail
// membership reports true and the directive sails through. For these
// directives existence is the test, and the repair is to remap the source
// docroot onto the destination one — NOT to delete the line. Deleting also
// stops the 500, but it silently disarms the site's WAF, which trades a
// visible outage for an invisible security regression.
var phpIniDocrootDirectives = map[string]bool{
	"auto_prepend_file": true,
	"auto_append_file":  true,
}

var iniDirectiveRe = regexp.MustCompile(`^(\s*)([A-Za-z0-9_.]+)\s*=\s*(.*?)\s*$`)

// pathInJail reports whether an absolute path lives inside the per-user
// open_basedir jail for username.
func pathInJail(p, username string) bool {
	p = strings.Trim(p, `"' `)
	home := "/home/" + username
	switch {
	case p == home, strings.HasPrefix(p, home+"/"):
		return true
	case p == "/tmp", strings.HasPrefix(p, "/tmp/"):
		return true
	case p == "/var/tmp", strings.HasPrefix(p, "/var/tmp/"):
		return true
	}
	return false
}

// docrootCandidates maps an absolute source-layout path onto docroot, most
// specific first.
//
// cPanel serves out of /home/<user>/public_html, jabali out of
// /home/<user>/domains/<domain>/public_html, so the useful part of a migrated
// path is whatever follows the source docroot. Returns nil when there is
// nothing to work with (no docroot, or a relative value) so callers degrade to
// the pre-JAB-211 behaviour instead of synthesising a path off "/".
func docrootCandidates(p, username, docroot string) []string {
	if docroot == "" || !strings.HasPrefix(p, "/") {
		return nil
	}
	var cands []string
	add := func(rel string) {
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" {
			return
		}
		c := filepath.Join(docroot, rel)
		for _, existing := range cands {
			if existing == c {
				return
			}
		}
		cands = append(cands, c)
	}
	// Tail after the LAST public_html is the docroot-relative part — and the
	// last one matters because the destination docroot itself ends in
	// public_html, so an already-correct path must map to itself.
	if i := strings.LastIndex(p, "/public_html/"); i >= 0 {
		add(p[i+len("/public_html/"):])
	}
	if home := "/home/" + username + "/"; strings.HasPrefix(p, home) {
		add(strings.TrimPrefix(p, home))
	}
	add(filepath.Base(p))
	return cands
}

// remapFile finds the migrated copy of a source-layout file under docroot.
func remapFile(p, username, docroot string, exists func(string) bool) (string, bool) {
	for _, c := range docrootCandidates(p, username, docroot) {
		if exists(c) {
			return c, true
		}
	}
	return "", false
}

// remapUnder finds a path under docroot whose PARENT exists — for values like
// error_log where the file is created on demand but the directory is not.
func remapUnder(p, username, docroot string, exists func(string) bool) (string, bool) {
	for _, c := range docrootCandidates(p, username, docroot) {
		if exists(filepath.Dir(c)) {
			return c, true
		}
	}
	return "", false
}

// sanitizePhpIni neutralizes path-valued PHP directives in a migrated
// php.ini / .user.ini that resolve outside the user's open_basedir
// jail, preserving every benign tunable (memory_limit, upload_max_
// filesize, …) verbatim. Returns the rewritten content + a per-change
// note list for the manifest. changed=false ⇒ content untouched.
//
//	session.save_path outside jail → rewritten to /tmp (in jail, always
//	    present, session files are 0600 so cross-tenant read is blocked;
//	    matches jabali's own sso.php). Dropping it isn't safe — the
//	    system php.ini default (/var/lib/php/sessions) is also outside
//	    the jail.
//	open_basedir → always dropped (the pool hard-codes it; a migrated
//	    value only conflicts).
//	sys_temp_dir / upload_tmp_dir / extension_dir / soap.wsdl_cache_dir
//	    with an absolute path outside the jail → dropped (PHP falls back
//	    to an in-jail default).
//	auto_prepend_file / auto_append_file / error_log / include_path →
//	    judged by whether the path RESOLVES, not by jail membership, and
//	    remapped onto docroot when the source docroot layout explains the
//	    miss (JAB-211). See phpIniDocrootDirectives.
//
// docroot is the destination docroot; "" disables remapping. exists defaults
// to os.Stat and is injected by tests.
func sanitizePhpIni(content, username, docroot string, exists func(string) bool) (string, []string, bool) {
	if exists == nil {
		exists = func(p string) bool { _, err := os.Stat(p); return err == nil }
	}
	lines := strings.Split(content, "\n")
	var notes []string
	for i, line := range lines {
		m := iniDirectiveRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent, key, val := m[1], m[2], m[3]
		kl := strings.ToLower(key)
		if !phpIniPathDirectives[kl] {
			continue
		}
		switch kl {
		case "session.save_path":
			v := strings.Trim(val, `"' `)
			pathPart := v
			if semi := strings.LastIndex(v, ";"); semi >= 0 {
				pathPart = v[semi+1:] // "N;/path" depth form
			}
			if strings.HasPrefix(pathPart, "/") && !pathInJail(pathPart, username) {
				lines[i] = indent + key + " = /tmp"
				notes = append(notes, fmt.Sprintf("session.save_path %q → /tmp", v))
			}
		case "open_basedir":
			lines[i] = indent + "; jabali-migration: removed open_basedir (pool-managed) ; was: " + val
			notes = append(notes, "open_basedir removed (pool sets it)")

		case "error_log":
			// Keep non-paths (error_log = syslog) and stay with the old
			// removal for anything outside the jail. What is new is the
			// in-jail-but-stale case: /home/<u>/public_html/php_errors.log
			// passes pathInJail yet its directory does not exist here, so PHP
			// drops the log on the floor.
			v := strings.Trim(val, `"' `)
			if !strings.HasPrefix(v, "/") {
				continue
			}
			if !pathInJail(v, username) {
				lines[i] = indent + "; jabali-migration: removed " + key + " (path outside open_basedir) ; was: " + val
				notes = append(notes, fmt.Sprintf("%s removed (path %q outside open_basedir)", kl, v))
				continue
			}
			if exists(filepath.Dir(v)) {
				continue
			}
			if np, ok := remapUnder(v, username, docroot, exists); ok {
				lines[i] = indent + key + " = " + np
				notes = append(notes, fmt.Sprintf("error_log %q → %q (source docroot layout)", v, np))
			}
			// Unresolvable: leave it. A missing error_log is not fatal, and
			// churning it would add manifest noise for no gain.

		case "include_path":
			v := strings.Trim(val, `"' `)
			var kept, dropped []string
			for _, part := range strings.Split(v, ":") {
				p := strings.TrimSpace(part)
				switch {
				case p == "":
					// Trailing/duplicate separator — drop silently.
				case !strings.HasPrefix(p, "/"):
					kept = append(kept, p) // "." and friends stay verbatim
				case exists(p):
					kept = append(kept, p)
				default:
					if np, ok := remapFile(p, username, docroot, exists); ok {
						kept = append(kept, np)
					} else {
						dropped = append(dropped, p)
					}
				}
			}
			rebuilt := strings.Join(kept, ":")
			if rebuilt == v {
				continue
			}
			lines[i] = indent + key + ` = "` + rebuilt + `"`
			if len(dropped) > 0 {
				notes = append(notes, fmt.Sprintf("include_path dropped unresolvable %v", dropped))
			} else {
				notes = append(notes, fmt.Sprintf("include_path %q → %q (source docroot layout)", v, rebuilt))
			}

		default:
			if phpIniDocrootDirectives[kl] {
				// auto_prepend_file / auto_append_file: existence decides, not
				// jail membership. An unresolvable value here is a hard fatal
				// on EVERY request (JAB-211).
				v := strings.Trim(val, `"' `)
				if !strings.HasPrefix(v, "/") || exists(v) {
					continue
				}
				if np, ok := remapFile(v, username, docroot, exists); ok {
					lines[i] = indent + key + " = " + np
					notes = append(notes, fmt.Sprintf("%s %q → %q (source docroot layout)", kl, v, np))
					continue
				}
				// Nothing to point at. Commenting out costs whatever the file
				// did — often a WAF — so this note has to be loud enough that
				// the operator re-arms it by hand.
				lines[i] = indent + "; jabali-migration: disabled " + key + " (target not migrated) ; was: " + val
				notes = append(notes, fmt.Sprintf(
					"%s DISABLED — %q was not migrated; every request fataled until this was "+
						"commented out. If it was a security plugin (MalCare/Wordfence), the site "+
						"is now running WITHOUT it — reinstall the plugin to re-arm.", kl, v))
				continue
			}
			v := strings.Trim(val, `"' `)
			if strings.HasPrefix(v, "/") && !pathInJail(v, username) {
				lines[i] = indent + "; jabali-migration: removed " + key + " (path outside open_basedir) ; was: " + val
				notes = append(notes, fmt.Sprintf("%s removed (path %q outside open_basedir)", kl, v))
			}
		}
	}
	if len(notes) == 0 {
		return content, nil, false
	}
	return strings.Join(lines, "\n"), notes, true
}
