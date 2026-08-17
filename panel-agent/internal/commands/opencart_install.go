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

type opencartInstallReq struct {
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
	AdminUser    string `json:"admin_user"`
	AdminPass    string `json:"admin_pass"`
	AdminEmail   string `json:"admin_email"`
}

type opencartInstallResp struct {
	Version string `json:"version"`
}

// opencartVersion is the upstream OpenCart release this build targets.
// Bump alongside opencartZipSHA256 when moving to a new release.
//
// Releases: https://github.com/opencart/opencart/releases
// We pin a 4.x release because 4 is the current major and ships the
// composer-free release zip the cli_install.php expects.
const opencartVersion = "4.0.2.3"

var opencartZipURL = fmt.Sprintf(
	"https://github.com/opencart/opencart/releases/download/%s/opencart-%s.zip",
	opencartVersion, opencartVersion,
)

// opencartZipSHA256 is the SHA-256 of the zip at opencartZipURL as of
// the install-time pin. Empty value disables the check (DEV ONLY).
//
//	curl -sSL -A 'jabali-panel-agent/1.0 (+https://jabali.local)' \
//	  -L https://github.com/opencart/opencart/releases/download/4.0.2.3/opencart-4.0.2.3.zip \
//	  | sha256sum
const opencartZipSHA256 = "8d18dc67e23d9937925e2efb470f6fabed98aeafd64550c43b6f413d174e5117"

