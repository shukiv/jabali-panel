package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nextcloud_install.go — installs Nextcloud (Discussion #1032), self-hosted
// file sync and share, on the tenant's own PHP-FPM pool and MariaDB.
//
// The pinned release is downloaded, SHA-256-checked and extracted into the
// docroot as the tenant. Nextcloud's own `occ maintenance:install` then
// creates the schema and the admin account, with the data directory beside the
// docroot (never under it, so nginx can never serve it).
//
// Secrets reach the installer on stdin only. A transient systemd unit's argv
// is logged to the system journal ("Started run-uN.service - <argv>"), and its
// argv and Environment are readable by every local user through
// `systemctl show` while it runs. So a static PHP shim reads the install
// options as JSON from stdin and hands them to occ internally; the unit's
// command line carries only paths.
//
// After the install: trusted_domains gets the site's host, background jobs
// switch to cron (the panel creates the job), config/config.php moves to the
// tenant's own group (Nextcloud rewrites it in place as 0640, so this keeps it
// owner-only), and a per-install nginx drop-in is written
// (nextcloud_nginx.go).

const (
	nextcloudVersion    = "35.0.1"
	nextcloudTarballURL = "https://download.nextcloud.com/server/releases/nextcloud-" + nextcloudVersion + ".tar.bz2"
	nextcloudTarballSHA = "7ed305e880192d804ba83179a19dd22257679608d63161a611ce3f8a918d2aa6"
	// nextcloudDataDirName is the data directory, created beside the docroot.
	nextcloudDataDirName = "nextcloud-data"
	// Nextcloud 35's lib/versioncheck.php refuses PHP < 8.3 and >= 8.6.
	nextcloudMinPHP = "8.3"
	nextcloudMaxPHP = "8.5"
)

// nextcloudInstallShim runs `occ maintenance:install` with the options it
// reads as JSON from stdin, so no secret is ever on a command line. It builds
// occ's argv itself and requires console.php, which is exactly what occ does.
const nextcloudInstallShim = `<?php
// Jabali headless Nextcloud installer (Discussion #1032). Reads the install
// options as JSON on stdin and runs occ maintenance:install with them.
$in = json_decode(stream_get_contents(STDIN), true);
if (!is_array($in)) { fwrite(STDERR, "INSTALL_FAIL: unreadable input\n"); exit(2); }
foreach (["install_path", "db_name", "db_host", "db_user", "db_pass", "admin_user", "admin_pass", "data_dir"] as $k) {
  if (!isset($in[$k]) || !is_string($in[$k]) || $in[$k] === "") { fwrite(STDERR, "INSTALL_FAIL: missing $k\n"); exit(2); }
}
$root = $in["install_path"];
$args = [$root . "/occ", "maintenance:install", "--no-interaction",
  "--database=mysql",
  "--database-name=" . $in["db_name"],
  "--database-host=" . $in["db_host"],
  "--database-user=" . $in["db_user"],
  "--database-pass=" . $in["db_pass"],
  "--admin-user=" . $in["admin_user"],
  "--admin-pass=" . $in["admin_pass"],
  "--data-dir=" . $in["data_dir"],
];
if (isset($in["admin_email"]) && is_string($in["admin_email"]) && $in["admin_email"] !== "") {
  $args[] = "--admin-email=" . $in["admin_email"];
}
$_SERVER["argv"] = $args;
$_SERVER["argc"] = count($args);
$argv = $args;
$argc = count($args);
chdir($root);
require $root . "/console.php";
`

type nextcloudInstallReq struct {
	AppType    string `json:"app_type"` // present, ignored
	OSUser     string `json:"os_user"`
	Docroot    string `json:"docroot"`
	SiteURL    string `json:"site_url"`
	DBName     string `json:"db_name"`
	DBUser     string `json:"db_user"`
	DBPassword string `json:"db_password"`
	DBHost     string `json:"db_host"`
	AdminUser  string `json:"admin_user"`
	AdminPass  string `json:"admin_pass"`
	AdminEmail string `json:"admin_email"`
}

type nextcloudInstallResp struct {
	Version    string `json:"version"`
	PHPVersion string `json:"php_version"`
}

// nextcloudShimInput is what the shim reads on stdin.
type nextcloudShimInput struct {
	InstallPath string `json:"install_path"`
	DBName      string `json:"db_name"`
	DBHost      string `json:"db_host"`
	DBUser      string `json:"db_user"`
	DBPass      string `json:"db_pass"`
	AdminUser   string `json:"admin_user"`
	AdminPass   string `json:"admin_pass"`
	AdminEmail  string `json:"admin_email,omitempty"`
	DataDir     string `json:"data_dir"`
}

var (
	// nextcloudDBIdentRE is a MariaDB database or user name as the panel
	// provisions them.
	nextcloudDBIdentRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	// nextcloudDBHostRE is a host name, or "localhost".
	nextcloudDBHostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252})$`)
	// nextcloudAdminUserRE is a subset of what Nextcloud accepts as a login.
	nextcloudAdminUserRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$`)
)

