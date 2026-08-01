// restore_appconfigs.go — M35.8 P8. After ImportDatabases creates
// new (db_name, db_user, db_pass) triples + ImportHomeSplit rsync's
// app files into /home/<user>/domains/<dom>/public_html/, every
// WordPress/Drupal/Joomla/Magento app's config file still points
// at the SOURCE db credentials (cpanel preserved the names but the
// passwords are new). This writer scans each per-domain docroot
// for known app signatures + splices the new triple in via
// agent.files.read + files.write.
//
// Today's matchers (more to follow as we observe live tarballs):
//   wp-config.php        — WordPress define('DB_NAME', ...) lines
//   configuration.php    — Joomla public $db, $user, $password
//   sites/default/settings.php — Drupal databases array
//   app/etc/env.php      — Magento 2 'db' => ['connection' => ...]
//   config.php           — ITFlow $dbhost / $dbusername / $dbpassword / $database
//
// Strategy: read file content, regex-replace the value (preserving
// surrounding syntax), write back. If a single file matches more
// than one app (rare collision) we apply every matcher — they're
// scoped to their own marker strings so they don't clobber each
// other.

package cpanel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate"
)

// AppConfigsResult tallies per-app rewrites.
type AppConfigsResult struct {
	WordPress     int      // wp-config.php files rewritten
	Joomla        int      // configuration.php files rewritten
	Drupal        int      // settings.php files rewritten
	Magento       int      // app/etc/env.php files rewritten
	ITFlow        int      // config.php files rewritten
	CachesCleared int      // cache directories wiped (WP Rocket / LiteSpeed / W3TC / Joomla / Drupal etc.)
	PhpIniFixed   int      // php.ini/.user.ini files whose out-of-jail path directives were neutralized
	Skipped       []string // human-readable reasons (file unreadable, no match, etc.)
}

