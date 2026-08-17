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

type prestashopInstallReq struct {
	AppType        string `json:"app_type"`
	OSUser         string `json:"os_user"`
	Docroot        string `json:"docroot"`
	Subdirectory   string `json:"subdirectory"`
	SiteURL        string `json:"site_url"`
	UseWWW         bool   `json:"use_www"`
	DBName         string `json:"db_name"`
	DBUser         string `json:"db_user"`
	DBPassword     string `json:"db_password"`
	DBHost         string `json:"db_host"`
	SiteTitle      string `json:"site_title"`
	AdminEmail     string `json:"admin_email"`
	AdminPass      string `json:"admin_pass"`
	AdminFirstName string `json:"admin_first_name"`
	AdminLastName  string `json:"admin_last_name"`
	Country        string `json:"country"`
	Language       string `json:"language"`
}

type prestashopInstallResp struct {
	Version string `json:"version"`
}

// prestashopVersion is the upstream PrestaShop release this build
// targets. Bump alongside prestashopOuterZipSHA256 when moving to a
// new release.
//
// Releases: https://github.com/PrestaShop/PrestaShop/releases
// PrestaShop ships an *outer* zip that contains an *inner* zip plus
// an Install_PrestaShop.html landing — that's the upstream packaging
// choice, not a wrapper convenience. The agent unzips both layers
// before running the CLI installer.
const prestashopVersion = "8.2.0"

var prestashopOuterZipURL = fmt.Sprintf(
	"https://github.com/PrestaShop/PrestaShop/releases/download/%s/prestashop_%s.zip",
	prestashopVersion, prestashopVersion,
)

// prestashopOuterZipSHA256 is the SHA-256 of the outer zip at
// prestashopOuterZipURL as of the install-time pin. Empty value
// disables the integrity check (DEV ONLY).
const prestashopOuterZipSHA256 = "d512d05ffa30dcde9819b6a867694fa24b447ae32acaded6cce9d19472bc9cd1"

// prestashopCountryPattern matches the two-letter ISO 3166-1 country
// codes PrestaShop's installer accepts (lowercase).
var prestashopCountryPattern = regexp.MustCompile(`^[a-z]{2}$`)

