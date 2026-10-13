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
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// moodleInstallReq is the input for the Moodle 1-click installer (GH #310).
// Moodle is a PHP + MariaDB app; the agent runs its headless
// `admin/cli/install.php` with pre-supplied credentials so the user never
// sees the web installer. Unlike the other apps, Moodle keeps its uploaded
// content + cache in a `moodledata` directory OUTSIDE the web root, created
// here and removed by the deleter.
type moodleInstallReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory"`
	SiteURL      string `json:"site_url"`
	DBName       string `json:"db_name"`
	DBUser       string `json:"db_user"`
	DBPassword   string `json:"db_password"`
	DBHost       string `json:"db_host"`
	SiteTitle    string `json:"site_title"`
	AdminUser    string `json:"admin_user"`
	AdminPass    string `json:"admin_pass"`
	AdminEmail   string `json:"admin_email"`
	Language     string `json:"language"`
}

type moodleInstallResp struct {
	Version string `json:"version"`
}

// moodleVersion / moodleTarballURL / moodleTarballSHA256 pin the upstream
// release. Moodle 5.0 supports PHP 8.2-8.4 (4.5 caps at PHP 8.3, so it cannot
// run on a PHP-8.4-only host). Bump the version + SHA together; the versioned
// URL under stable500 is stable (unlike moodle-latest-500).
//
// To recompute on a host with the URL reachable:
//
//	curl -sSL -A 'jabali-panel-agent/1.0' \
//	  https://download.moodle.org/download.php/direct/stable500/moodle-5.0.8.tgz \
//	  | sha256sum
const moodleVersion = "5.0.8"
const moodleTarballURL = "https://download.moodle.org/download.php/direct/stable500/moodle-" + moodleVersion + ".tgz"
const moodleTarballSHA256 = "76e44d24956701b97583dd2d7d5339a0fe5917b8c795845579b4b1bc0fbfe9c6"

