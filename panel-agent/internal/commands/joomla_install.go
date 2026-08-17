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

// joomlaInstallReq is the input for the Joomla installer. Mirrors
// drupalInstallReq except for AdminFullName (Joomla wants a display
// name distinct from the login username) and the omitted profile/site
// mail params (Joomla's CLI installer doesn't expose them).
type joomlaInstallReq struct {
	AppType       string `json:"app_type"`
	OSUser        string `json:"os_user"`
	Docroot       string `json:"docroot"`
	Subdirectory  string `json:"subdirectory"`
	SiteURL       string `json:"site_url"`
	UseWWW        bool   `json:"use_www"`
	DBName        string `json:"db_name"`
	DBUser        string `json:"db_user"`
	DBPassword    string `json:"db_password"`
	DBHost        string `json:"db_host"`
	SiteTitle     string `json:"site_title"`
	AdminUser     string `json:"admin_user"`
	AdminPass     string `json:"admin_pass"`
	AdminEmail    string `json:"admin_email"`
	AdminFullName string `json:"admin_full_name"`
}

type joomlaInstallResp struct {
	Version string `json:"version"`
}

// joomlaVersion is the upstream Joomla 5 release this build targets.
// Bump alongside joomlaTarballSHA256 when moving to a new release.
// Released tarballs live at:
// https://downloads.joomla.org/cms/joomla5/<dashed>/Joomla_<dashed>-Stable-Full_Package.tar.gz
//
// We pin a 5.x release rather than 4.x because Joomla 5 is the
// current major; 5.x has the longest support window (through 2027).
const joomlaVersion = "5.2.0"

// joomlaDashedVersion turns 5.2.0 into 5-2-0 for the URL path Joomla
// serves releases under.
func joomlaDashedVersion() string {
	return strings.ReplaceAll(joomlaVersion, ".", "-")
}

var joomlaTarballURL = fmt.Sprintf(
	"https://downloads.joomla.org/cms/joomla5/%s/Joomla_%s-Stable-Full_Package.tar.gz",
	joomlaDashedVersion(), joomlaDashedVersion(),
)

// joomlaTarballSHA256 is the SHA-256 of the tarball at joomlaTarballURL
// as of the install-time pin. Empty value disables the integrity
// check (DEV ONLY — production builds MUST set this).
//
// To compute on a host with the URL accessible:
//
//	curl -sSL -A 'jabali-panel-agent/1.0 (+https://jabali.local)' \
//	  https://downloads.joomla.org/cms/joomla5/5-2-0/Joomla_5-2-0-Stable-Full_Package.tar.gz \
//	  | sha256sum
const joomlaTarballSHA256 = "0124ebde9b535311bdba2e128f3f4928918c021491e4c81aa5373e1290f82329"

