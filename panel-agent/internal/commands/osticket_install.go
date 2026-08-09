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
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// osticket_install.go — installs osTicket (GH #962, JAB-231), an open-source
// support-ticket system, on the per-user PHP-FPM pool.
//
// osTicket ships no headless installer, so rather than reimplement its
// version-sensitive schema import + admin/config seeding we drive osTicket's
// OWN installer from a tiny CLI shim placed in setup/: it requires
// setup.inc.php, constructs the real Installer, and calls install($vars) — the
// exact code the web wizard runs. Every value (including secrets) reaches the
// shim through the environment, so nothing is interpolated into PHP source or
// passed on argv.
//
// After a successful install we lock the config read-only, delete the setup/
// tree (osTicket refuses to run while it exists), and write a per-install nginx
// drop-in: the PATH_INFO handler (osTicket routes /scp/ajax.php/... through
// /script.php/extra/path URLs the default `location ~ \.php$` never matches —
// GH #962) plus deny blocks for /include (holds ost-config.php with the DB
// password) and /setup. Removed on delete.
//
// Pinned to the official release zip, SHA-256 verified before extract, per the
// catalog's no-floating-latest bar.

const (
	osticketVersion     = "1.18.4"
	osticketTarballURL  = "https://github.com/osTicket/osTicket/releases/download/v" + osticketVersion + "/osTicket-v" + osticketVersion + ".zip"
	osticketTarballSHA  = "a4187df5f52dd41625e6b61b322245853542e12b5969a45f0a6d81f7ab8151a7"
	osticketTablePrefix = "ost_"
	osticketLangID      = "en_US"
)

type osticketInstallReq struct {
	AppType       string `json:"app_type"` // present, ignored
	OSUser        string `json:"os_user"`
	Docroot       string `json:"docroot"`
	SiteURL       string `json:"site_url"`
	HelpdeskName  string `json:"helpdesk_name"`
	HelpdeskEmail string `json:"helpdesk_email"` // system/default email; MUST differ from admin_email
	AdminFirst    string `json:"admin_first"`
	AdminLast     string `json:"admin_last"`
	AdminEmail    string `json:"admin_email"`
	AdminUsername string `json:"admin_username"`
	AdminPass     string `json:"admin_pass"`
	DBName        string `json:"db_name"`
	DBUser        string `json:"db_user"`
	DBPassword    string `json:"db_password"`
	DBHost        string `json:"db_host"`
}

type osticketInstallResp struct {
	Version string `json:"version"`
}

// osticketInstallCLI is the static install shim. It reads EVERY value from the
// environment (no interpolation, no argv), so operator input can't break out
// into PHP or a shell. Placed in <installPath>/setup/ so setup.inc.php's
// relative requires resolve; chdir(__DIR__) makes cwd irrelevant.
const osticketInstallCLI = `<?php
// Jabali headless osTicket installer (GH #962). Drives osTicket's own
// Installer->install() so schema + admin + config seeding is osTicket's code.
error_reporting(E_ERROR | E_PARSE);
$_SERVER["SCRIPT_NAME"] = "/setup/install.php";
$_SERVER["HTTP_HOST"]   = getenv("OST_HTTP_HOST") ?: "localhost";
chdir(__DIR__);
require("setup.inc.php");
require_once INC_DIR."class.installer.php";
$installer = new Installer("../include/ost-config.php");
$vars = array(
  "name"        => getenv("OST_NAME"),
  "email"       => getenv("OST_SYSEMAIL"),
  "fname"       => getenv("OST_FNAME"),
  "lname"       => getenv("OST_LNAME"),
  "admin_email" => getenv("OST_ADMINEMAIL"),
  "username"    => getenv("OST_USERNAME"),
  "passwd"      => getenv("OST_PW"),
  "passwd2"     => getenv("OST_PW"),
  "prefix"      => getenv("OST_PREFIX"),
  "dbhost"      => getenv("OST_DBHOST"),
  "dbname"      => getenv("OST_DBNAME"),
  "dbuser"      => getenv("OST_DBUSER"),
  "dbpass"      => getenv("OST_DBPASS"),
  "lang_id"     => getenv("OST_LANG"),
);
if ($installer->install($vars)) { fwrite(STDERR, "INSTALL_OK\n"); exit(0); }
fwrite(STDERR, "INSTALL_FAIL: " . json_encode($installer->getErrors()) . "\n");
exit(1);
`

func osticketInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req osticketInstallReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	switch {
	case req.OSUser == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user is required"}
	case req.Docroot == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot is required"}
	case req.AdminEmail == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	case req.HelpdeskEmail == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "helpdesk_email is required"}
	case req.AdminUsername == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_username is required"}
	case req.AdminPass == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass is required"}
	case req.DBName == "" || req.DBUser == "" || req.DBPassword == "":
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "db_name, db_user, db_password are required"}
	}
	// osTicket refuses to install when the system email == the admin email.
	// Catch it here with a clear message rather than a cryptic installer error.
	if strings.EqualFold(strings.TrimSpace(req.HelpdeskEmail), strings.TrimSpace(req.AdminEmail)) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "helpdesk_email must be different from admin_email"}
	}
	if req.HelpdeskName == "" {
		req.HelpdeskName = "Support"
	}
	if req.AdminFirst == "" {
		req.AdminFirst = "Admin"
	}
	if req.AdminLast == "" {
		req.AdminLast = "User"
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	// Docroot-only (RootOnly): no subdirectory.
	installPath, err := appInstallPath(req.Docroot, "")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	domain, derr := DomainFromSiteURL(req.SiteURL)
	if derr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("derive domain from site_url: %v", derr)}
	}
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}

	removePlaceholderIndex(ctx, installPath)

	// Download + verify, then extract as the OS user (osTicket zips a top-level
	// upload/ = the webroot; scripts/ is host-side tooling we don't serve).
	tmpDir, err := stagingMkdirTemp("osticket-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	zipPath := filepath.Join(tmpDir, "osticket.zip")
	dlCtx, dlCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer dlCancel()
	if err := osticketDownload(dlCtx, zipPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(zipPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod zip: %v", err)}
	}
	if out, err := runBoundedOutput(buildSystemdRunCmd(ctx, req.OSUser, "unzip", "-q", "-o", zipPath, "-d", installPath), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("unzip: %v (%s)", err, truncateStr(string(out), 512))}
	}
	// Move upload/{*,.*} up to installPath, then drop the unused scripts/ and
	// the now-empty upload/.
	relocate := "shopt -s dotglob nullglob; " +
		"mv " + shellQuote(filepath.Join(installPath, "upload")) + "/* " + shellQuote(installPath) + "/ && " +
		"rmdir " + shellQuote(filepath.Join(installPath, "upload")) + " && " +
		"rm -rf " + shellQuote(filepath.Join(installPath, "scripts"))
	if out, err := runBoundedOutput(buildSystemdRunShell(ctx, req.OSUser, relocate), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("relocate webroot: %v (%s)", err, truncateStr(string(out), 512))}
	}

	// Config from the sample, temporarily writable (the installer stamps
	// SECRET_SALT + DB creds into it). Done as the OS user so ownership stays
	// with the tenant and the shim (also OS-user) can write it.
	prep := "cp " + shellQuote(filepath.Join(installPath, "include", "ost-sampleconfig.php")) + " " +
		shellQuote(filepath.Join(installPath, "include", "ost-config.php")) + " && " +
		"chmod 0666 " + shellQuote(filepath.Join(installPath, "include", "ost-config.php"))
	if out, err := runBoundedOutput(buildSystemdRunShell(ctx, req.OSUser, prep), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("prepare config: %v (%s)", err, truncateStr(string(out), 512))}
	}

	// The static install shim.
	if err := dokuwikiWriteUserFile(ctx, req.OSUser, filepath.Join(installPath, "setup", "install-cli.php"), osticketInstallCLI, "0644"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// Run the installer as the OS user. Every value via env — no argv, no
	// interpolation. cwd=installPath so relative paths anchor at the webroot.
	php := phpCLIFor(req.OSUser)
	env := []string{
		"OST_NAME=" + req.HelpdeskName,
		"OST_SYSEMAIL=" + req.HelpdeskEmail,
		"OST_FNAME=" + req.AdminFirst,
		"OST_LNAME=" + req.AdminLast,
		"OST_ADMINEMAIL=" + req.AdminEmail,
		"OST_USERNAME=" + req.AdminUsername,
		"OST_PW=" + req.AdminPass,
		"OST_PREFIX=" + osticketTablePrefix,
		"OST_DBHOST=" + dbHost,
		"OST_DBNAME=" + req.DBName,
		"OST_DBUSER=" + req.DBUser,
		"OST_DBPASS=" + req.DBPassword,
		"OST_LANG=" + osticketLangID,
		"OST_HTTP_HOST=" + domain,
	}
	runCmd := buildSystemdRunCmdEnv(ctx, req.OSUser, env, php, filepath.Join(installPath, "setup", "install-cli.php"))
	runCmd.Dir = installPath
	out, runErr := runBoundedOutput(runCmd, 0)
	if runErr != nil || !strings.Contains(string(out), "INSTALL_OK") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("osticket install: %v (%s)", runErr, truncateStr(string(out), 1024))}
	}

	// Lock the config read-only and remove the installer tree (osTicket warns
	// / refuses to run while setup/ exists). Best-effort chmod of writable dirs
	// osTicket wants (avatars, attachments) is left to the app defaults.
	finalize := "chmod 0644 " + shellQuote(filepath.Join(installPath, "include", "ost-config.php")) + " && " +
		"rm -rf " + shellQuote(filepath.Join(installPath, "setup"))
	if out, err := runBoundedOutput(buildSystemdRunShell(ctx, req.OSUser, finalize), 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("finalize install: %v (%s)", err, truncateStr(string(out), 512))}
	}

	if err := writeOsTicketNginx(ctx, domain, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("nginx snippet: %v", err)}
	}

	return osticketInstallResp{Version: osticketVersion}, nil
}

