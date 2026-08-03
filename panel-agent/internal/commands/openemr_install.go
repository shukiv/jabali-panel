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

// openemr_install.go — installs OpenEMR (GH #631), a large PHP + MariaDB
// electronic-health-records system. Unlike the Laravel apps it serves from
// the install root (index.php at root, no public/ flatten) and installs
// headlessly via its own contrib/util/installScripts/InstallerAuto.php:
// that script's quick_install() writes sites/default/sqlconf.php, loads the
// schema, and creates the admin — against the jabali-provisioned database +
// user in no_root_db_access mode, so no MySQL root credential is involved.
//
// Pinned to the official release tarball (vendor/ bundled), SHA-256 verified
// before extract, per the catalog's no-floating-latest bar (GH #631).

const (
	openemrVersion    = "8.2.0"
	openemrTarballURL = "https://github.com/openemr/openemr/releases/download/v8_2_0/openemr-" + openemrVersion + ".tar.gz"
	openemrTarballSHA = "0fa083a98a84b295f96e361189229f65102ade72ffc5af2ec66f054fd0237580"
)

type openemrInstallReq struct {
	AppType      string `json:"app_type"` // present, ignored
	OSUser       string `json:"os_user"`
	Docroot      string `json:"docroot"`
	Subdirectory string `json:"subdirectory"`
	SiteURL      string `json:"site_url"`
	SiteTitle    string `json:"site_title"`
	AdminUser    string `json:"admin_user"`
	AdminPass    string `json:"admin_pass"`
	AdminEmail   string `json:"admin_email"`
	DBName       string `json:"db_name"`
	DBUser       string `json:"db_user"`
	DBPassword   string `json:"db_password"`
	DBHost       string `json:"db_host"`
	UseWWW       bool   `json:"use_www"`
}

type openemrInstallResp struct {
	Version string `json:"version"`
}

// openemrUsernamePattern — OpenEMR usernames are conservative.
var openemrUsernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{2,32}$`)

func openemrInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req openemrInstallReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	switch {
	case req.OSUser == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user is required"}
	case req.Docroot == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot is required"}
	case req.AdminPass == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass is required"}
	case req.DBName == "" || req.DBUser == "" || req.DBPassword == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "db_name, db_user, db_password are required"}
	}
	req.AdminUser = strings.TrimSpace(req.AdminUser)
	if req.AdminUser == "" {
		req.AdminUser = "admin"
	}
	if !openemrUsernamePattern.MatchString(req.AdminUser) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_user must be 2-32 chars [A-Za-z0-9_.-]"}
	}
	// InstallerAuto parses `key=value` argv with explode("=") — a value
	// containing '=' would be truncated. Reject it in the admin password so
	// a login can never be set to a silently-truncated secret. Generated
	// passwords (base64url / ULID) never contain '='.
	if strings.ContainsAny(req.AdminPass, "=\r\n") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass must not contain '=' or newlines"}
	}
	if req.SiteTitle == "" {
		req.SiteTitle = "OpenEMR"
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	installPath, err := appInstallPath(req.Docroot, req.Subdirectory)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}

	if req.Subdirectory != "" {
		if out, err := runBoundedOutput(buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath), 0); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v (%s)", installPath, err, truncateStr(string(out), 256))}
		}
	}
	removePlaceholderIndex(ctx, installPath)

	tmpDir, err := stagingMkdirTemp("openemr-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarball := filepath.Join(tmpDir, "openemr.tar.gz")
	dlCtx, dlCancel := context.WithTimeout(ctx, 20*time.Minute)
	defer dlCancel()
	if err := openemrDownload(dlCtx, tarball); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarball, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}
	// Extract as the OS user (strip openemr-<ver>/).
	extract := buildSystemdRunCmd(ctx, req.OSUser,
		"tar", "--extract", "--gzip", "--strip-components=1",
		"--file", tarball, "--directory", installPath)
	if out, err := runBoundedOutput(extract, 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("tar extract: %v (%s)", err, truncateStr(string(out), 512))}
	}

	// Headless install via InstallerAuto.php (no_root_db_access mode — uses
	// the jabali-provisioned db + user on the pre-created database). The
	// script is gated behind OPENEMR_ENABLE_INSTALLER_AUTO=1; passed via the
	// unit environment, not argv. iuserpass rides argv (like Moodle's
	// installer) — /proc hidepid (GH #499) keeps it unreadable to other
	// tenants for the brief install window.
	php := phpCLIFor(req.OSUser)
	args := []string{
		php, "-d", "memory_limit=512M", "-d", "max_input_vars=5000",
		filepath.Join(installPath, "contrib", "util", "installScripts", "InstallerAuto.php"),
		"server=" + dbHost,
		"port=3306",
		"login=" + req.DBUser,
		"pass=" + req.DBPassword,
		"dbname=" + req.DBName,
		"no_root_db_access=1",
		"site=default",
		"iuser=" + req.AdminUser,
		"iuname=Administrator",
		"iuserpass=" + req.AdminPass,
		"igroup=" + req.SiteTitle,
	}
	cmd := buildSystemdRunCmdEnv(ctx, req.OSUser, []string{"OPENEMR_ENABLE_INSTALLER_AUTO=1"}, args...)
	cmd.Dir = installPath
	out, runErr := runBoundedOutput(cmd, 0)
	// InstallerAuto prints "ERROR: ..." on failure but exits 0, so scan
	// stdout too (silent-exit-0 class).
	if runErr != nil || strings.Contains(string(out), "ERROR:") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("openemr InstallerAuto: %v (%s)", runErr, truncateStr(string(out), 1024))}
	}

	if err := writeOpenEMRNginx(ctx, siteDomainOrEmpty(req.SiteURL), req.OSUser, req.Subdirectory); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("nginx snippet: %v", err)}
	}

	return openemrInstallResp{Version: openemrVersion}, nil
}

// siteDomainOrEmpty derives the domain from site_url, returning "" on
// failure so the caller can decide (the nginx writer validates too).
func siteDomainOrEmpty(siteURL string) string {
	d, err := DomainFromSiteURL(siteURL)
	if err != nil {
		return ""
	}
	return d
}

func openemrDownload(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openemrTarballURL, nil)
	if err != nil {
		return fmt.Errorf("openemr download: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("openemr download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("openemr download: HTTP %d from %s", resp.StatusCode, openemrTarballURL)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("openemr download: create %s: %w", dest, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return fmt.Errorf("openemr download: copy: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != openemrTarballSHA {
		return fmt.Errorf("openemr download: SHA256 mismatch: got %s want %s", got, openemrTarballSHA)
	}
	return nil
}

func init() {
	RegisterAppInstaller("openemr", openemrInstallHandler)
}
