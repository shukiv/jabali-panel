package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// flarumInstallReq is the input shape for the Flarum installer. Same
// RequiresDB=true envelope as phpBB/Drupal.
type flarumInstallReq struct {
	AppType      string `json:"app_type"`
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory"`
	SiteURL      string `json:"site_url"`
	UseWWW       bool   `json:"use_www"`
	DBName       string `json:"db_name"`
	DBUser       string `json:"db_user"`
	DBPassword   string `json:"db_password"`
	DBHost       string `json:"db_host"`
	SiteTitle    string `json:"site_title"`
	AdminUser    string `json:"admin_user"`
	AdminPass    string `json:"admin_pass"`
	AdminEmail   string `json:"admin_email"`
}

type flarumInstallResp struct {
	Version string `json:"version"`
}

// flarumVersion is the upstream Flarum line this build targets. The
// prebuilt archive tracks the rolling v1.x tip; the exact release is
// pinned via flarumPkgCommit below.
const flarumVersion = "1.x"

// flarumPkgCommit is the immutable flarum/installation-packages commit
// the archive URL is pinned to. raw.githubusercontent serves any
// commit, so pinning the commit (instead of `main`) freezes the bytes —
// otherwise flarumTarballSHA256 would break on the next upstream
// release that regenerates the rolling v1.x archive. Bump commit + SHA
// together.
const flarumPkgCommit = "c713d4d17963f37547bc750bca0d72c005677cb4"

// flarumTarballURL is the "no-public-dir", PHP 8.4 prebuilt archive.
// vendor/ and the extension-manager are bundled, so there's no
// composer step. The no-public-dir variant flattens Flarum's default
// public/ webroot so the install root IS the docroot — that keeps it
// inside jabali's docroot+subdir model (see flarumNginxConf for the
// sensitive-path hardening this layout requires).
//
// PHP 8.4 matches jabali's default pool. The archive's composer
// platform check requires php >= 8.4, satisfied by jabali's 8.4/8.5
// pools.
var flarumTarballURL = fmt.Sprintf(
	"https://raw.githubusercontent.com/flarum/installation-packages/%s/packages/v1.x/flarum-v1.x-no-public-dir-php8.4.tar.gz",
	flarumPkgCommit,
)

// flarumTarballSHA256 is the SHA-256 of the commit-pinned tarball.
// Immutable as long as flarumPkgCommit is fixed.
const flarumTarballSHA256 = "9e29fdb67c13ed5e35118f4818e792c93b3d9d2abd005ea8eb7a7eea14f12a32"