// moodleAdminUserPattern: Moodle usernames are lowercase alnum plus a few
// punctuation chars (it lowercases on save). Keep it conservative.
var moodleAdminUserPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._@-]{1,99}$`)

// moodleLanguagePattern matches Moodle language packs (en, fr, es, pt_br…).
var moodleLanguagePattern = regexp.MustCompile(`^[a-z]{2,3}(_[a-z0-9]{1,8})?$`)

func computeMoodleInstallPath(docroot, subdirectory string) string {
	if subdirectory == "" {
		return docroot
	}
	return filepath.Join(docroot, subdirectory)
}

// computeMoodleDataRoot derives the moodledata directory — deterministic from
// the install location so the deleter can find it. It sits as a sibling of the
// docroot (e.g. /home/<u>/domains/<d>/.moodledata/<key>), which is OUTSIDE
// public_html so nginx never serves it (a hard Moodle requirement), yet inside
// the tenant-owned domain dir so the unprivileged owner can create it — the
// home root itself is root:<user> 0751 and is NOT tenant-writable. The key is
// the subdirectory (or "root" for a docroot install).
func computeMoodleDataRoot(docroot, subdirectory string) string {
	key := strings.TrimSuffix(subdirectory, "/")
	if key == "" {
		key = "root"
	}
	key = regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(key, "_")
	return filepath.Join(filepath.Dir(docroot), ".moodledata", key)
}

// validateMoodleSubdir rejects a subdirectory that escapes the docroot. The
// panel validates it at create, but the agent is the trust boundary — never
// trust the wire value (mirrors wordpress_delete's defense-in-depth check).
func validateMoodleSubdir(docroot, subdirectory string) error {
	sub := strings.Trim(subdirectory, "/")
	if sub == "" {
		return nil
	}
	installPath := filepath.Join(docroot, sub)
	if rel, err := filepath.Rel(docroot, installPath); err != nil ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("subdirectory escapes docroot: %q", subdirectory)
	}
	return nil
}

// moodleShortName slugifies the site title into a Moodle shortname (alnum,
// non-empty). Falls back to "moodle".
func moodleShortName(title string) string {
	s := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(title, "")
	if s == "" {
		return "moodle"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

func downloadMoodleTarball(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, moodleTarballURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", moodleTarballURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", moodleTarballURL, resp.StatusCode)
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

func verifyMoodleSHA256(path string) error {
	if moodleTarballSHA256 == "" {
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
	if !strings.EqualFold(got, moodleTarballSHA256) {
		return fmt.Errorf("moodle tarball sha256 mismatch: got %s want %s", got, moodleTarballSHA256)
	}
	return nil
}

// extractMoodleTarball untars into installPath; the tarball wraps content
// under a top-level `moodle/` dir, so --strip-components=1 flattens it.
func extractMoodleTarball(ctx context.Context, osUser, tarballPath, installPath string) error {
	cmd := buildSystemdRunCmd(ctx, osUser,
		"tar", "--extract", "--gzip", "--strip-components=1",
		"--file", tarballPath, "--directory", installPath,
	)
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("tar extract: %w (output: %s)", err, truncateStr(string(out), 512))
	}
	return nil
}

// runMoodleCLIInstaller drives admin/cli/install.php headless. Moodle is
// strict about --wwwroot matching the URL the site is served on. Moodle's CLI
// installer takes the DB and admin passwords only as options, so they reach it
// through the PHP runner on stdin, never on a command line
// (php_script_run.go).
// prepareMoodleDataRoot makes the data root (moodledata), outside the docroot
// so it is never web-served, and keeps it the tenant's alone. Every step runs
// as the tenant, so a link the tenant planted can't redirect it.
//
// The domain directory is setgid www-data, so .moodledata and the data root
// would be group www-data, and Moodle chmods the data root to its directory
// mode: the web server's group could write every stored file. Nothing but the
// tenant's PHP pool and cron, which run as the tenant, needs it.
func prepareMoodleDataRoot(ctx context.Context, osUser, dataRoot string) error {
	parent := filepath.Dir(dataRoot)
	for _, args := range [][]string{
		{"mkdir", "-p", "-m", "0770", dataRoot},
		{"chgrp", "--no-dereference", osUser, parent, dataRoot},
		{"chmod", "0750", parent},
	} {
		if out, err := runBoundedOutput(buildSystemdRunCmd(ctx, osUser, args...), 0); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("set up moodledata: %v (output: %s)", err, truncateStr(string(out), 256))}
		}
	}
	return nil
}

func runMoodleCLIInstaller(ctx context.Context, req moodleInstallReq, installPath, dataRoot string) error {
	out, err := moodleInstallRun(req, installPath, dataRoot).run(ctx)
	if err != nil {
		return fmt.Errorf("php admin/cli/install.php: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	return nil
}

// moodleInstallRun is the admin/cli/install.php run for req.
func moodleInstallRun(req moodleInstallReq, installPath, dataRoot string) phpScriptRun {
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}
	return phpScriptRun{
		OSUser: req.OSUser,
		PHP:    phpCLIFor(req.OSUser),
		// CLI php.ini ships max_input_vars=1000; Moodle's environment check
		// requires >=5000. The per-domain FPM pool sets this for the runtime,
		// but the CLI installer uses the CLI ini, so override inline.
		PHPFlags: []string{"-d", "max_input_vars=5000", "-d", "memory_limit=256M"},
		Script:   filepath.Join(installPath, "admin", "cli", "install.php"),
		Args: []string{
			"--non-interactive",
			"--agree-license",
			"--lang=" + lang,
			"--wwwroot=" + strings.TrimSuffix(req.SiteURL, "/"),
			"--dataroot=" + dataRoot,
			// Directories Moodle creates in the data root, and through the
			// umask it derives from this, its files (0660). Its default, 2777,
			// makes both writable by everyone.
			"--chmod=2770",
			"--dbtype=mariadb",
			"--dbhost=" + dbHost,
			"--dbname=" + req.DBName,
			"--dbuser=" + req.DBUser,
			"--dbpass=" + req.DBPassword,
			"--fullname=" + req.SiteTitle,
			"--shortname=" + moodleShortName(req.SiteTitle),
			"--summary=",
			"--adminuser=" + req.AdminUser,
			"--adminpass=" + req.AdminPass,
			"--adminemail=" + req.AdminEmail,
		},
		Dir: installPath,
	}
}

func moodleInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req moodleInstallReq
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
	if req.SiteURL == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "site_url is required (Moodle wwwroot must match the served URL)"}
	}
	if req.AdminUser == "" || !moodleAdminUserPattern.MatchString(req.AdminUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_user must be lowercase alphanumeric (plus . _ - @)"}
	}
	// Moodle's default password policy: >=8 chars with upper, lower, digit and a
	// non-alphanumeric. Require a caller-supplied password (no auto-generate)
	// since a generated ULID wouldn't satisfy it; surface a clear error early.
	if len(req.AdminPass) < 8 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass must be at least 8 characters (Moodle policy: upper, lower, digit + symbol)"}
	}
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	}
	if req.Language != "" && !moodleLanguagePattern.MatchString(req.Language) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("language %q does not match expected Moodle pack form", req.Language)}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	if err := validateMoodleSubdir(req.Docroot, req.Subdirectory); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computeMoodleInstallPath(req.Docroot, req.Subdirectory)
	dataRoot := computeMoodleDataRoot(req.Docroot, req.Subdirectory)

	if req.Subdirectory != "" {
		mkdirCmd := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath)
		if out, err := runBoundedOutput(mkdirCmd, 0); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v (output: %s)", installPath, err, truncateStr(string(out), 256))}
		}
	}
	if err := prepareMoodleDataRoot(ctx, req.OSUser, dataRoot); err != nil {
		return nil, err
	}

	removePlaceholderIndex(ctx, installPath)

	tmpDir, err := stagingMkdirTemp("moodle-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarballPath := filepath.Join(tmpDir, "moodle.tgz")

	dlCtx, dlCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer dlCancel()
	if err := downloadMoodleTarball(dlCtx, tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := verifyMoodleSHA256(tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarballPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}
	if err := extractMoodleTarball(ctx, req.OSUser, tarballPath, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := runMoodleCLIInstaller(ctx, req, installPath, dataRoot); err != nil {
		// Best-effort cleanup so a retry isn't blocked by config.php / the
		// moodledata tree from the failed attempt.
		_ = buildSystemdRunCmd(ctx, req.OSUser, "rm", "-f", filepath.Join(installPath, "config.php")).Run()
		_ = buildSystemdRunCmd(ctx, req.OSUser, "rm", "-rf", dataRoot).Run()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// config.php holds the DB password in plaintext; normalize the tree to
	// owner+group read (same as the other PHP apps).
	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	return moodleInstallResp{Version: moodleVersion}, nil
}

func init() {
	RegisterAppInstaller("moodle", moodleInstallHandler)
}