// ImportAppConfigs walks every domain docroot under
// /home/<targetUsername>/domains/*/public_html/ and rewrites any
// app-config file it finds with the new (db_name, db_user, db_pass)
// from `creds`. `creds` is keyed by db_name; the rewriter picks the
// first credential that matches the file's existing DB_NAME line
// (modern cpanel migrations preserve names, so the match is a
// straight string lookup).
func ImportAppConfigs(
	ctx context.Context,
	agentCli agent.AgentInterface,
	targetUserID, targetUsername string,
	creds map[string]DBCredential,
	docroots []string,
) (*AppConfigsResult, error) {
	res := &AppConfigsResult{}
	if agentCli == nil || len(creds) == 0 {
		return res, nil
	}
	// JAB-32: scan each domain's ACTUAL docroot. cPanel/DA use
	// /home/<user>/domains/<dom>/public_html; HestiaCP uses
	// /home/<user>/web/<dom>/public_html. When the caller supplies the
	// resolved docroots (parsed.DocRoots) use them; otherwise fall back to the
	// legacy cPanel scan so pre-existing callers stay unaffected.
	docrootList := docroots
	if len(docrootList) == 0 {
		root := filepath.Join("/home", targetUsername, "domains")
		entries, err := os.ReadDir(root)
		if err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("appconfigs_read_root:%s:%v", root, err))
			return res, nil
		}
		for _, e := range entries {
			if e.IsDir() {
				docrootList = append(docrootList, filepath.Join(root, e.Name(), "public_html"))
			}
		}
	}

	for _, docroot := range docrootList {
		// WordPress
		for _, candidate := range []string{
			filepath.Join(docroot, "wp-config.php"),
			filepath.Join(docroot, "wp", "wp-config.php"),
		} {
			if n, sk := rewriteOne(ctx, agentCli, targetUserID, targetUsername, candidate, creds, rewriteWordPress); n > 0 {
				res.WordPress += n
			} else if sk != "" {
				res.Skipped = append(res.Skipped, sk)
			}
		}
		// Joomla
		joomla := filepath.Join(docroot, "configuration.php")
		if n, sk := rewriteOne(ctx, agentCli, targetUserID, targetUsername, joomla, creds, rewriteJoomla); n > 0 {
			res.Joomla += n
		} else if sk != "" {
			res.Skipped = append(res.Skipped, sk)
		}
		// Drupal
		drupal := filepath.Join(docroot, "sites", "default", "settings.php")
		if n, sk := rewriteOne(ctx, agentCli, targetUserID, targetUsername, drupal, creds, rewriteDrupal); n > 0 {
			res.Drupal += n
		} else if sk != "" {
			res.Skipped = append(res.Skipped, sk)
		}
		// Magento 2
		magento := filepath.Join(docroot, "app", "etc", "env.php")
		if n, sk := rewriteOne(ctx, agentCli, targetUserID, targetUsername, magento, creds, rewriteMagento); n > 0 {
			res.Magento += n
		} else if sk != "" {
			res.Skipped = append(res.Skipped, sk)
		}

		// ITFlow (GH #723). config.php is a very common filename, so
		// rewriteITFlow refuses anything without ITFlow's own signature —
		// see the guard there.
		itflow := filepath.Join(docroot, "config.php")
		if n, sk := rewriteOne(ctx, agentCli, targetUserID, targetUsername, itflow, creds, rewriteITFlow); n > 0 {
			res.ITFlow += n
		} else if sk != "" {
			res.Skipped = append(res.Skipped, sk)
		}

		// php.ini / .user.ini sanitization: a verbatim-copied cPanel ini
		// often points session.save_path / open_basedir / *_tmp_dir /
		// error_log / extension_dir at a path outside jabali's per-user
		// open_basedir jail, which breaks every request under the pool.
		// Neutralize only the out-of-jail path directives; preserve
		// benign tunables (memory_limit, upload_max_filesize, …).
		for _, ini := range []string{
			filepath.Join(docroot, "php.ini"),
			filepath.Join(docroot, ".user.ini"),
		} {
			n, sk := sanitizePhpIniFile(ctx, agentCli, targetUserID, targetUsername, ini, docroot, res)
			if n > 0 {
				res.PhpIniFixed += n
			} else if sk != "" {
				res.Skipped = append(res.Skipped, sk)
			}
		}

		// Cache-plugin / framework-cache cleanup. cpanel-side
		// caches reference the SOURCE host's URL, hash IDs that
		// don't exist post-rsync, or absolute paths from the source
		// box. Easier to nuke + let the app regenerate on next
		// request than to chase per-plugin invalidation APIs.
		for _, victim := range cachePathsForDocroot(docroot) {
			if _, err := os.Stat(victim); err != nil {
				continue
			}
			// Best-effort RemoveAll via agent.files.delete (operator
			// scoped, audited). Direct os.RemoveAll would write as
			// jabali user; agent dispatch makes sure the per-user
			// scope guard runs + the audit log records the wipe.
			_, derr := agentCli.Call(ctx, "files.delete", map[string]any{
				"user_id":  targetUserID,
				"username": targetUsername,
				"path":     victim,
			})
			if derr != nil {
				// Fall back to direct RemoveAll — this code path
				// runs as the panel-api user which has rwx on
				// /home/<user>/... via the www-data group.
				if rmErr := os.RemoveAll(victim); rmErr != nil {
					res.Skipped = append(res.Skipped, fmt.Sprintf("cache_clear_skip:%s:%v", victim, rmErr))
					continue
				}
			}
			res.CachesCleared++
		}
	}
	return res, nil
}

// cachePathsForDocroot returns every known cache-plugin / framework
// cache dir relative to a per-domain docroot. Order doesn't matter;
// missing entries are skipped silently. Updates as new plugins
// surface in operator field reports.
func cachePathsForDocroot(docroot string) []string {
	return []string{
		// WordPress
		filepath.Join(docroot, "wp-content", "cache"),
		filepath.Join(docroot, "wp-content", "advanced-cache.php"),
		filepath.Join(docroot, "wp-content", "wp-cache-config.php"),
		filepath.Join(docroot, "wp-content", "object-cache.php"),
		filepath.Join(docroot, "wp-content", "wp-rocket-config"),
		filepath.Join(docroot, "wp-content", "litespeed"),
		filepath.Join(docroot, "wp-content", "w3tc-config"),
		filepath.Join(docroot, "wp-content", "plugins", "wp-fastest-cache", "cache"),
		filepath.Join(docroot, "wp-content", "uploads", "cache"),
		// Joomla
		filepath.Join(docroot, "cache"),
		filepath.Join(docroot, "administrator", "cache"),
		filepath.Join(docroot, "tmp"),
		// Drupal (8+: per-files-dir cache; legacy: cache/ dir at root)
		filepath.Join(docroot, "sites", "default", "files", "css"),
		filepath.Join(docroot, "sites", "default", "files", "js"),
		filepath.Join(docroot, "sites", "default", "files", "php"),
		filepath.Join(docroot, "sites", "default", "files", "styles"),
		// Magento 2 var/cache + var/page_cache + generated/
		filepath.Join(docroot, "..", "var", "cache"),
		filepath.Join(docroot, "..", "var", "page_cache"),
		filepath.Join(docroot, "..", "generated"),
		// Generic CDN-pull cache plugins (Hyper Cache, Comet Cache, …)
		filepath.Join(docroot, "wp-content", "plugins", "hyper-cache", "cache"),
		filepath.Join(docroot, "wp-content", "plugins", "comet-cache", "cache"),
	}
}

