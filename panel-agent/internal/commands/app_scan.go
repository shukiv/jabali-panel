// app.scan — walk /home/<user>/domains/*/public_html/ (+ depth-1
// subdirs) for known CMS / app signatures + return what we found.
// Panel-side handler matches against the existing application_installs
// table + INSERTs any unregistered hits.

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type appScanParams struct {
	Username string `json:"username"`
}

// appScanHit is one detected install. Fields map 1:1 onto the panel's
// ApplicationInstall row the handler will INSERT.
type appScanHit struct {
	Domain        string `json:"domain"`        // panel-side domain name (the docroot's parent dir)
	Subdirectory  string `json:"subdirectory"`  // "" for docroot root, e.g. "blog" for blog/
	AppType       string `json:"app_type"`      // wordpress | joomla | drupal | magento
	Version       string `json:"version,omitempty"`
	AdminUsername string `json:"admin_username,omitempty"` // best-effort detection (WP: wp_users)
}

type appScanResponse struct {
	Hits []appScanHit `json:"hits"`
}

func appScanHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p appScanParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse: " + err.Error()}
	}
	if !usernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid username"}
	}
	u, err := user.Lookup(p.Username)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "user not in /etc/passwd"}
	}
	domainsRoot := filepath.Join(u.HomeDir, "domains")
	entries, err := os.ReadDir(domainsRoot)
	if err != nil {
		// No domains dir = nothing to scan, not an error.
		return appScanResponse{}, nil
	}
	var hits []appScanHit
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dom := e.Name()
		docroot := filepath.Join(domainsRoot, dom, "public_html")
		// Docroot itself.
		if h := detectAt(dom, "", docroot); h != nil {
			hits = append(hits, *h)
		}
		// Depth-1 subdirs (max 100 to bound scan cost on bloated sites).
		subEntries, _ := os.ReadDir(docroot)
		count := 0
		for _, sub := range subEntries {
			if count >= 100 {
				break
			}
			if !sub.IsDir() || strings.HasPrefix(sub.Name(), ".") {
				continue
			}
			count++
			subPath := filepath.Join(docroot, sub.Name())
			if h := detectAt(dom, sub.Name(), subPath); h != nil {
				hits = append(hits, *h)
			}
		}
	}
	return appScanResponse{Hits: hits}, nil
}

// detectAt inspects a single directory for known signatures.
// Returns nil when nothing matches.
func detectAt(domain, subdir, dir string) *appScanHit {
	// WordPress: wp-config.php OR wp-load.php (some installs move
	// wp-config one level up but keep wp-load).
	if fileExists(filepath.Join(dir, "wp-config.php")) ||
		fileExists(filepath.Join(dir, "wp-load.php")) {
		hit := &appScanHit{
			Domain:       domain,
			Subdirectory: subdir,
			AppType:      "wordpress",
			Version:      readWPVersion(dir),
		}
		if admin := detectWPAdmin(dir); admin != "" {
			hit.AdminUsername = admin
		}
		return hit
	}
	// Joomla: configuration.php + libraries/src/Version.php (4.x)
	// or libraries/joomla/version.php (3.x).
	if fileExists(filepath.Join(dir, "configuration.php")) &&
		(dirExists(filepath.Join(dir, "libraries", "src")) ||
			dirExists(filepath.Join(dir, "libraries", "joomla"))) {
		return &appScanHit{
			Domain:       domain,
			Subdirectory: subdir,
			AppType:      "joomla",
			Version:      readJoomlaVersion(dir),
		}
	}
	// Drupal: sites/default/settings.php + core/lib/Drupal.php (8+)
	// or includes/bootstrap.inc (7.x).
	if fileExists(filepath.Join(dir, "sites", "default", "settings.php")) &&
		(fileExists(filepath.Join(dir, "core", "lib", "Drupal.php")) ||
			fileExists(filepath.Join(dir, "includes", "bootstrap.inc"))) {
		return &appScanHit{
			Domain:       domain,
			Subdirectory: subdir,
			AppType:      "drupal",
			Version:      readDrupalVersion(dir),
		}
	}
	// Magento 2: app/etc/env.php + composer.json with "magento/product-community-edition".
	if fileExists(filepath.Join(dir, "app", "etc", "env.php")) &&
		fileExists(filepath.Join(dir, "composer.json")) {
		return &appScanHit{
			Domain:       domain,
			Subdirectory: subdir,
			AppType:      "magento",
		}
	}
	return nil
}

