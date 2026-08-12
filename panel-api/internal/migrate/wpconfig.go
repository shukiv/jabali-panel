package migrate

import (
	"fmt"
	"regexp"
	"strings"
)

// wpConfigDefineRe matches a wp-config.php `define('DB_KEY', 'value');` for the
// four DB constants, tolerating single OR double quotes and flexible spacing.
// Identical to the cPanel restore's proven pattern — this is the single shared
// copy so the cPanel and wordpress_ssh (GH #647/#648) imports never drift.
var wpConfigDefineRe = regexp.MustCompile(`(?m)^\s*define\(\s*['"](DB_NAME|DB_USER|DB_PASSWORD|DB_HOST)['"]\s*,\s*['"]([^'"]*)['"]\s*\)\s*;`)

// phpSingleQuoteEscape escapes a value for a PHP single-quoted string literal:
// backslash then single-quote (PHP only special-cases \ and ' inside '...').
func phpSingleQuoteEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

// RewriteWPConfigDB rewrites the DB_NAME/DB_USER/DB_PASSWORD/DB_HOST define()s
// in a wp-config.php body to the destination Jabali credentials, returning the
// new body and whether anything changed. It is PURE (no I/O) — the caller reads
// the file and writes it back (via the agent), exactly like the cPanel restore.
// Shared by the cPanel restore and the wordpress_ssh import so both apply the
// identical, proven rewrite. Values are PHP single-quote escaped.
// RewriteWPConfigDB rewrites all four DB constants.
func RewriteWPConfigDB(text, dbName, dbUser, dbPass, dbHost string) (string, bool) {
	return RewriteWPConfigDBFields(text, dbName, &dbUser, &dbPass, dbHost)
}

// RewriteWPConfigDBFields rewrites the DB constants, leaving DB_USER and/or
// DB_PASSWORD alone when the corresponding pointer is nil.
//
// Needed because a migrated site can legitimately have to KEEP the user and
// password already in its config: under --preserve-source-state a compat user
// restores the source password hash onto a user named exactly as the config
// references, so the original user + password are what authenticate and any
// value written in their place would break the site (JAB-207 / GH #723). The
// DB name still has to change — jabali namespaces it (`<account>_<db>`) — and
// DB_HOST is normalised to localhost; only the credentials the recreated compat
// user backs are held. Writing credentials that do not work is strictly worse
// than writing none.
func RewriteWPConfigDBFields(text, dbName string, dbUser *string, dbPass *string, dbHost string) (string, bool) {
	if !strings.Contains(text, "DB_NAME") {
		return text, false
	}
	out := wpConfigDefineRe.ReplaceAllStringFunc(text, func(line string) string {
		m := wpConfigDefineRe.FindStringSubmatch(line)
		switch m[1] {
		case "DB_NAME":
			return fmt.Sprintf("define('DB_NAME', '%s');", phpSingleQuoteEscape(dbName))
		case "DB_USER":
			if dbUser == nil {
				return line // keep whatever the source config had
			}
			return fmt.Sprintf("define('DB_USER', '%s');", phpSingleQuoteEscape(*dbUser))
		case "DB_PASSWORD":
			if dbPass == nil {
				return line // keep whatever the source config had
			}
			return fmt.Sprintf("define('DB_PASSWORD', '%s');", phpSingleQuoteEscape(*dbPass))
		case "DB_HOST":
			return fmt.Sprintf("define('DB_HOST', '%s');", phpSingleQuoteEscape(dbHost))
		}
		return line
	})
	return out, out != text
}

// StripJabaliCacheBlock removes the panel-managed "// BEGIN/END Jabali WP Cache"
// fenced block (the JABALI_CACHE_* constants) from a wp-config.php body. On a
// migration/clone/restore the SOURCE's block pins the source tenant's Redis
// prefix + ACL token; carrying it verbatim would make the migrated site's
// object-cache drop-in read/write the SOURCE's Redis namespace (cross-tenant
// bleed). Stripping leaves the dest with a cold, fresh cache until the panel
// re-enables it and re-stamps the correct per-tenant constants (GH #621).
func StripJabaliCacheBlock(content string) string {
	const begin = "// BEGIN Jabali WP Cache"
	const end = "// END Jabali WP Cache"
	for {
		bi := strings.Index(content, begin)
		if bi < 0 {
			return content
		}
		ei := strings.Index(content[bi:], end)
		if ei < 0 {
			return content
		}
		ei = bi + ei + len(end)
		// also swallow a trailing newline left by the block
		if ei < len(content) && content[ei] == '\n' {
			ei++
		}
		content = content[:bi] + content[ei:]
	}
}