// rewriteOne reads `path` via agent.files.read, runs the rewriter,
// writes it back. Returns (count, skipReason). count is 0 + skip ""
// when the file doesn't exist (silent skip — most domains aren't
// every CMS). count is 1 when the rewriter mutated bytes; count is
// 0 + skip="…" on read/write error or no-op.
func rewriteOne(
	ctx context.Context,
	agentCli agent.AgentInterface,
	targetUserID, targetUsername, path string,
	creds map[string]DBCredential,
	rewriter func(text string, creds map[string]DBCredential) (string, bool),
) (int, string) {
	if _, err := os.Stat(path); err != nil {
		return 0, ""
	}
	raw, err := agentCli.Call(ctx, "files.read", map[string]any{
		"user_id":  targetUserID,
		"username": targetUsername,
		"path":     path, // absolute — agent's filesafe.Resolve rejects relative
	})
	if err != nil {
		return 0, fmt.Sprintf("appconfig_read_skip:%s:%v", path, err)
	}
	var r struct {
		Content  string `json:"content"`
		IsBinary bool   `json:"is_binary"`
	}
	if jErr := json.Unmarshal(raw, &r); jErr != nil || r.IsBinary {
		return 0, fmt.Sprintf("appconfig_decode_skip:%s", path)
	}
	updated, changed := rewriter(r.Content, creds)
	if !changed {
		return 0, ""
	}
	if _, err := agentCli.Call(ctx, "files.write", map[string]any{
		"user_id":  targetUserID,
		"username": targetUsername,
		"path":     path,
		"content":  updated,
		"mode":     "overwrite",
	}); err != nil {
		return 0, fmt.Sprintf("appconfig_write_skip:%s:%v", path, err)
	}
	return 1, ""
}

// ---- per-app rewriters ----

var (
	wpDefineRe = regexp.MustCompile(`(?m)^\s*define\(\s*['"](DB_NAME|DB_USER|DB_PASSWORD|DB_HOST)['"]\s*,\s*['"]([^'"]*)['"]\s*\)\s*;`)
)

// sanitizePhpIniFile reads a migrated php.ini/.user.ini via the agent,
// neutralizes path directives pointing outside the user's open_basedir
// jail (sanitizePhpIni), and writes it back. Returns (1, "") on a real
// rewrite, (0, "") when absent/clean, (0, note) on an error. The per-
// directive changes are appended to res.Skipped as manifest notes.
func sanitizePhpIniFile(ctx context.Context, agentCli agent.AgentInterface, targetUserID, targetUsername, path, docroot string, res *AppConfigsResult) (int, string) {
	if _, err := os.Stat(path); err != nil {
		return 0, ""
	}
	raw, err := agentCli.Call(ctx, "files.read", map[string]any{
		"user_id":  targetUserID,
		"username": targetUsername,
		"path":     path,
	})
	if err != nil {
		return 0, fmt.Sprintf("php_ini_read_skip:%s:%v", path, err)
	}
	var r struct {
		Content  string `json:"content"`
		IsBinary bool   `json:"is_binary"`
	}
	if jErr := json.Unmarshal(raw, &r); jErr != nil || r.IsBinary {
		return 0, fmt.Sprintf("php_ini_decode_skip:%s", path)
	}
	updated, notes, changed := sanitizePhpIni(r.Content, targetUsername, docroot, nil)
	if !changed {
		return 0, ""
	}
	if _, err := agentCli.Call(ctx, "files.write", map[string]any{
		"user_id":  targetUserID,
		"username": targetUsername,
		"path":     path,
		"content":  updated,
		"mode":     "overwrite",
	}); err != nil {
		return 0, fmt.Sprintf("php_ini_write_skip:%s:%v", path, err)
	}
	for _, n := range notes {
		res.Skipped = append(res.Skipped, fmt.Sprintf("php_ini_sanitized:%s: %s", path, n))
	}
	return 1, ""
}

