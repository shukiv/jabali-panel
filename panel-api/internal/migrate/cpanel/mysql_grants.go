package cpanel

import (
	"bufio"
	"os"
	"regexp"
	"strings"
)

// CompatUser captures one ORIGINAL MySQL user from the cpmove
// `mysql.sql` grants file — the user the source-side app's hardcoded
// db.php / wp-config.php / settings.php / etc references (and the
// password HASH the source kept). Recreating it on the destination
// with its original name + hash + grants lets the migrated app keep
// working with zero config rewrite — the bug class that broke the
// `newpuzzleans` cpmove restore (jabali had created a namespaced
// `<target>_<db>` user with a fresh random password; the app's
// `mysqli_connect('newpuzzl_wp', '<original-pw>', …)` got Access
// denied because the original user never existed on the destination).
type CompatUser struct {
	Name  string        // 'newpuzzl_wp'
	Host  string        // we only collect 'localhost' (jabali single-host)
	Hash  string        // *XXXX… 41-char mysql_native_password hash
	Grant []CompatGrant // ON `<source-db>`.* GRANT <privs>
}

type CompatGrant struct {
	SourceDB string   // raw cPanel-side db name as it appears in the grant
	Privs    []string // e.g. ["ALL"] or ["SELECT","INSERT",…]
}

// `mysql_native_password` old-style hash: '*' + 40 hex.
var grantUserHashRe = regexp.MustCompile(
	`(?i)GRANT\s+USAGE\s+ON\s+\*\.\*\s+TO\s+'([^']+)'@'([^']+)'\s+IDENTIFIED\s+BY\s+PASSWORD\s+'(\*[0-9A-Fa-f]{40})'`,
)

// Privs ON `db`.* TO 'u'@'h' — backticks optional. cpmove escapes
// underscores in db names with backslash (`\_`); strip them.
var grantPrivRe = regexp.MustCompile(
	`(?i)GRANT\s+([A-Z][A-Z, ]*[A-Z])\s+ON\s+` + "`?" + `([^` + "`" + `'\s]+)` + "`?" + `\.\*\s+TO\s+'([^']+)'@'([^']+)'`,
)

// nativePwdHashRe pins the supported hash format for the agent
// validator on the other end.
var nativePwdHashRe = regexp.MustCompile(`^\*[0-9A-Fa-f]{40}$`)

// IsNativePasswordHash returns true for an old-style
// mysql_native_password hash (the form cpmove ships).
func IsNativePasswordHash(s string) bool { return nativePwdHashRe.MatchString(s) }

// unescapeCpmoveDBName strips the LIKE-escapes cpmove writes around
// underscores in db names (`\_`) — the actual db is just `_`.
func unescapeCpmoveDBName(s string) string {
	s = strings.Trim(s, "`")
	return strings.ReplaceAll(s, `\_`, `_`)
}

// normalisePrivs maps cpmove's prose ("ALL PRIVILEGES") to the form
// the agent's db_user.grant expects ("ALL"), and splits comma lists.
func normalisePrivs(raw string) []string {
	raw = strings.TrimSpace(raw)
	up := strings.ToUpper(raw)
	if up == "ALL PRIVILEGES" || up == "ALL" {
		return []string{"ALL"}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ParseMySQLGrants reads a cpmove `cpmove-<user>/mysql.sql` grants
// file and returns one CompatUser per ORIGINAL `user@localhost` it
// contained — populated with the password hash + every per-DB grant.
//
// We deliberately keep ONLY `@localhost` entries — jabali is a
// single-host destination; the source-side IP/hostname grants don't
// translate. The file may not exist (older pkgacct shapes, DA, etc.)
// — in that case we return (nil, nil) so the caller falls through to
// the pre-existing "panel-managed user" path without disruption.
func ParseMySQLGrants(path string) ([]CompatUser, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	users := map[string]*CompatUser{} // key = name+"@"+host
	scanner := bufio.NewScanner(f)
	// cpmove grants files are typically small (KB), but raise the buffer
	// cap so a pathological single line can't truncate.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "--") || strings.HasPrefix(line, "/*") {
			continue
		}
		// USAGE-with-hash line registers the user.
		if m := grantUserHashRe.FindStringSubmatch(line); m != nil {
			name, host, hash := m[1], m[2], m[3]
			if host != "localhost" {
				continue
			}
			key := name + "@" + host
			if _, ok := users[key]; !ok {
				users[key] = &CompatUser{Name: name, Host: host, Hash: hash}
			} else {
				users[key].Hash = hash
			}
			continue
		}
		// Privileges-on-db line attaches a grant.
		if m := grantPrivRe.FindStringSubmatch(line); m != nil {
			privsRaw, db, name, host := m[1], m[2], m[3], m[4]
			if host != "localhost" {
				continue
			}
			// USAGE was matched by the regex above; the USAGE
			// privilege itself on `db`.* is meaningless to recreate
			// (it's "no privileges"). Skip pure USAGE non-* entries.
			if strings.EqualFold(strings.TrimSpace(privsRaw), "USAGE") {
				continue
			}
			key := name + "@" + host
			u, ok := users[key]
			if !ok {
				// Defensive: a privileges line without a preceding
				// USAGE+hash. We can still create the grant later
				// IF the user already exists by some other means;
				// skip recording it without a hash (we can't create
				// the user blind).
				continue
			}
			u.Grant = append(u.Grant, CompatGrant{
				SourceDB: unescapeCpmoveDBName(db),
				Privs:    normalisePrivs(privsRaw),
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	out := make([]CompatUser, 0, len(users))
	for _, u := range users {
		out = append(out, *u)
	}
	return out, nil
}