// validateNextcloudReq checks every field the installer uses. Secrets only
// need to be present and single-line; everything else is restricted to what
// the panel generates.
func validateNextcloudReq(req *nextcloudInstallReq) error {
	switch {
	case req.OSUser == "" || !osUserSafeRE.MatchString(req.OSUser):
		return fmt.Errorf("os_user is missing or invalid")
	case req.Docroot == "":
		return fmt.Errorf("docroot is required")
	case !nextcloudDBIdentRE.MatchString(req.DBName) || !nextcloudDBIdentRE.MatchString(req.DBUser):
		return fmt.Errorf("db_name and db_user are required ([A-Za-z0-9_], up to 64)")
	case req.DBPassword == "" || strings.ContainsAny(req.DBPassword, "\x00\r\n"):
		return fmt.Errorf("db_password is missing or invalid")
	case !nextcloudAdminUserRE.MatchString(req.AdminUser):
		return fmt.Errorf("admin_user is missing or invalid")
	case req.AdminPass == "" || strings.ContainsAny(req.AdminPass, "\x00\r\n"):
		return fmt.Errorf("admin_pass is missing or invalid")
	}
	if req.DBHost == "" {
		req.DBHost = "localhost"
	}
	if !nextcloudDBHostRE.MatchString(req.DBHost) {
		return fmt.Errorf("db_host is invalid")
	}
	if req.AdminEmail != "" {
		a, err := mail.ParseAddress(req.AdminEmail)
		if err != nil || a.Address != req.AdminEmail {
			return fmt.Errorf("admin_email is invalid")
		}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return fmt.Errorf("invalid docroot: %w", err)
	}
	return nil
}

// nextcloudDataDir is the data directory of an install at docroot: beside the
// docroot, inside the tenant's home.
func nextcloudDataDir(osUser, docroot string) (string, error) {
	dir := filepath.Join(filepath.Dir(filepath.Clean(docroot)), nextcloudDataDirName)
	home := filepath.Join("/home", osUser) + string(os.PathSeparator)
	if !strings.HasPrefix(dir, home) {
		return "", fmt.Errorf("data directory %s would be outside %s", dir, home)
	}
	return dir, nil
}

// phpVersionAtLeast compares "major.minor" versions. Both must parse.
func phpVersionAtLeast(v, min string) bool {
	vMaj, vMin, ok1 := splitPHPVersion(v)
	mMaj, mMin, ok2 := splitPHPVersion(min)
	if !ok1 || !ok2 {
		return false
	}
	return vMaj > mMaj || (vMaj == mMaj && vMin >= mMin)
}

