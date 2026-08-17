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

// mediawikiInstallReq is the input shape for the MediaWiki installer.
// Same envelope as wordpressInstallReq plus a Language param; the
// agent runs MediaWiki's `php maintenance/install.php` CLI with these
// inputs so the user never sees the web installer wizard.
type mediawikiInstallReq struct {
	AppType      string `json:"app_type"`     // present, ignored
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
	Language     string `json:"language"`
}

type mediawikiInstallResp struct {
	Version string `json:"version"`
}

// mediawikiVersion is the upstream MediaWiki release this build targets.
// Bump alongside mediawikiTarballSHA256 when moving to a new release.
// Pinned releases come from https://releases.wikimedia.org/mediawiki/<major>.<minor>/.
const mediawikiVersion = "1.41.2"

// mediawikiMinorSeries derives the directory MediaWiki publishes the
// release under (e.g. 1.41.2 → 1.41).
func mediawikiMinorSeries() string {
	parts := strings.Split(mediawikiVersion, ".")
	if len(parts) < 2 {
		return mediawikiVersion
	}
	return parts[0] + "." + parts[1]
}

// mediawikiTarballURL is computed once from mediawikiVersion so a
// single-place version bump propagates.
var mediawikiTarballURL = fmt.Sprintf(
	"https://releases.wikimedia.org/mediawiki/%s/mediawiki-%s.tar.gz",
	mediawikiMinorSeries(), mediawikiVersion,
)

// mediawikiTarballSHA256 is the SHA-256 of the tarball at
// mediawikiTarballURL as of the install-time pin. Update when bumping
// the version constant; an empty value disables the integrity check
// (DEV ONLY — production builds MUST set this).
//
// To compute on a host with the URL accessible:
//
//	curl -sSL -A 'jabali-panel-agent/1.0 (+https://jabali.local)' \
//	  https://releases.wikimedia.org/mediawiki/1.41/mediawiki-1.41.2.tar.gz \
//	  | sha256sum
const mediawikiTarballSHA256 = "52bb42c34ef502f66dfc5492195ab6ac15686caea6d88afd2480b28cbd6b1ce5"

// userAgent identifies the agent in outbound HTTP requests so upstreams
// can attribute traffic and contact us if needed. Wikimedia in
// particular returns 403 to requests with the Go default UA
// (`Go-http-client/1.1`) per their robot policy. Hard-coding here
// rather than per-call so a future second app fetcher gets the same
// treatment without re-deriving the policy.
const userAgent = "jabali-panel-agent/1.0 (+https://jabali.local)"

// mediawikiAdminUserPattern enforces MediaWiki's username rules: must
// start with an uppercase letter, alnum + spaces only, max 235 chars.
// MediaWiki itself rejects usernames that fail validation, but failing
// here surfaces a friendlier error than parsing CLI installer output.
var mediawikiAdminUserPattern = regexp.MustCompile(`^[A-Z][A-Za-z0-9 _]{0,234}$`)