// flarumAdminUserPattern — Flarum usernames are alnum + dash/underscore
// (it rejects dots and @). The API generates a 6-letter name, so this
// only guards against a malformed override.
var flarumAdminUserPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,30}$`)

func computeFlarumInstallPath(docroot, subdirectory string) string {
	if subdirectory == "" {
		return docroot
	}
	return filepath.Join(docroot, subdirectory)
}

func downloadFlarumTarball(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, flarumTarballURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", flarumTarballURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", flarumTarballURL, resp.StatusCode)
	}
	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}

func verifyFlarumSHA256(path string) error {
	if flarumTarballSHA256 == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, flarumTarballSHA256) {
		return fmt.Errorf("flarum tarball sha256 mismatch: got %s want %s", got, flarumTarballSHA256)
	}
	return nil
}

// extractFlarumTarball untars the archive into installPath. The
// no-public-dir archive has its files at the archive ROOT (index.php,
// flarum, vendor/, site.php …) with NO top-level wrapper directory, so
// — unlike Drupal/phpBB — there is NO --strip-components.
func extractFlarumTarball(ctx context.Context, osUser, tarballPath, installPath string) error {
	cmd := buildSystemdRunCmd(ctx, osUser,
		"tar",
		"--extract",
		"--gzip",
		"--file", tarballPath,
		"--directory", installPath,
	)
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("tar extract: %w (output: %s)", err, truncateStr(string(out), 512))
	}
	return nil
}

// writeFlarumInstallerYAML renders the config Flarum's CLI installer
// reads (FileDataProvider accepts JSON or YAML). Single-quoted scalars
// throughout so user strings (title, password) can't break the YAML
// structure or be coerced to booleans. The DB is dedicated per install,
// so the table prefix is empty.
func writeFlarumInstallerYAML(req flarumInstallReq, dest, baseURL, dbHost string) error {
	yaml := "debug: false\n" +
		"baseUrl: " + yamlSingleQuoted(baseURL) + "\n" +
		"databaseConfiguration:\n" +
		"  driver: 'mysql'\n" +
		"  host: " + yamlSingleQuoted(dbHost) + "\n" +
		"  port: 3306\n" +
		"  database: " + yamlSingleQuoted(req.DBName) + "\n" +
		"  username: " + yamlSingleQuoted(req.DBUser) + "\n" +
		"  password: " + yamlSingleQuoted(req.DBPassword) + "\n" +
		"  prefix: ''\n" +
		"adminUser:\n" +
		"  username: " + yamlSingleQuoted(req.AdminUser) + "\n" +
		"  password: " + yamlSingleQuoted(req.AdminPass) + "\n" +
		"  email: " + yamlSingleQuoted(req.AdminEmail) + "\n" +
		"settings:\n" +
		"  forum_title: " + yamlSingleQuoted(req.SiteTitle) + "\n"
	return os.WriteFile(dest, []byte(yaml), 0o600)
}

// runFlarumCLIInstaller drives `php flarum install -f <config>`. The
// `flarum` console (at installPath/flarum) runs the install migration +
// seeds, writes config.php with the runtime DB credentials, creates the
// admin user, and publishes assets to assets/. cwd MUST be installPath
// because the `flarum` script bootstraps via `require './site.php'`
// (cwd-relative).
func runFlarumCLIInstaller(ctx context.Context, osUser, installPath, configPath string) error {
	args := []string{
		"--working-directory=" + installPath,
		phpCLIFor(osUser),
		"flarum",
		"install",
		"-f", configPath,
	}
	cmd := buildSystemdRunCmd(ctx, osUser, args...)
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("php flarum install: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	return nil
}

// ---- nginx hardening for Flarum's no-public-dir layout ----------------

// flarumSnippetPath is where an install's nginx include lives. Docroot
// installs use the "root" slug (subdir installs use the subdir name) so
// the docroot snippet has a real, removable filename (removeAppRewrite's
// snippetPath would produce "flarum-.conf" for the empty subdir).
func flarumSnippetPath(domain, subdir string) string {
	slug := subdir
	if slug == "" {
		slug = "root"
	}
	return filepath.Join(nginxJabaliDir, domain, "flarum-"+slug+".conf")
}

// flarumNginxConf renders the per-install include. Flarum's flattened
// webroot exposes config.php / storage/ / vendor/ / the flarum CLI to
// the web — these are the deny rules from Flarum's own .nginx.conf.
//
// `=` (exact) and `^~` (prefix, halts regex search) BEAT the domain
// vhost's earlier `~ \.php$` regex (nginx precedence: = > ^~ > ~/~* >
// plain prefix), so /vendor/*.php can't be executed and /storage/*.log
// can't be read — even though the vhost's php handler is defined first.
// For subdir installs the longer `^~ /<sub>/vendor/` wins over the
// `^~ /<sub>/` front-controller block, so the deny still applies.
func flarumNginxConf(osUser, subdir string) string {
	p := ""
	if subdir != "" {
		p = "/" + subdir
	}
	var b strings.Builder
	b.WriteString("# Auto-generated by jabali-agent. Do not edit.\n")
	b.WriteString("# Flarum sensitive-path hardening (no-public-dir layout).\n")
	for _, f := range []string{"/config.php", "/flarum", "/composer.json", "/composer.lock", "/.env"} {
		fmt.Fprintf(&b, "location = %s%s { deny all; return 404; }\n", p, f)
	}
	for _, d := range []string{"/vendor/", "/storage/", "/.git"} {
		fmt.Fprintf(&b, "location ^~ %s%s { deny all; return 404; }\n", p, d)
	}
	// Subdir installs need a front controller — the generic vhost's
	// try_files targets /index.php at the docroot, not /<sub>/index.php.
	// Docroot installs ride the generic vhost's `location /`.
	if subdir != "" {
		fmt.Fprintf(&b, "location ^~ %s/ {\n", p)
		b.WriteString("    location ~ \\.php$ {\n")
		fmt.Fprintf(&b, "        fastcgi_pass unix:/run/php/jabali-%s/fpm.sock;\n", osUser)
		b.WriteString("        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;\n")
		b.WriteString("        include fastcgi_params;\n")
		b.WriteString("    }\n")
		fmt.Fprintf(&b, "    try_files $uri $uri/ %s/index.php?$query_string;\n", p)
		b.WriteString("}\n")
	}
	return b.String()
}

// writeFlarumNginx writes the per-install include (deny block + subdir
// front controller) and reloads nginx. Unlike writeAppRewrite it does
// NOT early-return for docroot installs — the deny block is needed
// regardless of subdir.
func writeFlarumNginx(ctx context.Context, domain, osUser, subdir string) error {
	if !domainRegex.MatchString(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}
	if subdir != "" && !subdirSlugRE.MatchString(subdir) {
		return fmt.Errorf("invalid subdir slug %q", subdir)
	}
	if !osUserSafeRE.MatchString(osUser) {
		return fmt.Errorf("invalid os_user %q", osUser)
	}
	domainDir := filepath.Join(nginxJabaliDir, domain)
	if err := os.MkdirAll(domainDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", domainDir, err)
	}
	dest := flarumSnippetPath(domain, subdir)
	if err := os.WriteFile(dest, []byte(flarumNginxConf(osUser, subdir)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return reloadNginxAfterSnippet(ctx, dest)
}

// removeFlarumNginx deletes the per-install include and reloads nginx.
// Missing file is fine — idempotent.
func removeFlarumNginx(ctx context.Context, domain, subdir string) error {
	if !domainRegex.MatchString(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}
	// Same containment as writeFlarumNginx: an unvalidated "../.." subdir
	// glued into the snippet filename would let filepath.Join escape the
	// domain dir and os.Remove (root) an arbitrary .conf.
	if subdir != "" && !subdirSlugRE.MatchString(subdir) {
		return fmt.Errorf("invalid subdir slug %q", subdir)
	}
	dest := flarumSnippetPath(domain, subdir)
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", dest, err)
	}
	return reloadNginxAfterSnippet(ctx, dest)
}

// -----------------------------------------------------------------------

func flarumInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req flarumInstallReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if req.OSUser == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user is required"}
	}
	if req.Docroot == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot is required"}
	}
	if req.DBName == "" || req.DBUser == "" || req.DBPassword == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "db_name, db_user, db_password are required"}
	}
	if req.SiteTitle == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "site_title is required"}
	}
	if req.AdminUser == "" || !flarumAdminUserPattern.MatchString(req.AdminUser) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_user must be 3-30 chars of letters, digits, dash, or underscore",
		}
	}
	if len(req.AdminPass) < 8 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_pass must be at least 8 characters (Flarum minimum)",
		}
	}
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computeFlarumInstallPath(req.Docroot, req.Subdirectory)
	subdir := strings.Trim(req.Subdirectory, "/")

	if subdir != "" {
		mkdirCmd := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath)
		if out, err := runBoundedOutput(mkdirCmd, 0); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("mkdir %s: %v (output: %s)", installPath, err, truncateStr(string(out), 256)),
			}
		}
	}

	removePlaceholderIndex(ctx, installPath)

	tmpDir, err := stagingMkdirTemp("flarum-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarballPath := filepath.Join(tmpDir, "flarum.tar.gz")

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	if err := downloadFlarumTarball(dlCtx, tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := verifyFlarumSHA256(tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarballPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}
	if err := extractFlarumTarball(ctx, req.OSUser, tarballPath, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// baseUrl is the FULL public forum URL. site_url already includes
	// the subdir (buildSiteURL appends it panel-side), so DON'T append
	// it again — doing so yields .../forum/forum and Flarum 404s its
	// own homepage (the app mounts at a path nginx never serves).
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	baseURL := strings.TrimRight(req.SiteURL, "/")

	configPath := filepath.Join(tmpDir, "flarum-install.yml")
	if err := writeFlarumInstallerYAML(req, configPath, baseURL, dbHost); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write installer yaml: %v", err)}
	}
	if err := os.Chmod(configPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod installer yaml: %v", err)}
	}

	if err := runFlarumCLIInstaller(ctx, req.OSUser, installPath, configPath); err != nil {
		// Best-effort cleanup of a partial config.php so re-install works.
		_ = exec.CommandContext(ctx, "rm", "-f", filepath.Join(installPath, "config.php")).Run()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// Write the nginx hardening + (subdir) front-controller snippet.
	// Required for BOTH docroot and subdir installs — the flattened
	// webroot exposes config.php / storage/ / vendor/ otherwise.
	domain, derr := DomainFromSiteURL(req.SiteURL)
	if derr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("extract domain: %v", derr)}
	}
	if err := writeFlarumNginx(ctx, domain, req.OSUser, subdir); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write nginx hardening: %v", err)}
	}

	return flarumInstallResp{Version: flarumVersion}, nil
}

func init() {
	RegisterAppInstaller("flarum", flarumInstallHandler)
}