func splitPHPVersion(v string) (int, int, bool) {
	maj, min, ok := strings.Cut(v, ".")
	if !ok {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(maj)
	b, err2 := strconv.Atoi(min)
	if err1 != nil || err2 != nil || a < 0 || b < 0 {
		return 0, 0, false
	}
	return a, b, true
}

// nextcloudSupportsPHP reports whether Nextcloud's pinned release runs on v.
func nextcloudSupportsPHP(v string) bool {
	return phpVersionAtLeast(v, nextcloudMinPHP) && phpVersionAtLeast(nextcloudMaxPHP, v)
}

// nextcloudInstallCmd builds the occ install run: the shim as the tenant, the
// options on stdin. Nothing secret is on the command line.
func nextcloudInstallCmd(ctx context.Context, req nextcloudInstallReq, phpBin, shimPath, installPath, dataDir string) (*exec.Cmd, error) {
	in, err := json.Marshal(nextcloudShimInput{
		InstallPath: installPath,
		DBName:      req.DBName,
		DBHost:      req.DBHost,
		DBUser:      req.DBUser,
		DBPass:      req.DBPassword,
		AdminUser:   req.AdminUser,
		AdminPass:   req.AdminPass,
		AdminEmail:  req.AdminEmail,
		DataDir:     dataDir,
	})
	if err != nil {
		return nil, err
	}
	cmd := buildSystemdRunCmd(ctx, req.OSUser, phpBin, "-d", "memory_limit=512M", shimPath)
	cmd.Stdin = bytes.NewReader(in)
	return cmd, nil
}

// nextcloudOCC runs one occ command as the tenant. Callers pass no secrets.
func nextcloudOCC(ctx context.Context, osUser, phpBin, installPath string, args ...string) error {
	full := append([]string{phpBin, "-d", "memory_limit=512M", filepath.Join(installPath, "occ")}, args...)
	cmd := buildSystemdRunCmd(ctx, osUser, full...)
	cmd.Stdin = strings.NewReader("")
	if out, err := runBoundedOutput(cmd, 0); err != nil {
		return fmt.Errorf("occ %s: %w (%s)", strings.Join(args, " "), err, truncateStr(string(out), 512))
	}
	return nil
}

func nextcloudInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req nextcloudInstallReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if err := validateNextcloudReq(&req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	// Docroot-only (RootOnly): the install path is the docroot.
	installPath, err := appInstallPath(req.Docroot, "")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	domain, err := DomainFromSiteURL(req.SiteURL)
	if err != nil || !domainRegex.MatchString(domain) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("site_url %q does not name a valid domain", req.SiteURL)}
	}
	dataDir, err := nextcloudDataDir(req.OSUser, installPath)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	// The PHP the domain is served by decides everything: the pinned release
	// refuses anything outside 8.3–8.5, so check before a 250 MB download.
	pool, err := domainFPMPool(domain, req.OSUser)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: err.Error()}
	}
	if !nextcloudSupportsPHP(pool.PHPVersion) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: fmt.Sprintf(
			"Nextcloud %s needs PHP %s to %s; %s runs PHP %s. Switch the domain's PHP version, then install again.",
			nextcloudVersion, nextcloudMinPHP, nextcloudMaxPHP, domain, pool.PHPVersion)}
	}
	phpBin, err := exec.LookPath("php" + pool.PHPVersion)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: fmt.Sprintf("php%s CLI is not installed", pool.PHPVersion)}
	}

	// The data directory must be new (or empty): never install over another
	// install's files. Checked without following a link; created as the tenant.
	if fi, lerr := os.Lstat(dataDir); lerr == nil {
		if !fi.IsDir() {
			return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: fmt.Sprintf("%s exists and is not a directory", dataDir)}
		}
		entries, rerr := os.ReadDir(dataDir)
		if rerr != nil || len(entries) > 0 {
			return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: fmt.Sprintf("%s already holds files; move them away first", dataDir)}
		}
	} else if out, err := runBoundedOutput(buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-m", "0770", dataDir), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("create data directory: %v (%s)", err, truncateStr(string(out), 512))}
	}

	removePlaceholderIndex(ctx, installPath)

	tmpDir, err := stagingMkdirTemp("nextcloud-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarball := filepath.Join(tmpDir, "nextcloud.tar.bz2")
	dlCtx, dlCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer dlCancel()
	if err := nextcloudDownload(dlCtx, tarball); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarball, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}
	if out, err := runBoundedOutput(buildSystemdRunCmd(ctx, req.OSUser,
		"tar", "--extract", "--bzip2", "--strip-components=1", "--file", tarball, "--directory", installPath), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("extract: %v (%s)", err, truncateStr(string(out), 512))}
	}

	shimPath := filepath.Join(tmpDir, "nextcloud-install.php")
	if err := os.WriteFile(shimPath, []byte(nextcloudInstallShim), 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write install shim: %v", err)}
	}
	installCmd, err := nextcloudInstallCmd(ctx, req, phpBin, shimPath, installPath, dataDir)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("build install: %v", err)}
	}
	if out, err := runBoundedOutput(installCmd, 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("occ maintenance:install: %v (%s)", err, truncateStr(string(out), 1024))}
	}

	// Serve the site's own host (the install trusts only localhost), and
	// run background jobs from the cron the panel creates.
	for _, args := range [][]string{
		{"config:system:set", "trusted_domains", "1", "--value=" + domain},
		{"config:system:set", "overwrite.cli.url", "--value=" + req.SiteURL},
		{"background:cron"},
	} {
		if err := nextcloudOCC(ctx, req.OSUser, phpBin, installPath, args...); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
		}
	}

	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("normalize perms: %v", err)}
	}
	// config.php holds the DB password, secret and passwordsalt. Nextcloud
	// rewrites it in place as 0640, so the group decides who else reads it:
	// the tenant's own group, not www-data. Done as the tenant, so a link
	// there can't redirect a root-side chgrp.
	cfgPath := filepath.Join(installPath, "config", "config.php")
	if out, err := runBoundedOutput(buildSystemdRunCmd(ctx, req.OSUser, "chgrp", "--no-dereference", req.OSUser, cfgPath), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chgrp config.php: %v (%s)", err, truncateStr(string(out), 512))}
	}

	if err := writeNextcloudNginx(ctx, domain, pool.Socket); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("nginx snippet: %v", err)}
	}

	return nextcloudInstallResp{Version: nextcloudVersion, PHPVersion: pool.PHPVersion}, nil
}

func nextcloudDownload(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextcloudTarballURL, nil)
	if err != nil {
		return fmt.Errorf("nextcloud download: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("nextcloud download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("nextcloud download: HTTP %d from %s", resp.StatusCode, nextcloudTarballURL)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("nextcloud download: create %s: %w", dest, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return fmt.Errorf("nextcloud download: copy: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != nextcloudTarballSHA {
		return fmt.Errorf("nextcloud download: SHA256 mismatch: got %s want %s", got, nextcloudTarballSHA)
	}
	return nil
}

func init() {
	RegisterAppInstaller("nextcloud", nextcloudInstallHandler)
}