// mediawikiLanguagePattern matches the ISO 639-style codes MediaWiki
// accepts (en, en-gb, zh-hant, …).
var mediawikiLanguagePattern = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{1,8})?$`)

// computeMediaWikiInstallPath joins docroot + subdir, returning the
// install root the tarball will land under after --strip-components=1.
func computeMediaWikiInstallPath(docroot, subdirectory string) string {
	if subdirectory == "" {
		return docroot
	}
	return filepath.Join(docroot, subdirectory)
}

// computeMediaWikiScriptPath derives the URL-prefix MediaWiki should
// be told it lives under so internal links resolve correctly. For a
// docroot install the script path is "/"; for a subdir install it's
// "/<subdir>". MediaWiki's installer expects no trailing slash on
// non-root paths.
func computeMediaWikiScriptPath(subdirectory string) string {
	if subdirectory == "" {
		return "/"
	}
	return "/" + strings.TrimSuffix(subdirectory, "/")
}

// computeMediaWikiServerURL strips the scriptpath off site_url so the
// CLI installer's --server flag receives just the origin. site_url
// arrives from the panel as e.g. https://example.com/wiki — MediaWiki
// wants https://example.com here and /wiki on --scriptpath.
func computeMediaWikiServerURL(siteURL string) string {
	u, err := url.Parse(siteURL)
	if err != nil || u.Host == "" {
		return siteURL
	}
	return u.Scheme + "://" + u.Host
}

func downloadMediaWikiTarball(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediawikiTarballURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", mediawikiTarballURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", mediawikiTarballURL, resp.StatusCode)
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

func verifyMediaWikiSHA256(path string) error {
	if mediawikiTarballSHA256 == "" {
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
	if !strings.EqualFold(got, mediawikiTarballSHA256) {
		return fmt.Errorf("mediawiki tarball sha256 mismatch: got %s want %s", got, mediawikiTarballSHA256)
	}
	return nil
}

// extractMediaWikiTarball untars the tarball into installPath. Like
// DokuWiki, the upstream tarball wraps its content under a top-level
// `mediawiki-<version>` directory; --strip-components=1 flattens it.
func extractMediaWikiTarball(ctx context.Context, osUser, tarballPath, installPath string) error {
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

// runMediaWikiCLIInstaller drives the maintenance/install.php script
// MediaWiki ships for headless setup. The CLI generates LocalSettings.php
// in the install root containing the DB credentials + every config
// flag MediaWiki needs at runtime.
//
// `--pass` is read from the command line (on /proc/<pid>/cmdline)
// briefly while the installer runs — the trade is that MediaWiki's
// installer does not read passwords from stdin or env. Same exposure
// window as wp-cli's `--admin_password` flag the WordPress installer
// already uses; on a single-tenant slice that's acceptable.
func runMediaWikiCLIInstaller(ctx context.Context, req mediawikiInstallReq, installPath string) error {
	scriptPath := computeMediaWikiScriptPath(req.Subdirectory)
	server := computeMediaWikiServerURL(req.SiteURL)

	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	lang := req.Language
	if lang == "" {
		lang = "en"
	}

	args := []string{
		phpCLIFor(req.OSUser), filepath.Join(installPath, "maintenance", "install.php"),
		"--dbtype=mysql",
		"--dbserver=" + dbHost,
		"--dbname=" + req.DBName,
		"--dbuser=" + req.DBUser,
		"--dbpass=" + req.DBPassword,
		"--installdbuser=" + req.DBUser,
		"--installdbpass=" + req.DBPassword,
		"--server=" + server,
		"--scriptpath=" + strings.TrimSuffix(scriptPath, "/"),
		"--lang=" + lang,
		"--pass=" + req.AdminPass,
		// LocalSettings.php is generated in the working directory by
		// default; force it to install_path so a docroot-relative
		// install lands the file in the right place.
		"--confpath=" + installPath,
		req.SiteTitle,
		req.AdminUser,
	}
	cmd := buildSystemdRunCmd(ctx, req.OSUser, args...)
	cmd.Dir = installPath
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("php maintenance/install.php: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	return nil
}

func mediawikiInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req mediawikiInstallReq
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
	if req.AdminUser == "" || !mediawikiAdminUserPattern.MatchString(req.AdminUser) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_user must start with an uppercase letter and contain only letters, digits, spaces, or underscores",
		}
	}
	if req.AdminPass == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass is required"}
	}
	if len(req.AdminPass) < 10 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_pass must be at least 10 characters (MediaWiki minimum)",
		}
	}
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	}
	if req.Language != "" && !mediawikiLanguagePattern.MatchString(req.Language) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("language %q does not match expected ISO-639 form", req.Language),
		}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computeMediaWikiInstallPath(req.Docroot, req.Subdirectory)

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

	tmpDir, err := stagingMkdirTemp("mediawiki-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	tarballPath := filepath.Join(tmpDir, "mediawiki.tar.gz")

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	if err := downloadMediaWikiTarball(dlCtx, tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := verifyMediaWikiSHA256(tarballPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(tarballPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod tarball: %v", err)}
	}
	if err := extractMediaWikiTarball(ctx, req.OSUser, tarballPath, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := runMediaWikiCLIInstaller(ctx, req, installPath); err != nil {
		// Best-effort cleanup so a re-install attempt isn't blocked by
		// the half-extracted tree. Caller already marks the install
		// row "failed"; this just clears the disk side.
		cleanCmd := execCommandContext(ctx, "rm", "-rf", filepath.Join(installPath, "LocalSettings.php"))
		_ = cleanCmd.Run()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// MediaWiki's installer writes LocalSettings.php with the DB
	// password in plaintext — restrict to owner+group read so other
	// users on the box can't peek. normalizePermsToWwwData below
	// brings the rest of the tree to 0640; LocalSettings.php sits
	// inside that tree so no second chmod is needed.
	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	return mediawikiInstallResp{Version: mediawikiVersion}, nil
}

func init() {
	RegisterAppInstaller("mediawiki", mediawikiInstallHandler)
}
