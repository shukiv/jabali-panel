package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// phpbbInstallReq is the input shape for the phpBB installer.
type phpbbInstallReq struct {
	AppType          string `json:"app_type"`
	OSUser           string `json:"os_user"`
	Docroot          string `json:"docroot"`
	Subdirectory     string `json:"subdirectory"`
	SiteURL          string `json:"site_url"`
	UseWWW           bool   `json:"use_www"`
	DBName           string `json:"db_name"`
	DBUser           string `json:"db_user"`
	DBPassword       string `json:"db_password"`
	DBHost           string `json:"db_host"`
	SiteTitle        string `json:"site_title"`
	BoardDescription string `json:"board_description"`
	AdminUser        string `json:"admin_user"`
	AdminPass        string `json:"admin_pass"`
	AdminEmail       string `json:"admin_email"`
	Language         string `json:"language"`
}

type phpbbInstallResp struct {
	Version string `json:"version"`
}

// phpbbVersion is the upstream phpBB release this build targets. Pinned
// to a 3.3.x because 3.3 is the current stable line; 4.x is in alpha
// as of late 2025.
//
// download.phpbb.com sits behind Cloudflare with a UA challenge that
// returns 403 to any non-browser client (we tried `Mozilla/5.0 ...`,
// real Chrome UA — all 403). Fetch from GitHub source archive instead;
// the github auto-archive is unauthenticated and unrestricted. Tag
// format on the repo is `release-X.Y.Z` (with `release-` prefix), and
// the archive extracts as `phpbb-release-X.Y.Z/` containing the source
// tree — webroot is the inner `phpBB/` subdir, see extractPhpbbTarball.
const phpbbVersion = "3.3.13"

var phpbbTarballURL = fmt.Sprintf(
	"https://github.com/phpbb/phpbb/archive/refs/tags/release-%s.tar.gz",
	phpbbVersion,
)

// phpbbTarballSHA256 is the SHA-256 of the tarball at phpbbTarballURL
// as of the install-time pin. Empty value disables the integrity
// check (DEV ONLY — production builds MUST set this).
//
//	curl -sSL -A 'jabali-panel-agent/1.0 (+https://jabali.local)' \
//	  https://download.phpbb.com/pub/release/3.3/3.3.13/phpBB-3.3.13.tar.bz2 \
//	  | sha256sum
const phpbbTarballSHA256 = "2990317d3bdbf39d38b36f182253cf389d7d43287a908f2ccc8bc36ae38b9267"

// phpbbAdminUserPattern: phpBB usernames are 3-20 chars by default
// (configurable post-install). Allow alnum + dot/dash/underscore;
// stricter than phpBB's own validator which accepts spaces.
var phpbbAdminUserPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,20}$`)

// phpbbLanguagePattern matches the lang-pack directory names phpBB
// uses (en, en-us, de, fr, …).
var phpbbLanguagePattern = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{1,8})?$`)

func computePhpbbInstallPath(docroot, subdirectory string) string {
	if subdirectory == "" {
		return docroot
	}
	return filepath.Join(docroot, subdirectory)
}

func downloadPhpbbTarball(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, phpbbTarballURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", phpbbTarballURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", phpbbTarballURL, resp.StatusCode)
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

func verifyPhpbbSHA256(path string) error {
	if phpbbTarballSHA256 == "" {
		// Fail closed (GH security batch): an unpinned hash must never
		// silently accept arbitrary downloaded code.
		return fmt.Errorf("phpBB tarball SHA-256 not pinned — refusing to install unverified code")
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
	if !strings.EqualFold(got, phpbbTarballSHA256) {
		return fmt.Errorf("phpbb tarball sha256 mismatch: got %s want %s", got, phpbbTarballSHA256)
	}
	return nil
}

// extractPhpbbTarball untars the GitHub source archive and copies the
// inner phpBB webroot into installPath. The github archive shape is:
//
//	phpbb-release-3.3.13/
//	├── phpBB/                  (← the actual webroot, what we want)
//	├── tests/
//	├── travis/
//	├── README.md
//	└── ...
//
// `--strip-components=2` would peel both wrappers but also expose
// tests/ and travis/ as siblings of installPath's contents, so use
// `--strip-components=1` and then cp the inner phpBB/. → installPath/.
func extractPhpbbTarball(ctx context.Context, osUser, tarballPath, installPath string) error {
	stagingDir := filepath.Join(filepath.Dir(tarballPath), "stage")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return fmt.Errorf("mkdir staging: %w", err)
	}
	if err := execCommandContext(ctx, "chown", "-R", osUser+":"+osUser, stagingDir).Run(); err != nil {
		return fmt.Errorf("chown staging: %w", err)
	}
	tarCmd := buildSystemdRunCmd(ctx, osUser,
		"tar",
		"--extract",
		"--gzip",
		"--strip-components=1",
		"--file", tarballPath,
		"--directory", stagingDir,
	)
	if out, err := runBoundedOutput(tarCmd, 0); err != nil {
		return fmt.Errorf("tar extract: %w (output: %s)", err, truncateStr(string(out), 512))
	}
	src := filepath.Join(stagingDir, "phpBB")
	mvCmd := buildSystemdRunCmd(ctx, osUser, "sh", "-c",
		fmt.Sprintf("cp -a %s/. %s/", shellQuote(src), shellQuote(installPath)),
	)
	if out, err := runBoundedOutput(mvCmd, 0); err != nil {
		return fmt.Errorf("move phpBB contents: %w (output: %s)", err, truncateStr(string(out), 512))
	}
	return nil
}