// joomlaAdminUserPattern allows alnum + dot/dash/underscore. Joomla's
// own constraint is roughly the same plus disallowing leading/trailing
// whitespace; we're stricter to keep the rule trivial.
var joomlaAdminUserPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,150}$`)

// computeJoomlaInstallPath joins docroot + subdir, returning the
// install root the tarball will land under.
//
// IMPORTANT: Joomla's tarball does NOT wrap content under a top-level
// joomla-<version>/ directory the way DokuWiki/MediaWiki/Drupal do —
// it explodes directly to the install root. So we extract WITHOUT
// --strip-components=1.
func computeJoomlaInstallPath(docroot, subdirectory string) string {
	if subdirectory == "" {
		return docroot
	}
	return filepath.Join(docroot, subdirectory)
}

func downloadJoomlaTarball(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, joomlaTarballURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", joomlaTarballURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", joomlaTarballURL, resp.StatusCode)
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

func verifyJoomlaSHA256(path string) error {
	if joomlaTarballSHA256 == "" {
		// Fail closed (GH security batch): an unpinned hash must never
		// silently accept arbitrary downloaded code.
		return fmt.Errorf("Joomla tarball SHA-256 not pinned — refusing to install unverified code")
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
	if !strings.EqualFold(got, joomlaTarballSHA256) {
		return fmt.Errorf("joomla tarball sha256 mismatch: got %s want %s", got, joomlaTarballSHA256)
	}
	return nil
}

// extractJoomlaTarball untars the tarball into installPath. Unlike the
// other CMS tarballs we ship, Joomla's archive doesn't have a top-
// level wrapper directory, so NO --strip-components.
func extractJoomlaTarball(ctx context.Context, osUser, tarballPath, installPath string) error {
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

// runJoomlaCLIInstaller drives the headless installer Joomla 4+ ships.
// Same `--admin-password` exposure window on /proc/<pid>/cmdline as
// the wp-cli / drush / mediawiki installers — acceptable on a single-
// tenant slice.
func runJoomlaCLIInstaller(ctx context.Context, req joomlaInstallReq, installPath string) error {
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	adminFullName := req.AdminFullName
	if adminFullName == "" {
		adminFullName = "Super User"
	}

	args := []string{
		phpCLIFor(req.OSUser), filepath.Join(installPath, "installation", "joomla.php"),
		"install",
		"--site-name=" + req.SiteTitle,
		"--admin-email=" + req.AdminEmail,
		"--admin-username=" + req.AdminUser,
		"--admin-user=" + adminFullName,
		"--admin-password=" + req.AdminPass,
		"--db-type=mysqli",
		"--db-host=" + dbHost,
		"--db-user=" + req.DBUser,
		"--db-pass=" + req.DBPassword,
		"--db-name=" + req.DBName,
		"--db-prefix=jos_",
	}
	cmd := buildSystemdRunCmd(ctx, req.OSUser, args...)
	cmd.Dir = installPath
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("php installation/joomla.php install: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	return nil
}

// removeJoomlaInstallationDir deletes the `installation/` folder
// after a successful CLI install. Joomla refuses to load while that
// folder exists — it bounces every request to the web installer
// even though we've already provisioned the site headlessly.
func removeJoomlaInstallationDir(ctx context.Context, osUser, installPath string) error {
	cmd := buildSystemdRunCmd(ctx, osUser, "rm", "-rf", filepath.Join(installPath, "installation"))
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("rm installation/: %w (output: %s)", err, truncateStr(string(out), 256))
	}
	return nil
}

func joomlaInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req joomlaInstallReq
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
	if req.AdminUser == "" || !joomlaAdminUserPattern.MatchString(req.AdminUser) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_user must be 1-150 chars of letters, digits, dot, dash, or underscore",
		}
	}
	if req.AdminPass == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass is required"}
	}
	if len(req.AdminPass) < 12 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_pass must be at least 12 characters (Joomla minimum)",
		}
	}
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computeJoomlaInstallPath(req.Docroot, req.Subdirectory)

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

	tmpDir, err := stagingMkdirTemp("joomla-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarballPath := filepath.Join(tmpDir, "joomla.tar.gz")

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	if err := downloadJoomlaTarball(dlCtx, tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := verifyJoomlaSHA256(tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarballPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}
	if err := extractJoomlaTarball(ctx, req.OSUser, tarballPath, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := runJoomlaCLIInstaller(ctx, req, installPath); err != nil {
		// Best-effort cleanup so re-install isn't blocked. Don't remove
		// installation/ here — the user may want to retry the web
		// installer manually for diagnostics.
		_ = execCommandContext(ctx, "rm", "-f", filepath.Join(installPath, "configuration.php")).Run()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// Joomla refuses to load past the splash screen while
	// installation/ exists — even after a successful headless install.
	if err := removeJoomlaInstallationDir(ctx, req.OSUser, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// Per-install nginx rewrite for subdir installs — same rationale as
	// Drupal (pretty URLs under /<subdir>/ need to land at
	// /<subdir>/index.php, not the docroot's index.php).
	subdir := strings.Trim(req.Subdirectory, "/")
	if subdir != "" {
		domain, err := DomainFromSiteURL(req.SiteURL)
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("extract domain: %v", err)}
		}
		if err := writeAppRewrite(ctx, "joomla", domain, req.OSUser, subdir); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write nginx rewrite: %v", err)}
		}
	}

	return joomlaInstallResp{Version: joomlaVersion}, nil
}

func init() {
	RegisterAppInstaller("joomla", joomlaInstallHandler)
}
