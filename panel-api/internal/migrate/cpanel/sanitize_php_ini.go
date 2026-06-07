package cpanel

import (
	"fmt"
	"regexp"
	"strings"
)

// phpIniPathDirectives are the PHP ini settings whose value is a
// filesystem path. A migrated cPanel ini that points one of these
// OUTSIDE the per-user FPM pool's open_basedir
// (/home/<u>:/run/mysqld/mysqld.sock:/tmp:/var/tmp, ADR-0023) breaks
// the site under jabali's jail — e.g.
//   session.save_path = "/var/cpanel/php/sessions/ea-php83"
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

// sanitizePhpIni neutralizes path-valued PHP directives in a migrated
// php.ini / .user.ini that resolve outside the user's open_basedir
// jail, preserving every benign tunable (memory_limit, upload_max_
// filesize, …) verbatim. Returns the rewritten content + a per-change
// note list for the manifest. changed=false ⇒ content untouched.
//
//   session.save_path outside jail → rewritten to /tmp (in jail, always
//       present, session files are 0600 so cross-tenant read is blocked;
//       matches jabali's own sso.php). Dropping it isn't safe — the
//       system php.ini default (/var/lib/php/sessions) is also outside
//       the jail.
//   open_basedir → always dropped (the pool hard-codes it; a migrated
//       value only conflicts).
//   error_log / sys_temp_dir / upload_tmp_dir / extension_dir /
//       soap.wsdl_cache_dir with an absolute path outside the jail →
//       dropped (PHP falls back to an in-jail default).
func sanitizePhpIni(content, username string) (string, []string, bool) {
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
		default:
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