func rewriteWordPress(text string, creds map[string]DBCredential) (string, bool) {
	if !strings.Contains(text, "DB_NAME") {
		return text, false
	}
	// Find the source DB_NAME first so we can match it against a credential.
	var sourceDB string
	for _, m := range wpDefineRe.FindAllStringSubmatch(text, -1) {
		if m[1] == "DB_NAME" {
			sourceDB = m[2]
			break
		}
	}
	creds2 := creds
	cred, ok := creds2[sourceDB]
	if !ok {
		// fallback — single-cred case: use the only entry
		if len(creds2) == 1 {
			for _, c := range creds2 {
				cred = c
				ok = true
			}
		}
	}
	if !ok {
		return text, false
	}
	// GH #647: delegate the actual rewrite to the shared migrate.RewriteWPConfigDB
	// so the cPanel restore and the wordpress_ssh import apply one identical,
	// proven rewriter (no drift). Behavior-preserving (same regex + php-escape,
	// DB_HOST -> localhost).
	// JAB-207: when a preserved compat user owns this MySQL account, the
	// original password in the config is the one that authenticates — pass nil
	// so DB_PASSWORD is left exactly as the source wrote it.
	pass := &cred.Password
	if cred.KeepSourcePassword {
		pass = nil
	}
	return migrate.RewriteWPConfigDBFields(text, cred.DBName, cred.DBUser, pass, "localhost")
}

var (
	joomlaAssignRe = regexp.MustCompile(`(?m)public \$(db|user|password|host)\s*=\s*['"]([^'"]*)['"]\s*;`)
)

func rewriteJoomla(text string, creds map[string]DBCredential) (string, bool) {
	if !strings.Contains(text, "public $db") {
		return text, false
	}
	var sourceDB string
	for _, m := range joomlaAssignRe.FindAllStringSubmatch(text, -1) {
		if m[1] == "db" {
			sourceDB = m[2]
			break
		}
	}
	cred, ok := creds[sourceDB]
	if !ok && len(creds) == 1 {
		for _, c := range creds {
			cred = c
			ok = true
		}
	}
	if !ok {
		return text, false
	}
	out := joomlaAssignRe.ReplaceAllStringFunc(text, func(line string) string {
		m := joomlaAssignRe.FindStringSubmatch(line)
		switch m[1] {
		case "db":
			return fmt.Sprintf("public $db = '%s';", phpEscape(cred.DBName))
		case "user":
			return fmt.Sprintf("public $user = '%s';", phpEscape(cred.DBUser))
		case "password":
			if cred.KeepSourcePassword {
				return m[0] // JAB-207: preserved compat hash owns this account
			}
			return fmt.Sprintf("public $password = '%s';", phpEscape(cred.Password))
		case "host":
			return "public $host = 'localhost';"
		}
		return line
	})
	return out, out != text
}

var (
	// itflowAssignRe matches ITFlow's config.php lines. Its setup writes them
	// with var_export(), i.e. single-quoted, but accept double quotes too since
	// operators hand-edit this file.
	itflowAssignRe = regexp.MustCompile(`(?m)^\s*\$(dbhost|dbusername|dbpassword|database)\s*=\s*['"]([^'"]*)['"]\s*;`)

	// itflowSignatureRe is the guard. "config.php" is one of the most common
	// filenames in PHP hosting, and a false positive here would corrupt an
	// unrelated app's config. ITFlow's setup always emits this connect line
	// verbatim right after the four assignments, so requiring it means we only
	// touch files ITFlow itself wrote.
	itflowSignatureRe = regexp.MustCompile(`mysqli_connect\s*\(\s*\$dbhost\s*,\s*\$dbusername\s*,\s*\$dbpassword\s*,\s*\$database\s*\)`)
)