var prestashopLanguagePattern = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{1,8})?$`)

func computePrestaShopInstallPath(docroot, subdirectory string) string {
	if subdirectory == "" {
		return docroot
	}
	return filepath.Join(docroot, subdirectory)
}

func downloadPrestaShopZip(ctx context.Context, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, prestashopOuterZipURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{Timeout: 15 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", prestashopOuterZipURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", prestashopOuterZipURL, resp.StatusCode)
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

func verifyPrestaShopSHA256(path string) error {
	if prestashopOuterZipSHA256 == "" {
		// Fail closed (GH security batch): an unpinned hash must never
		// silently accept arbitrary downloaded code.
		return fmt.Errorf("PrestaShop zip SHA-256 not pinned — refusing to install unverified code")
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
	if !strings.EqualFold(got, prestashopOuterZipSHA256) {
		return fmt.Errorf("prestashop outer zip sha256 mismatch: got %s want %s", got, prestashopOuterZipSHA256)
	}
	return nil
}

// extractPrestaShopZip handles PrestaShop's double-zip distribution:
//  1. unzip outer.zip into staging/ → produces prestashop.zip + html
//  2. unzip staging/prestashop.zip → produces install/, modules/, etc.
//     landing directly at staging/prestashop/  (or directly at staging/
//     depending on the release; we handle both)
//  3. cp -a staging/<wherever>/. installPath/
func extractPrestaShopZip(ctx context.Context, osUser, outerZip, installPath, stagingDir string) error {
	cmd := buildSystemdRunCmd(ctx, osUser, "unzip", "-q", "-o", outerZip, "-d", stagingDir)
	if out, err := runBoundedOutput(cmd, 0); err != nil {
		return fmt.Errorf("unzip outer: %w (output: %s)", err, truncateStr(string(out), 512))
	}

	innerZip := filepath.Join(stagingDir, "prestashop.zip")
	if _, err := os.Stat(innerZip); err != nil {
		return fmt.Errorf("inner prestashop.zip missing from outer archive: %w", err)
	}

	innerStage := filepath.Join(stagingDir, "inner")
	if err := execCommandContext(ctx, "mkdir", "-p", innerStage).Run(); err != nil {
		return fmt.Errorf("mkdir inner stage: %w", err)
	}
	if err := execCommandContext(ctx, "chown", osUser+":"+osUser, innerStage).Run(); err != nil {
		return fmt.Errorf("chown inner stage: %w", err)
	}

	cmd2 := buildSystemdRunCmd(ctx, osUser, "unzip", "-q", "-o", innerZip, "-d", innerStage)
	if out, err := runBoundedOutput(cmd2, 0); err != nil {
		return fmt.Errorf("unzip inner: %w (output: %s)", err, truncateStr(string(out), 512))
	}

	// Don't `rm -rf %s` here — stagingDir's parent (tmpDir) is mode 0755
	// root-owned, so the per-domain user can't delete entries from it.
	// Caller's `defer os.RemoveAll(tmpDir)` handles cleanup as root.
	mvCmd := buildSystemdRunCmd(ctx, osUser, "sh", "-c",
		fmt.Sprintf("cp -a %s/. %s/",
			shellQuote(innerStage), shellQuote(installPath)),
	)
	if out, err := runBoundedOutput(mvCmd, 0); err != nil {
		return fmt.Errorf("move prestashop contents: %w (output: %s)", err, truncateStr(string(out), 512))
	}
	return nil
}

// runPrestaShopCLIInstaller drives `php install/index_cli.php`. The
// installer reads its config from --flag=value pairs, materialises
// the schema, writes app/config/parameters.php, and creates the
// admin user in one shot.
func runPrestaShopCLIInstaller(ctx context.Context, req prestashopInstallReq, installPath string) error {
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	country := req.Country
	if country == "" {
		country = "us"
	}
	language := req.Language
	if language == "" {
		language = "en"
	}
	firstName := req.AdminFirstName
	if firstName == "" {
		firstName = "Site"
	}
	lastName := req.AdminLastName
	if lastName == "" {
		lastName = "Owner"
	}

	domain := ""
	baseURI := "/"
	if u, err := url.Parse(req.SiteURL); err == nil && u.Host != "" {
		domain = u.Hostname()
		if u.Path != "" {
			baseURI = u.Path
			if !strings.HasSuffix(baseURI, "/") {
				baseURI += "/"
			}
		}
	}

	args := []string{
		phpCLIFor(req.OSUser), filepath.Join(installPath, "install", "index_cli.php"),
		"--step=all",
		"--language=" + language,
		"--country=" + country,
		"--domain=" + domain,
		"--base_uri=" + baseURI,
		"--db_server=" + dbHost,
		"--db_name=" + req.DBName,
		"--db_user=" + req.DBUser,
		"--db_password=" + req.DBPassword,
		"--prefix=ps_",
		"--engine=InnoDB",
		"--name=" + req.SiteTitle,
		"--activity=10",
		"--firstname=" + firstName,
		"--lastname=" + lastName,
		"--password=" + req.AdminPass,
		"--email=" + req.AdminEmail,
		"--newsletter=0",
		"--send_email=0",
	}
	cmd := buildSystemdRunCmd(ctx, req.OSUser, args...)
	cmd.Dir = installPath
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("install/index_cli.php: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	return nil
}

// removePrestaShopInstallDir deletes the install/ directory after
// install. PrestaShop's runtime won't load while it exists.
func removePrestaShopInstallDir(ctx context.Context, osUser, installPath string) error {
	cmd := buildSystemdRunCmd(ctx, osUser, "rm", "-rf", filepath.Join(installPath, "install"))
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("rm install/: %w (output: %s)", err, truncateStr(string(out), 256))
	}
	return nil
}

func prestashopInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req prestashopInstallReq
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
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required"}
	}
	if len(req.AdminPass) < 8 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_pass must be at least 8 characters (PrestaShop minimum)",
		}
	}
	if req.Country != "" && !prestashopCountryPattern.MatchString(req.Country) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("country %q must be a 2-letter ISO 3166-1 alpha-2 code", req.Country),
		}
	}
	if req.Language != "" && !prestashopLanguagePattern.MatchString(req.Language) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("language %q does not match expected ISO-639 form", req.Language),
		}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computePrestaShopInstallPath(req.Docroot, req.Subdirectory)

	if req.Subdirectory != "" {
		mkdirCmd := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath)
		if out, err := runBoundedOutput(mkdirCmd, 0); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v (output: %s)", installPath, err, truncateStr(string(out), 256))}
		}
	}

	removePlaceholderIndex(ctx, installPath)

	tmpDir, err := stagingMkdirTemp("prestashop-")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("staging mktemp: %v", err)}
	}
	defer os.RemoveAll(tmpDir)
	zipPath := filepath.Join(tmpDir, "prestashop_outer.zip")

	dlCtx, dlCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer dlCancel()
	if err := downloadPrestaShopZip(dlCtx, zipPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := verifyPrestaShopSHA256(zipPath); err != nil {
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

	if err := extractPrestaShopZip(ctx, req.OSUser, zipPath, installPath, stagingDir); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// PrestaShop's installer wants 15-min budget on slower hosts —
	// the catalog seeding + sample image import is the long pole.
	installCtx, installCancel := context.WithTimeout(ctx, 15*time.Minute)
	defer installCancel()
	if err := runPrestaShopCLIInstaller(installCtx, req, installPath); err != nil {
		_ = execCommandContext(ctx, "rm", "-rf", filepath.Join(installPath, "app", "config", "parameters.php")).Run()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := removePrestaShopInstallDir(ctx, req.OSUser, installPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	return prestashopInstallResp{Version: prestashopVersion}, nil
}

func init() {
	RegisterAppInstaller("prestashop", prestashopInstallHandler)
}