// osticketSnippetPath is the per-install nginx include for a docroot osTicket.
func osticketSnippetPath(domain string) string {
	return filepath.Join(nginxJabaliDir, domain, "osticket.conf")
}

// osticketNginxConf renders the PATH_INFO handler + deny blocks. The PATH_INFO
// location is the same block the per-domain nginx_safe_options.path_info toggle
// emits (GH #962), verified live: it fires only for a real .php followed by a
// further /segment, guards existence before FPM, and carries the split
// SCRIPT_FILENAME/PATH_INFO. deny /include protects ost-config.php (DB creds).
func osticketNginxConf(osUser string) string {
	var b strings.Builder
	b.WriteString("# osTicket (GH #962): never serve the config/include tree or the installer.\n")
	b.WriteString("location ^~ /include/ { deny all; return 403; }\n")
	b.WriteString("location ^~ /setup/ { deny all; return 403; }\n")
	b.WriteString("# PATH_INFO for /scp/ajax.php/... and client-side ajax (front-controller routes).\n")
	b.WriteString("location ~ ^(?<jabali_pi_script>.+?\\.php)(?<jabali_pi_suffix>/.+)$ {\n")
	b.WriteString("    if (!-f $realpath_root$jabali_pi_script) { return 404; }\n")
	fmt.Fprintf(&b, "    fastcgi_pass unix:/run/php/jabali-%s/fpm.sock;\n", osUser)
	b.WriteString("    include fastcgi_params;\n")
	b.WriteString("    fastcgi_param SCRIPT_FILENAME $realpath_root$jabali_pi_script;\n")
	b.WriteString("    fastcgi_param PATH_INFO $jabali_pi_suffix;\n")
	b.WriteString("}\n")
	return b.String()
}

func writeOsTicketNginx(ctx context.Context, domain, osUser string) error {
	if !domainRegex.MatchString(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}
	if !osUserSafeRE.MatchString(osUser) {
		return fmt.Errorf("invalid os_user %q", osUser)
	}
	domainDir := filepath.Join(nginxJabaliDir, domain)
	if err := os.MkdirAll(domainDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", domainDir, err)
	}
	dest := osticketSnippetPath(domain)
	if err := os.WriteFile(dest, []byte(osticketNginxConf(osUser)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return reloadNginxAfterSnippet(ctx, dest)
}

// removeOsTicketNginx deletes the per-install include and reloads nginx.
// Missing file is fine — idempotent.
func removeOsTicketNginx(ctx context.Context, domain string) error {
	if !domainRegex.MatchString(domain) {
		return fmt.Errorf("invalid domain %q", domain)
	}
	dest := osticketSnippetPath(domain)
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", dest, err)
	}
	return reloadNginxAfterSnippet(ctx, dest)
}

func osticketDownload(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, osticketTarballURL, nil)
	if err != nil {
		return fmt.Errorf("osticket download: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("osticket download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("osticket download: HTTP %d from %s", resp.StatusCode, osticketTarballURL)
	}
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("osticket download: create %s: %w", dest, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return fmt.Errorf("osticket download: copy: %w", err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != osticketTarballSHA {
		return fmt.Errorf("osticket download: SHA256 mismatch: got %s want %s", got, osticketTarballSHA)
	}
	return nil
}

func init() {
	RegisterAppInstaller("osticket", osticketInstallHandler)
}