// rewriteITFlow points an ITFlow config.php at the destination credentials
// (GH #723). Jabali ships an ITFlow installer, so a migrated ITFlow site left
// on the source's DB name — which no longer exists once jabali namespaces it —
// is a site that simply doesn't load.
//
// Mirrors rewriteJoomla: match the source DB name to pick the right credential
// on a multi-database account, falling back to the sole credential when there
// is exactly one.
func rewriteITFlow(text string, creds map[string]DBCredential) (string, bool) {
	// Guard first: never rewrite a config.php that isn't ITFlow's.
	if !itflowSignatureRe.MatchString(text) {
		return text, false
	}
	if !itflowAssignRe.MatchString(text) {
		return text, false
	}
	var sourceDB string
	for _, m := range itflowAssignRe.FindAllStringSubmatch(text, -1) {
		if m[1] == "database" {
			sourceDB = m[2]
			break
		}
	}
	cred, ok := creds[sourceDB]
	if !ok && len(creds) == 1 {
		for _, c := range creds {
			cred = c
			ok = true
		}
	}
	if !ok {
		return text, false
	}
	out := itflowAssignRe.ReplaceAllStringFunc(text, func(line string) string {
		m := itflowAssignRe.FindStringSubmatch(line)
		switch m[1] {
		case "database":
			return fmt.Sprintf("$database = '%s';", phpEscape(cred.DBName))
		case "dbusername":
			return fmt.Sprintf("$dbusername = '%s';", phpEscape(cred.DBUser))
		case "dbpassword":
			return fmt.Sprintf("$dbpassword = '%s';", phpEscape(cred.Password))
		case "dbhost":
			return "$dbhost = 'localhost';"
		}
		return line
	})
	return out, out != text
}

var (
	drupalKVRe = regexp.MustCompile(`(?m)'(database|username|password|host)'\s*=>\s*['"]([^'"]*)['"]`)
)

func rewriteDrupal(text string, creds map[string]DBCredential) (string, bool) {
	if !strings.Contains(text, "'database'") {
		return text, false
	}
	var sourceDB string
	for _, m := range drupalKVRe.FindAllStringSubmatch(text, -1) {
		if m[1] == "database" {
			sourceDB = m[2]
			break
		}
	}
	cred, ok := creds[sourceDB]
	if !ok && len(creds) == 1 {
		for _, c := range creds {
			cred = c
			ok = true
		}
	}
	if !ok {
		return text, false
	}
	out := drupalKVRe.ReplaceAllStringFunc(text, func(line string) string {
		m := drupalKVRe.FindStringSubmatch(line)
		switch m[1] {
		case "database":
			return fmt.Sprintf("'database' => '%s'", phpEscape(cred.DBName))
		case "username":
			return fmt.Sprintf("'username' => '%s'", phpEscape(cred.DBUser))
		case "password":
			return fmt.Sprintf("'password' => '%s'", phpEscape(cred.Password))
		case "host":
			return "'host' => 'localhost'"
		}
		return line
	})
	return out, out != text
}

// Magento env.php is also PHP-array form but its dbname / username
// / password keys live under db->connection->default. Reuse the
// same key-value regex; it's lenient enough.
var (
	magentoKVRe = regexp.MustCompile(`(?m)'(dbname|username|password|host)'\s*=>\s*['"]([^'"]*)['"]`)
)

func rewriteMagento(text string, creds map[string]DBCredential) (string, bool) {
	if !strings.Contains(text, "'connection'") || !strings.Contains(text, "'dbname'") {
		return text, false
	}
	var sourceDB string
	for _, m := range magentoKVRe.FindAllStringSubmatch(text, -1) {
		if m[1] == "dbname" {
			sourceDB = m[2]
			break
		}
	}
	cred, ok := creds[sourceDB]
	if !ok && len(creds) == 1 {
		for _, c := range creds {
			cred = c
			ok = true
		}
	}
	if !ok {
		return text, false
	}
	out := magentoKVRe.ReplaceAllStringFunc(text, func(line string) string {
		m := magentoKVRe.FindStringSubmatch(line)
		switch m[1] {
		case "dbname":
			return fmt.Sprintf("'dbname' => '%s'", phpEscape(cred.DBName))
		case "username":
			return fmt.Sprintf("'username' => '%s'", phpEscape(cred.DBUser))
		case "password":
			return fmt.Sprintf("'password' => '%s'", phpEscape(cred.Password))
		case "host":
			return "'host' => 'localhost'"
		}
		return line
	})
	return out, out != text
}

// phpEscape quotes a PHP single-quoted string body: escape ' and \.
// Sufficient for db credentials which are alnum + ULID + bcrypt-safe
// chars — no embedded quotes expected, defensive only.
func phpEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}