var (
	wpVersionRe     = regexp.MustCompile(`\$wp_version\s*=\s*['"]([^'"]+)['"]`)
	drupalVersionRe = regexp.MustCompile(`const\s+VERSION\s*=\s*['"]([^'"]+)['"]`)
	joomla4Re       = regexp.MustCompile(`MAJOR_VERSION\s*=\s*(\d+)[\s\S]+?MINOR_VERSION\s*=\s*(\d+)[\s\S]+?PATCH_VERSION\s*=\s*(\d+)`)
)

func readWPVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "wp-includes", "version.php"))
	if err != nil {
		return ""
	}
	if m := wpVersionRe.FindSubmatch(b); len(m) == 2 {
		return string(m[1])
	}
	return ""
}

func readDrupalVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "core", "lib", "Drupal.php"))
	if err != nil {
		return ""
	}
	if m := drupalVersionRe.FindSubmatch(b); len(m) == 2 {
		return string(m[1])
	}
	return ""
}

func readJoomlaVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "libraries", "src", "Version.php"))
	if err != nil {
		// Joomla 3.x fallback
		b, err = os.ReadFile(filepath.Join(dir, "libraries", "joomla", "version.php"))
		if err != nil {
			return ""
		}
	}
	if m := joomla4Re.FindSubmatch(b); len(m) == 4 {
		return fmt.Sprintf("%s.%s.%s", m[1], m[2], m[3])
	}
	return ""
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func init() {
	Default.Register("app.scan", appScanHandler)
}


// detectWPAdmin parses wp-config.php for DB_NAME + $table_prefix +
// queries the local MariaDB (agent connects as root via unix socket)
// for the first administrator user. Empty string on any failure —
// the SSO template's WP_User_Query fallback handles missing admin
// names so this is opportunistic enrichment, never load-bearing.
func detectWPAdmin(docroot string) string {
	cfg, err := os.ReadFile(filepath.Join(docroot, "wp-config.php"))
	if err != nil {
		return ""
	}
	dbName := wpConfigGrab(cfg, "DB_NAME")
	prefix := wpConfigGrabPrefix(cfg)
	if dbName == "" || prefix == "" {
		return ""
	}
	// Only allow [a-z0-9_] in both — these become part of a SQL
	// identifier (back-tick quoted but defence-in-depth).
	if !safeMySQLIdent(dbName) || !safeMySQLIdent(prefix) {
		return ""
	}
	q := fmt.Sprintf(
		"SELECT u.user_login FROM `%s`.`%susers` u JOIN `%s`.`%susermeta` m ON u.ID = m.user_id WHERE m.meta_key = '%scapabilities' AND m.meta_value LIKE '%%\"administrator\"%%' ORDER BY u.ID ASC LIMIT 1",
		dbName, prefix, dbName, prefix, prefix,
	)
	out, err := execCommand("mysql", "-BN", "-e", q).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var (
	wpConfigDefineRe = regexp.MustCompile(`define\s*\(\s*['"](DB_NAME|DB_USER|DB_PASSWORD|DB_HOST)['"]\s*,\s*['"]([^'"]*)['"]\s*\)`)
	wpConfigPrefixRe = regexp.MustCompile(`\$table_prefix\s*=\s*['"]([A-Za-z0-9_]+)['"]`)
)

func wpConfigGrab(cfg []byte, key string) string {
	for _, m := range wpConfigDefineRe.FindAllSubmatch(cfg, -1) {
		if string(m[1]) == key {
			return string(m[2])
		}
	}
	return ""
}

func wpConfigGrabPrefix(cfg []byte) string {
	if m := wpConfigPrefixRe.FindSubmatch(cfg); len(m) == 2 {
		return string(m[1])
	}
	return ""
}

func safeMySQLIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return false
		}
	}
	return true
}