// yamlSingleQuoted escapes a string for use as a single-quoted YAML
// scalar. The only escape rule for single-quoted YAML is doubling
// embedded single quotes, so the encoder is trivial — and trivial
// keeps the trust boundary tight (no parser surprises around 1.1 vs
// 1.2 spec edge cases like Yes/No/On/Off being booleans).
func yamlSingleQuoted(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// writePhpbbInstallerYAML renders the YAML config phpBB's CLI
// installer reads. We use single-quoted scalars throughout so user-
// supplied strings (titles, descriptions, passwords) can't break the
// YAML structure or accidentally be interpreted as booleans.
func writePhpbbInstallerYAML(req phpbbInstallReq, dest, scriptPath string, port int, secure bool) error {
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	proto := "http://"
	if secure {
		proto = "https://"
	}

	serverHost := ""
	if u, err := url.Parse(req.SiteURL); err == nil && u.Host != "" {
		serverHost = u.Hostname()
	}

	cookieSecure := "false"
	if secure {
		cookieSecure = "true"
	}

	yaml := "installer:\n" +
		"  admin:\n" +
		"    name: " + yamlSingleQuoted(req.AdminUser) + "\n" +
		"    password: " + yamlSingleQuoted(req.AdminPass) + "\n" +
		"    email: " + yamlSingleQuoted(req.AdminEmail) + "\n" +
		"  board:\n" +
		"    lang: " + yamlSingleQuoted(lang) + "\n" +
		"    name: " + yamlSingleQuoted(req.SiteTitle) + "\n" +
		"    description: " + yamlSingleQuoted(req.BoardDescription) + "\n" +
		"  database:\n" +
		"    dbms: 'mysqli'\n" +
		"    dbhost: " + yamlSingleQuoted(dbHost) + "\n" +
		"    dbport: ''\n" +
		"    dbuser: " + yamlSingleQuoted(req.DBUser) + "\n" +
		"    dbpasswd: " + yamlSingleQuoted(req.DBPassword) + "\n" +
		"    dbname: " + yamlSingleQuoted(req.DBName) + "\n" +
		"    table_prefix: 'phpbb_'\n" +
		"  email:\n" +
		"    enabled: false\n" +
		"    smtp_delivery: false\n" +
		"    smtp_host: ''\n" +
		"    smtp_auth: ''\n" +
		"    smtp_user: ''\n" +
		"    smtp_pass: ''\n" +
		"  server:\n" +
		"    cookie_secure: " + cookieSecure + "\n" +
		"    server_protocol: " + yamlSingleQuoted(proto) + "\n" +
		"    force_server_vars: false\n" +
		"    server_name: " + yamlSingleQuoted(serverHost) + "\n" +
		"    server_port: " + fmt.Sprintf("%d", port) + "\n" +
		"    script_path: " + yamlSingleQuoted(scriptPath) + "\n"

	return os.WriteFile(dest, []byte(yaml), 0o600)
}

// runPhpbbCLIInstaller drives `php phpbbcli.php install <config>`.
// phpBB's installer reads the YAML, persists data to MariaDB, and
// writes config.php in the install root with the runtime DB
// credentials.
//
// Two cwd-sensitive quirks:
//   - phpbbcli.php is the documented CLI entry point (not app.php).
//     app.php's line 30 hard-codes `require('../install/startup.php')`
//     and only resolves correctly when cwd is install/ — running it
//     from the docroot gives `<docroot>/../install/startup.php` which
//     doesn't exist.
//   - cwd MUST be installPath/install/ for the require chain to work.
//     phpbbcli.php pulls in startup.php with the same `../install/...`
//     pattern as app.php, so the same rule applies.
func runPhpbbCLIInstaller(ctx context.Context, osUser, installPath, configPath string) error {
	// systemd-run launches the wrapped command in a NEW unit — Go's
	// cmd.Dir on the systemd-run process doesn't propagate. The unit's
	// cwd MUST be installPath/install/ because phpbbcli.php internally
	// does `require('../install/startup.php')`, which is cwd-relative
	// (resolves to <cwd>/../install/startup.php; only works when cwd
	// is install/ itself). Pass `--working-directory=` to systemd-run
	// so the unit starts in the right place.
	installDir := filepath.Join(installPath, "install")
	args := []string{
		"--working-directory=" + installDir,
		phpCLIFor(osUser),
		"phpbbcli.php",
		"install",
		configPath,
	}
	cmd := buildSystemdRunCmd(ctx, osUser, args...)
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("php phpbbcli.php install: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	return nil
}

// runPhpbbComposerInstall runs `composer install --no-dev --no-progress`
// in the phpBB root to populate vendor/. composer is provisioned by
// install.sh (used by Drupal too). --no-dev skips testing/linting
// packages we don't need at runtime; --no-progress keeps stdout
// quiet so the agent's CombinedOutput buffer stays small.
func runPhpbbComposerInstall(ctx context.Context, osUser, installPath string) error {
	cmd := buildSystemdRunCmd(ctx, osUser,
		"--working-directory="+installPath,
		"composer", "install", "--no-dev", "--no-progress", "--no-interaction",
	)
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("composer install: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	return nil
}

// removePhpbbInstallDir deletes the install/ folder after a successful
// install. phpBB serves a "you must delete the install dir" warning
// otherwise; the upstream docs say it's mandatory.
func removePhpbbInstallDir(ctx context.Context, osUser, installPath string) error {
	cmd := buildSystemdRunCmd(ctx, osUser, "rm", "-rf", filepath.Join(installPath, "install"))
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("rm install/: %w (output: %s)", err, truncateStr(string(out), 256))
	}
	return nil
}

func phpbbInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req phpbbInstallReq
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
	if req.AdminUser == "" || !phpbbAdminUserPattern.MatchString(req.AdminUser) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_user must be 3-20 chars of letters, digits, dot, dash, or underscore",
		}
	}
	if len(req.AdminPass) < 6 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_pass must be at least 6 characters (phpBB minimum)",
		}
	}
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	}
	if req.Language != "" && !phpbbLanguagePattern.MatchString(req.Language) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("language %q does not match expected ISO-639 form", req.Language),
		}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computePhpbbInstallPath(req.Docroot, req.Subdirectory)

	if req.Subdirectory != "" {
		mkdirCmd := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath)
		if out, err := runBoundedOutput(mkdirCmd, 0); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("mkdir %s: %v (output: %s)", installPath, err, truncateStr(string(out), 256)),
			}
		}
	}

	removePlaceholderIndex(ctx, installPath)

	tmpDir, err := stagingMkdirTemp("phpbb-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarballPath := filepath.Join(tmpDir, "phpbb.tar.bz2")

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	if err := downloadPhpbbTarball(dlCtx, tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := verifyPhpbbSHA256(tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarballPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}
	if err := extractPhpbbTarball(ctx, req.OSUser, tarballPath, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	// GitHub source archive doesn't ship vendor/ — phpBB's autoloader
	// (and phpbbcli.php) require it. The release tarball at
	// download.phpbb.com had vendor/ baked in, so this step was a
	// no-op there; switching to the github source as our download
	// mirror means we have to bootstrap composer ourselves.
	if err := runPhpbbComposerInstall(ctx, req.OSUser, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// Render the YAML config in the temp dir, then chmod 0644 + chown
	// to the install user so the systemd-run-as-user installer can
	// open it.
	scriptPath := "/"
	if req.Subdirectory != "" {
		scriptPath = "/" + strings.Trim(req.Subdirectory, "/") + "/"
	}
	port := 443
	secure := true
	if u, err := url.Parse(req.SiteURL); err == nil {
		if u.Scheme == "http" {
			port = 80
			secure = false
		}
		if u.Port() != "" {
			fmt.Sscanf(u.Port(), "%d", &port)
		}
	}

	configPath := filepath.Join(tmpDir, "install-config.yml")
	if err := writePhpbbInstallerYAML(req, configPath, scriptPath, port, secure); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write installer yaml: %v", err)}
	}
	if err := os.Chmod(configPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod installer yaml: %v", err)}
	}

	if err := runPhpbbCLIInstaller(ctx, req.OSUser, installPath, configPath); err != nil {
		// Best-effort cleanup of partial config.php so re-install works.
		_ = execCommandContext(ctx, "rm", "-f", filepath.Join(installPath, "config.php")).Run()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := removePhpbbInstallDir(ctx, req.OSUser, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	return phpbbInstallResp{Version: phpbbVersion}, nil
}

func init() {
	RegisterAppInstaller("phpbb", phpbbInstallHandler)
}