// opencartAdminUserPattern: OpenCart admin usernames are 3-20 chars,
// no special-character restriction beyond email-style chars.
var opencartAdminUserPattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{3,20}$`)

// findOpenCartWebroot locates the OpenCart webroot inside the staging
// dir. Probes the common layouts in order:
//   - stagingDir itself (OpenCart 4.x release zip drops files at root)
//   - stagingDir/upload (OpenCart 3.x layout)
//   - stagingDir/<single-subdir>/upload (some forks/repacks)
//   - stagingDir/<single-subdir> (versioned wrapper without upload/)
//
// The webroot is identified by the simultaneous presence of
// `index.php` AND an `admin/` directory — narrow enough to avoid
// false positives on archive landing pages or installer wrappers.
func findOpenCartWebroot(stagingDir string) (string, error) {
	isWebroot := func(dir string) bool {
		idx, err := os.Stat(filepath.Join(dir, "index.php"))
		if err != nil || idx.IsDir() {
			return false
		}
		adm, err := os.Stat(filepath.Join(dir, "admin"))
		if err != nil || !adm.IsDir() {
			return false
		}
		return true
	}

	if isWebroot(stagingDir) {
		return stagingDir, nil
	}
	if up := filepath.Join(stagingDir, "upload"); isWebroot(up) {
		return up, nil
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return "", fmt.Errorf("read stage dir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(stagingDir, e.Name())
		if up := filepath.Join(sub, "upload"); isWebroot(up) {
			return up, nil
		}
		if isWebroot(sub) {
			return sub, nil
		}
	}
	return "", fmt.Errorf("opencart webroot (dir with index.php + admin/) not found under %s — release zip layout may have changed", stagingDir)
}

func computeOpenCartInstallPath(docroot, subdirectory string) string {
	if subdirectory == "" {
		return docroot
	}
	return filepath.Join(docroot, subdirectory)
}

func downloadOpenCartZip(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opencartZipURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", opencartZipURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", opencartZipURL, resp.StatusCode)
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

func verifyOpenCartSHA256(path string) error {
	if opencartZipSHA256 == "" {
		// Fail closed (GH security batch): an unpinned hash must never
		// silently accept arbitrary downloaded code.
		return fmt.Errorf("OpenCart zip SHA-256 not pinned — refusing to install unverified code")
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
	if !strings.EqualFold(got, opencartZipSHA256) {
		return fmt.Errorf("opencart zip sha256 mismatch: got %s want %s", got, opencartZipSHA256)
	}
	return nil
}

// extractOpenCartZip unzips into staging then flattens the OpenCart
// payload into installPath. Layout-resilient: OpenCart 3.x release
// zips wrap the webroot under `upload/`; OpenCart 4.x release zips
// drop the webroot directly at the zip root (with no wrapper).
// We probe for index.php to find the right source dir rather than
// hardcoding either layout — keeps the installer working across
// future major-version repackaging.
//
// Also renames the two "*.dist" config samples to their final names —
// OpenCart's installer expects `config.php` and `admin/config.php` to
// be writable, so we create them empty (mode 0640) and let the
// installer write into them.
func extractOpenCartZip(ctx context.Context, osUser, zipPath, installPath, stagingDir string) error {
	cmd := buildSystemdRunCmd(ctx, osUser, "unzip", "-q", "-o", zipPath, "-d", stagingDir)
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("unzip: %w (output: %s)", err, truncateStr(string(out), 512))
	}

	// Probe for the OpenCart webroot. Recognise it by the presence of
	// both `index.php` and `admin/` — that combination is unique enough
	// in OpenCart's distribution to avoid false positives on a
	// future-version reorg.
	src, err := findOpenCartWebroot(stagingDir)
	if err != nil {
		return err
	}
	// Don't `rm -rf %s` here — stagingDir's parent (tmpDir) is mode 0755
	// root-owned, so the per-domain user can't delete entries from it.
	// Caller's `defer os.RemoveAll(tmpDir)` handles cleanup as root.
	mvCmd := buildSystemdRunCmd(ctx, osUser, "sh", "-c",
		fmt.Sprintf("cp -a %s/. %s/",
			shellQuote(src), shellQuote(installPath)),
	)
	mvOut, err := runBoundedOutput(mvCmd, 0)
	if err != nil {
		return fmt.Errorf("move opencart contents: %w (output: %s)", err, truncateStr(string(mvOut), 512))
	}

	// OpenCart ships config-dist.php samples that need to become
	// config.php in storefront and admin roots. Touch the files so
	// cli_install.php can write into them.
	for _, p := range []string{"config.php", filepath.Join("admin", "config.php")} {
		full := filepath.Join(installPath, p)
		touchCmd := buildSystemdRunCmd(ctx, osUser, "sh", "-c",
			fmt.Sprintf("touch %s && chmod 0640 %s", shellQuote(full), shellQuote(full)),
		)
		if tOut, err := runBoundedOutput(touchCmd, 0); err != nil {
			return fmt.Errorf("touch %s: %w (output: %s)", p, err, truncateStr(string(tOut), 256))
		}
	}
	return nil
}

// runOpenCartCLIInstaller drives `php install/cli_install.php install`.
// OpenCart's CLI installer materialises the schema, writes both
// config.php files, and creates the admin user in one shot.
func runOpenCartCLIInstaller(ctx context.Context, req opencartInstallReq, installPath string) error {
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	httpServer := req.SiteURL
	if !strings.HasSuffix(httpServer, "/") {
		httpServer += "/"
	}

	args := []string{
		phpCLIFor(req.OSUser), filepath.Join(installPath, "install", "cli_install.php"),
		"install",
		"--db_hostname", dbHost,
		"--db_username", req.DBUser,
		"--db_password", req.DBPassword,
		"--db_database", req.DBName,
		"--db_driver", "mysqli",
		"--db_port", "3306",
		"--db_prefix", "oc_",
		"--username", req.AdminUser,
		"--password", req.AdminPass,
		"--email", req.AdminEmail,
		"--http_server", httpServer,
	}
	cmd := buildSystemdRunCmd(ctx, req.OSUser, args...)
	cmd.Dir = installPath
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("install/cli_install.php install: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	// cli_install.php prints "ERROR: ..." and exits 0 on validation
	// failures (password length, missing PHP extension, unwritable
	// config, etc.). Exit code alone can't distinguish success from a
	// silent failure, so scan stdout for ERROR: and success for the
	// known happy-path string.
	outStr := string(out)
	if strings.Contains(outStr, "ERROR:") || !strings.Contains(outStr, "SUCCESS") {
		return fmt.Errorf("install/cli_install.php rejected: %s", truncateStr(outStr, 1024))
	}
	return nil
}

// removeOpenCartInstallDir deletes the `install/` directory after a
// successful install. OpenCart's runtime checks for its existence and
// surfaces a "delete the install folder" warning in admin if it's
// still around.
func removeOpenCartInstallDir(ctx context.Context, osUser, installPath string) error {
	cmd := buildSystemdRunCmd(ctx, osUser, "rm", "-rf", filepath.Join(installPath, "install"))
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("rm install/: %w (output: %s)", err, truncateStr(string(out), 256))
	}
	return nil
}

func opencartInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req opencartInstallReq
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
	if req.AdminUser == "" || !opencartAdminUserPattern.MatchString(req.AdminUser) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_user must be 3-20 chars of letters, digits, dot, dash, underscore, or @",
		}
	}
	if len(req.AdminPass) < 4 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_pass must be at least 4 characters (OpenCart minimum)",
		}
	}
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computeOpenCartInstallPath(req.Docroot, req.Subdirectory)

	if req.Subdirectory != "" {
		mkdirCmd := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath)
		if out, err := runBoundedOutput(mkdirCmd, 0); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v (output: %s)", installPath, err, truncateStr(string(out), 256))}
		}
	}

	removePlaceholderIndex(ctx, installPath)

	tmpDir, err := stagingMkdirTemp("opencart-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	zipPath := filepath.Join(tmpDir, "opencart.zip")

	dlCtx, dlCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer dlCancel()
	if err := downloadOpenCartZip(dlCtx, zipPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := verifyOpenCartSHA256(zipPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Chmod(zipPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod zip: %v", err)}
	}

	stagingDir := filepath.Join(tmpDir, "stage")
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir staging: %v", err)}
	}
	if err := execCommandContext(ctx, "chown", "-R", req.OSUser+":"+req.OSUser, stagingDir).Run(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown staging: %v", err)}
	}

	if err := extractOpenCartZip(ctx, req.OSUser, zipPath, installPath, stagingDir); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// GH #953: make OpenCart's Twig loader compatible with jabali's per-user
	// open_basedir confinement before it ever renders a page.
	if err := patchOpenCartOpenBasedir(ctx, req.OSUser, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("patch twig open_basedir: %v", err)}
	}

	if err := runOpenCartCLIInstaller(ctx, req, installPath); err != nil {
		// Best-effort cleanup so re-install works.
		_ = execCommandContext(ctx, "rm", "-f",
			filepath.Join(installPath, "config.php"),
			filepath.Join(installPath, "admin", "config.php"),
		).Run()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := removeOpenCartInstallDir(ctx, req.OSUser, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	return opencartInstallResp{Version: opencartVersion}, nil
}

// rewriteOpenCartTwigLoader rewrites OpenCart's hardcoded Twig base path (GH
// #953). OpenCart 4.x builds its loader as
//
//	new \Twig\Loader\FilesystemLoader('/', $this->root)
//
// using "/" as the base search path. Under jabali's per-user open_basedir
// confinement, PHP's is_dir('/') is denied, so every storefront and admin page
// dies with `open_basedir restriction in effect. File(/) ... The "/" directory
// does not exist`. $this->root is already the install web root — inside the
// user's home, hence inside open_basedir — so pointing the loader at it renders
// correctly while leaving the hardening intact (absolute template paths, which
// OpenCart passes at render time, resolve the same either way).
//
// Returns the rewritten content and whether a change was made. A miss (upstream
// changed the construction) is a no-op, not an error — we never guess at a
// shape we don't recognise.
func rewriteOpenCartTwigLoader(content string) (string, bool) {
	const oldLoader = `new \Twig\Loader\FilesystemLoader('/', $this->root)`
	const newLoader = `new \Twig\Loader\FilesystemLoader($this->root, $this->root)`
	if !strings.Contains(content, oldLoader) {
		return content, false
	}
	return strings.Replace(content, oldLoader, newLoader, 1), true
}

// patchOpenCartOpenBasedir applies rewriteOpenCartTwigLoader to the installed
// system/library/template/twig.php, writing it back as the OS user. Idempotent
// and a no-op when the marker is absent (already patched / upstream changed).
func patchOpenCartOpenBasedir(ctx context.Context, osUser, installPath string) error {
	twigPath := filepath.Join(installPath, "system", "library", "template", "twig.php")
	raw, err := os.ReadFile(twigPath) // #nosec G304 — fixed path under the validated install root
	if err != nil {
		return fmt.Errorf("read twig.php: %w", err)
	}
	patched, changed := rewriteOpenCartTwigLoader(string(raw))
	if !changed {
		return nil
	}
	return dokuwikiWriteUserFile(ctx, osUser, twigPath, patched, "0644")
}

func init() {
	RegisterAppInstaller("opencart", opencartInstallHandler)
}
