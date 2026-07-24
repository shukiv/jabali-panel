package hestiacp

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DBCredential is one source MySQL user recovered from a HestiaCP backup's
// per-database db.conf. HestiaCP stores the account's mysql_native_password
// hash in the (VestaCP-legacy-named) MD5= field — captured at DB-create time
// via `SHOW CREATE USER` / `SHOW GRANTS` (func/db.sh), NOT an md5 of the
// plaintext. Recreating the user on the destination with this hash lets a
// migrated app keep working with its hardcoded db creds (config.php /
// wp-config.php) — the GH #633 fix (offline Hestia DB-password carry-over).
type DBCredential struct {
	DBName string // Hestia DB name, e.g. "olduser_wp" — also the grant target + dump base name
	User   string // Hestia DB user, e.g. "olduser_wp"
	Hash   string // normalised '*'+40-hex mysql_native_password hash
}

// key='value' extractor for a db.conf line
// (DB='olduser_wp' DBUSER='olduser_wp' MD5='*ABC…' HOST='localhost' TYPE='mysql' …).
var hestiaDBConfKVRe = regexp.MustCompile(`([A-Z0-9_]+)='([^']*)'`)

// old-style mysql_native_password hash: '*' + 40 hex.
var hestiaNativeHashRe = regexp.MustCompile(`^\*[0-9A-Fa-f]{40}$`)

// ParseDBCredentials walks the extracted Hestia backup for every
// db/<db>/hestia/db.conf (plus an aggregated hestia/db.conf when present) and
// returns the source MySQL users that carry a USABLE native password hash.
// Non-mysql engines (pgsql), missing/empty hashes, and hash formats jabali
// can't recreate (ed25519, auth sockets, …) are dropped — the caller falls
// back to a fresh password for those. Best-effort: a malformed file is
// skipped, never fatal.
func ParseDBCredentials(extractDir string) []DBCredential {
	matches, _ := filepath.Glob(filepath.Join(extractDir, "db", "*", "hestia", "db.conf"))
	// Some layouts also drop the whole-user db.conf at the top-level hestia/
	// dir — parse it too (deduped by DB|user below).
	if agg := filepath.Join(extractDir, "hestia", "db.conf"); fileExists(agg) {
		matches = append(matches, agg)
	}

	seen := map[string]bool{}
	var out []DBCredential
	for _, path := range matches {
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			kv := map[string]string{}
			for _, m := range hestiaDBConfKVRe.FindAllStringSubmatch(line, -1) {
				kv[m[1]] = m[2]
			}
			if !strings.EqualFold(kv["TYPE"], "mysql") {
				continue // jabali's native-hash recreate path is mysql only
			}
			db, user := kv["DB"], kv["DBUSER"]
			hash := normalizeHestiaDBHash(kv["MD5"])
			if db == "" || user == "" || hash == "" {
				continue
			}
			key := db + "|" + user
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, DBCredential{DBName: db, User: user, Hash: hash})
		}
	}
	return out
}

// normalizeHestiaDBHash converts HestiaCP's MD5= field to the canonical
// '*'+40-hex mysql_native_password form jabali's db_user.create accepts, or ""
// when the value isn't a recreatable native hash.
//
// Two on-disk forms (func/db.sh):
//   - "*ABCD…" (41 chars)  — MySQL / MariaDB `SHOW GRANTS` form; used verbatim.
//   - "0x2A41…"            — MariaDB `print_identified_with_as_hex=ON` form: the
//     hex encoding of the *XXXX string (0x2A = '*'), so hex-decoding yields the
//     same "*ABCD…" value.
func normalizeHestiaDBHash(raw string) string {
	h := strings.TrimSpace(raw)
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "0x") || strings.HasPrefix(h, "0X") {
		decoded, err := hex.DecodeString(h[2:])
		if err != nil {
			return ""
		}
		h = string(decoded)
	}
	if hestiaNativeHashRe.MatchString(h) {
		return h
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
