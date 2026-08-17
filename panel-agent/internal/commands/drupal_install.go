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

// drupalInstallReq mirrors mediawikiInstallReq — same RequiresDB=true
// envelope plus a Profile param picking the install profile (standard /
// minimal / demo_umami) and an optional SiteMail for the From-address.
type drupalInstallReq struct {
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
	SiteMail     string `json:"site_mail"`
	Profile      string `json:"profile"`
}

type drupalInstallResp struct {
	Version string `json:"version"`
}

// drupalVersion is the upstream Drupal release this build targets.
// Bump alongside drupalTarballSHA256 when moving to a new release.
// Released tarballs live at:
// https://ftp.drupal.org/files/projects/drupal-<version>.tar.gz
//
// We pin a 10.x release rather than 11.x because 10 is LTS through
// late 2026 and ships with the broadest contrib-module compatibility.
// 11.x is still landing breaking changes for popular modules.
const drupalVersion = "10.3.6"

// drupalTarballURL is computed once from drupalVersion so a single-place
// version bump propagates.
var drupalTarballURL = fmt.Sprintf(
	"https://ftp.drupal.org/files/projects/drupal-%s.tar.gz",
	drupalVersion,
)

// drupalTarballSHA256 is the SHA-256 of the tarball at drupalTarballURL
// as of the install-time pin. Empty value disables the integrity check
// (DEV ONLY — production builds MUST set this). Update when bumping
// drupalVersion.
//
// To compute on a host with the URL accessible:
//
//	curl -sSL -A 'jabali-panel-agent/1.0 (+https://jabali.local)' \
//	  https://ftp.drupal.org/files/projects/drupal-10.3.6.tar.gz \
//	  | sha256sum
//
// Left empty until the operator computes it on the test host — the
// guard returns nil for empty per verifyDrupalSHA256 below.
const drupalTarballSHA256 = "524f51908b746280914744da0fb2cee38de8ca0b0f50f32ed2d12c0530640837"

// drupalAdminUserPattern allows alnum + dot/dash/underscore. Drupal's
// own constraint is "user.name must not contain @ or /"; this is
// stricter for safety.
var drupalAdminUserPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,60}$`)

// drupalInstallProfilePattern matches the install-profile machine name
// drush expects: lowercase + underscore + digits, max 50 chars.
var drupalInstallProfilePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,49}$`)

// computeDrupalInstallPath joins docroot + subdir, returning the
// install root the tarball will land under after --strip-components=1.
func computeDrupalInstallPath(docroot, subdirectory string) string {
	if subdirectory == "" {
		return docroot
	}
	return filepath.Join(docroot, subdirectory)
}

func downloadDrupalTarball(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, drupalTarballURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", drupalTarballURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", drupalTarballURL, resp.StatusCode)
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

func verifyDrupalSHA256(path string) error {
	if drupalTarballSHA256 == "" {
		// Fail closed (GH security batch): an unpinned hash must never
		// silently accept arbitrary downloaded code.
		return fmt.Errorf("Drupal tarball SHA-256 not pinned — refusing to install unverified code")
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
	if !strings.EqualFold(got, drupalTarballSHA256) {
		return fmt.Errorf("drupal tarball sha256 mismatch: got %s want %s", got, drupalTarballSHA256)
	}
	return nil
}

// extractDrupalTarball untars the tarball into installPath. The
// upstream tarball wraps content under a top-level drupal-<version>
// directory; --strip-components=1 flattens that out so files land
// directly under installPath/core, installPath/sites, etc.
func extractDrupalTarball(ctx context.Context, osUser, tarballPath, installPath string) error {
	cmd := buildSystemdRunCmd(ctx, osUser,
		"tar",
		"--extract",
		"--gzip",
		"--strip-components=1",
		"--file", tarballPath,
		"--directory", installPath,
	)
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("tar extract: %w (output: %s)", err, truncateStr(string(out), 512))
	}
	return nil
}

// installDrushViaComposer pulls drush into vendor/bin under installPath.
// Drupal ships its own composer.json so `composer require drush/drush`
// adds it as a dev-time CLI dependency without breaking core. Runs as
// the per-domain user via systemd-run so the resulting vendor/ tree
// lands with the right uid.
//
// COMPOSER_HOME is forced to a writable per-user path — composer's
// default ~/.composer needs to exist + be writable, and per-domain
// users on this host don't have a HOME prepopulated.
func installDrushViaComposer(ctx context.Context, osUser, installPath string) error {
	composerHome := filepath.Join(installPath, ".composer")
	if err := os.MkdirAll(composerHome, 0o755); err != nil {
		return fmt.Errorf("mkdir composer home: %w", err)
	}
	if err := execCommandContext(ctx, "chown", "-R", osUser+":"+osUser, composerHome).Run(); err != nil {
		return fmt.Errorf("chown composer home: %w", err)
	}

	// Reproducible install (GH #459): write the reviewed, pinned composer.json
	// + composer.lock over the extracted tree, then `composer install` — never
	// an open-ended `composer require`, which would re-resolve Drush and its
	// transitive deps against live upstream metadata on every install.
	for name, data := range map[string][]byte{
		"composer.json": drupalComposerJSON,
		"composer.lock": drupalComposerLock,
	} {
		p := filepath.Join(installPath, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return fmt.Errorf("write pinned %s: %w", name, err)
		}
		if err := execCommandContext(ctx, "chown", osUser+":"+osUser, p).Run(); err != nil {
			return fmt.Errorf("chown pinned %s: %w", name, err)
		}
	}
	cmd := buildSystemdRunCmd(ctx, osUser,
		"env",
		"COMPOSER_HOME="+composerHome,
		"COMPOSER_NO_INTERACTION=1",
		"composer",
		"install",
		"--working-dir="+installPath,
		"--no-dev",
		"--no-interaction",
		"--no-progress",
		"--prefer-dist",
		"--optimize-autoloader",
	)
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("composer install (pinned drush): %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	return nil
}

// runDrushSiteInstall drives drush's site:install command. The
// --account-pass / --db-url flags both contain secrets that briefly
// appear on /proc/<pid>/cmdline while the install runs — same
// exposure window as wp-cli's --admin_password and MediaWiki's
// --pass. Acceptable on a single-tenant slice.
func runDrushSiteInstall(ctx context.Context, req drupalInstallReq, installPath string) error {
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	profile := req.Profile
	if profile == "" {
		profile = "standard"
	}
	siteMail := req.SiteMail
	if siteMail == "" {
		siteMail = req.AdminEmail
	}

	dbURL := fmt.Sprintf("mysql://%s:%s@%s/%s",
		urlEscape(req.DBUser),
		urlEscape(req.DBPassword),
		dbHost,
		req.DBName,
	)

	composerHome := filepath.Join(installPath, ".composer")
	args := []string{
		"env",
		"COMPOSER_HOME=" + composerHome,
		filepath.Join(installPath, "vendor", "bin", "drush"),
		"site:install",
		profile,
		"--db-url=" + dbURL,
		"--account-name=" + req.AdminUser,
		"--account-pass=" + req.AdminPass,
		"--account-mail=" + req.AdminEmail,
		"--site-name=" + req.SiteTitle,
		"--site-mail=" + siteMail,
		"--yes",
	}
	cmd := buildSystemdRunCmd(ctx, req.OSUser, args...)
	cmd.Dir = installPath
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("drush site:install: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	return nil
}

// runDrushDisableAssetAggregation turns off Drupal's CSS/JS preprocessing
// so the site doesn't reference /sites/default/files/{css,js}/<hash>
// URLs that only work with Apache's .htaccess-based on-demand regen.
// See the caller for the rationale.
func runDrushDisableAssetAggregation(ctx context.Context, req drupalInstallReq, installPath string) error {
	composerHome := filepath.Join(installPath, ".composer")
	drush := filepath.Join(installPath, "vendor", "bin", "drush")
	for _, key := range []string{"css.preprocess", "js.preprocess"} {
		args := []string{
			"env",
			"COMPOSER_HOME=" + composerHome,
			drush,
			"config:set",
			"system.performance",
			key,
			"0",
			"--yes",
		}
		cmd := buildSystemdRunCmd(ctx, req.OSUser, args...)
		cmd.Dir = installPath
		if out, err := runBoundedOutput(cmd, 0); err != nil {
			return fmt.Errorf("drush config:set system.performance %s 0: %w (output: %s)",
				key, err, truncateStr(string(out), 512))
		}
	}
	return nil
}

// urlEscape percent-encodes characters that would break the db-url
// query string drush parses. We intentionally only escape the small
// set of characters that actually break URLs (#, ?, /, @, :, %, &)
// rather than calling net/url.QueryEscape which would encode + and
// space differently than mysql:// expects.
func urlEscape(s string) string {
	r := strings.NewReplacer(
		"%", "%25",
		"@", "%40",
		":", "%3A",
		"/", "%2F",
		"?", "%3F",
		"#", "%23",
		"&", "%26",
		" ", "%20",
	)
	return r.Replace(s)
}

func drupalInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req drupalInstallReq
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
	if req.AdminUser == "" || !drupalAdminUserPattern.MatchString(req.AdminUser) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_user must be 1-60 chars of letters, digits, dot, dash, or underscore",
		}
	}
	if req.AdminPass == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass is required"}
	}
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	}
	if req.Profile != "" && !drupalInstallProfilePattern.MatchString(req.Profile) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("profile %q does not match install-profile machine-name form", req.Profile),
		}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computeDrupalInstallPath(req.Docroot, req.Subdirectory)

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

	// Stage outside /tmp so the agent's PrivateTmp sandbox doesn't hide
	// the tarball from the per-user systemd-run extract unit. See
	// staging_tmp.go for the full rationale. stagingMkdirTemp already
	// ensures 0755 on the random dir.
	tmpDir, err := stagingMkdirTemp("drupal-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarballPath := filepath.Join(tmpDir, "drupal.tar.gz")

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	if err := downloadDrupalTarball(dlCtx, tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := verifyDrupalSHA256(tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarballPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}
	if err := extractDrupalTarball(ctx, req.OSUser, tarballPath, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// Composer + drush is the long step. Bump timeout to 15 min — on a
	// cold cache `composer require drush/drush` can pull 60+ packages.
	composerCtx, composerCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer composerCancel()
	if err := installDrushViaComposer(composerCtx, req.OSUser, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := runDrushSiteInstall(ctx, req, installPath); err != nil {
		// Best-effort cleanup of half-written settings.php so a
		// re-install attempt isn't blocked.
		settingsPath := filepath.Join(installPath, "sites", "default", "settings.php")
		_ = execCommandContext(ctx, "rm", "-f", settingsPath).Run()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// Disable CSS/JS aggregation. Drupal's default is to emit aggregated
	// bundles at /sites/default/files/{css,js}/<hash>.{css,js} and expect
	// Apache's .htaccess rewrite to route 404s back through index.php for
	// on-demand regen. We serve via nginx which doesn't read .htaccess;
	// adding the equivalent rewrite is awkward for subdir installs (the
	// vhost is domain-level). Turning preprocessing off makes Drupal serve
	// individual, never-aggregated source files (core/themes/.../css/*.css)
	// which are real files on disk — nginx serves them directly.
	// Trade-off: more HTTP requests per page. Acceptable for small sites.
	if err := runDrushDisableAssetAggregation(ctx, req, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// Write the per-install nginx rewrite snippet for subdir installs.
	// Drupal's pretty URLs (/<subdir>/user, /<subdir>/admin/*) would
	// otherwise 404 because our generic vhost's try_files fallback
	// targets /index.php at the docroot, not /<subdir>/index.php.
	// No-op for docroot installs.
	subdir := strings.Trim(req.Subdirectory, "/")
	if subdir != "" {
		domain, err := DomainFromSiteURL(req.SiteURL)
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("extract domain: %v", err)}
		}
		if err := writeAppRewrite(ctx, "drupal", domain, req.OSUser, subdir); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write nginx rewrite: %v", err)}
		}
	}

	return drupalInstallResp{Version: drupalVersion}, nil
}

func init() {
	RegisterAppInstaller("drupal", drupalInstallHandler)
}
