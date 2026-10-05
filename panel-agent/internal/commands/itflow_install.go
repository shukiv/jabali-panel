package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// itflow_install.go — ITFlow (MSP ERP) one-click installer (GH #206).
//
// ITFlow is distributed via git (no release tarball; it self-updates from
// the admin UI by `git pull`), so this is the first git-clone app. The
// upstream headless flow (confirmed with the ITFlow maintainer on #206):
//
//   1. git clone master into the docroot (as the site user, so the .git
//      tree the in-app updater pulls is user-owned).
//   2. php scripts/setup_cli.php --non-interactive ... — writes config.php,
//      IMPORTS db.sql itself, and creates the first admin. (We do NOT
//      pre-import db.sql; the script does it.)
//
// ITFlow already defaults CONST_GET_IP_METHOD to REMOTE_ADDR, which is the
// correct source under jabali's nginx->FPM/FastCGI (no reverse-proxy XFF
// hop), so we no longer write that define (GH #226).
//
// The single cron ITFlow needs (GH #928) — cron.php run every minute as
// the dispatcher — is created panel-side after this returns ready (see
// api.createITFlowInstallAndKickAgent). It's just `php <docroot>/cron/cron.php`,
// which the jabali cron allowlist accepts.

type itflowInstallReq struct {
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
	CompanyName  string `json:"company_name"`
	AdminName    string `json:"admin_name"`
	AdminEmail   string `json:"admin_email"`
	AdminPass    string `json:"admin_pass"`
	RepoBranch   string `json:"repo_branch"` // "master" (default) or "develop"
}

type itflowInstallResp struct {
	Version string `json:"version"`
}

// itflowRepoURL is the upstream git repo. Branch master per the
// maintainer (#206) — ITFlow self-updates along master via git pull from
// its admin UI.
const itflowRepoURL = "https://github.com/itflow-org/itflow.git"

// itflowPinnedCommit is the reviewed master commit the installer checks out
// (GH #455). ITFlow self-updates along master via its admin UI, so we clone
// master (the in-app updater needs that .git) but reset the working tree to
// this reviewed SHA at install time and verify HEAD matches — installs are
// reproducible and never pull an unreviewed master tip. Upstream now tags
// its monthly releases (v26.10, ...), but a tag can move, so we still pin
// the SHA. Bump deliberately (code review) when adopting a newer ITFlow.
// v26.10 (GH #1973): develop->master release merge (upstream PR #1303).
// Since the 2026-09-04 pin (GH #1461): 39 commits, mostly new API v1
// endpoints (archive/create/update for domains, networks, vendors, ...),
// each behind validate_api_key.php. Focused-review at bump time:
// setup_cli.php unchanged (our headless-setup contract holds); db.sql adds
// one column (recurring_invoice_auto_send); config.php write unchanged;
// getIP() unchanged, still REMOTE_ADDR (GH #226 — no XFF/proxy-header
// trust under our nginx->FPM); no exec/shell_exec/proc_open/eval/
// unserialize added outside the bundled libs. Domain expiry stays exec-free
// (RDAP + native DNS); the in-app updater still needs exec and degrades
// gracefully without it.
const itflowMasterPinnedCommit = "c05ccb738a5628519dcf8124391c9944aa09b891"

// itflowDevelopPinnedCommit pins the `develop` branch (GH #332). develop is
// ITFlow's active dev branch  bleeding-edge and NOT security-reviewed; we pin
// it (like master) so installs are reproducible and never drift to a raw tip,
// but the UI labels it unreviewed. Bump deliberately when adopting newer dev.
// Bumped alongside the v26.10 master release (GH #1973) to the develop
// commit that release merged, whose tree is identical to v26.10.
const itflowDevelopPinnedCommit = "ab2361b294be618eace4e1fa964d856af784a8b8"

// itflowResolveBranch maps the requested branch to (branch, pinnedCommit),
// defaulting to master. Any unknown value falls back to master (fail-safe).
func itflowResolveBranch(req string) (branch, pin string) {
	if req == "develop" {
		return "develop", itflowDevelopPinnedCommit
	}
	return "master", itflowMasterPinnedCommit
}

// itflowSafeText guards the free-text fields that setup_cli.php
// interpolates UNESCAPED into SQL INSERTs (company_name, admin_name).
// Reject quotes, backslash, and control chars so a crafted value can't
// break the install or inject SQL. (Same reflex as the DokuWiki
// admin_email guard.)
var itflowSafeText = regexp.MustCompile(`^[^'"\\\x00-\x1f]{1,120}$`)

func computeITFlowInstallPath(docroot, subdirectory string) string {
	if subdirectory == "" {
		return docroot
	}
	return filepath.Join(docroot, subdirectory)
}

// cloneITFlow clones master into installPath as the site user. git clone
// needs an empty target, so clone into a staging dir then cp -a the whole
// tree (INCLUDING .git — the in-app updater needs it) into installPath.
func cloneITFlow(ctx context.Context, osUser, installPath, branch, pin string) error {
	stagingDir, err := stagingMkdirTemp("itflow-clone-")
	if err != nil {
		return fmt.Errorf("staging mktemp: %w", err)
	}
	defer os.RemoveAll(stagingDir)
	src := filepath.Join(stagingDir, "itflow")
	// chown staging to the user so the as-user clone can write there.
	if err := execCommandContext(ctx, "chown", "-R", osUser+":"+osUser, stagingDir).Run(); err != nil {
		return fmt.Errorf("chown staging: %w", err)
	}
	// Full clone (no --depth) so the reviewed pinned commit is reachable even
	// after master advances upstream; master ref is kept for the in-app updater.
	cloneCmd := buildSystemdRunCmd(ctx, osUser,
		"git", "clone", "--branch", branch, itflowRepoURL, src,
	)
	if out, err := runBoundedOutput(cloneCmd, 0); err != nil {
		return fmt.Errorf("git clone: %w (output: %s)", err, truncateStr(string(out), 512))
	}
	// Pin the working tree (and master) to the reviewed commit (GH #455).
	resetCmd := buildSystemdRunCmd(ctx, osUser,
		"git", "-C", src, "reset", "--hard", pin,
	)
	if out, err := runBoundedOutput(resetCmd, 0); err != nil {
		return fmt.Errorf("git reset to pinned commit %s: %w (output: %s)", pin, err, truncateStr(string(out), 512))
	}
	// Verify HEAD is exactly the pin — fail closed on any mismatch.
	headCmd := buildSystemdRunCmd(ctx, osUser, "git", "-C", src, "rev-parse", "HEAD")
	headOut, err := runBoundedOutput(headCmd, 0)
	if err != nil {
		return fmt.Errorf("git rev-parse HEAD: %w (output: %s)", err, truncateStr(string(headOut), 256))
	}
	if got := strings.TrimSpace(string(headOut)); got != pin {
		return fmt.Errorf("itflow pin mismatch: HEAD=%s want=%s — refusing to install unreviewed code", got, pin)
	}
	cpCmd := buildSystemdRunCmd(ctx, osUser, "sh", "-c",
		fmt.Sprintf("cp -a %s/. %s/", shellQuote(src), shellQuote(installPath)),
	)
	if out, err := runBoundedOutput(cpCmd, 0); err != nil {
		return fmt.Errorf("copy itflow tree: %w (output: %s)", err, truncateStr(string(out), 512))
	}
	return nil
}

// runITFlowSetupCLI drives scripts/setup_cli.php headlessly. The script
// chdir()s to its own dir and resolves ../config.php + ../db.sql, so cwd
// only has to be installPath. Secrets land on /proc/<pid>/cmdline for the
// install window — same exposure as wp-cli / drush, acceptable on a
// single-tenant slice.
func runITFlowSetupCLI(ctx context.Context, req itflowInstallReq, installPath, baseURL string) error {
	branch, _ := itflowResolveBranch(req.RepoBranch)
	dbHost := req.DBHost
	if dbHost == "" {
		dbHost = "localhost"
	}
	args := []string{
		"--working-directory=" + installPath,
		phpCLIFor(req.OSUser),
		"scripts/setup_cli.php",
		"--host=" + dbHost,
		"--username=" + req.DBUser,
		"--password=" + req.DBPassword,
		"--database=" + req.DBName,
		"--base-url=" + baseURL,
		"--locale=en_US",
		"--timezone=UTC",
		"--currency=USD",
		"--company-name=" + req.CompanyName,
		"--country=United States",
		"--user-name=" + req.AdminName,
		"--user-email=" + req.AdminEmail,
		"--user-password=" + req.AdminPass,
		"--repo-branch=" + branch,
		"--non-interactive",
	}
	cmd := buildSystemdRunCmd(ctx, req.OSUser, args...)
	out, err := runBoundedOutput(cmd, 0)
	if err != nil {
		return fmt.Errorf("setup_cli.php: %w (output: %s)", err, truncateStr(string(out), 1024))
	}
	// setup_cli.php prints errors + exit 0 in some failure modes; scan
	// for its DB-import / connection failure markers.
	o := string(out)
	if strings.Contains(o, "Database Connection Failed") ||
		strings.Contains(o, "Database connection failed") ||
		strings.Contains(o, "db.sql file not found") ||
		strings.Contains(o, "Error performing query") {
		return fmt.Errorf("setup_cli.php reported failure: %s", truncateStr(o, 512))
	}
	return nil
}

func itflowInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req itflowInstallReq
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
	if !itflowSafeText.MatchString(req.CompanyName) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "company_name must be 1-120 chars with no quotes, backslash, or control characters"}
	}
	if !itflowSafeText.MatchString(req.AdminName) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_name must be 1-120 chars with no quotes, backslash, or control characters"}
	}
	if req.AdminEmail == "" || strings.ContainsAny(req.AdminEmail, "'\"\\\r\n ") || !strings.Contains(req.AdminEmail, "@") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_email is required and must be a plain email address"}
	}
	if len(req.AdminPass) < 8 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "admin_pass must be at least 8 characters (ITFlow minimum)"}
	}
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	installPath := computeITFlowInstallPath(req.Docroot, req.Subdirectory)
	subdir := strings.Trim(req.Subdirectory, "/")

	if subdir != "" {
		mkdirCmd := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath)
		if out, err := runBoundedOutput(mkdirCmd, 0); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir %s: %v (output: %s)", installPath, err, truncateStr(string(out), 256))}
		}
	}
	removePlaceholderIndex(ctx, installPath)

	cloneCtx, cloneCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cloneCancel()
	branch, pin := itflowResolveBranch(req.RepoBranch)
	if err := cloneITFlow(cloneCtx, req.OSUser, installPath, branch, pin); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// base-url is the public host (+ optional subdir), NO protocol —
	// setup_cli wants "example.com" or "example.com/itflow".
	baseURL := strings.TrimPrefix(strings.TrimPrefix(req.SiteURL, "https://"), "http://")
	baseURL = strings.TrimRight(baseURL, "/")

	if err := runITFlowSetupCLI(ctx, req, installPath, baseURL); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// Subdir installs need the pretty-URL rewrite (generic vhost targets
	// /index.php at the docroot). No-op for docroot installs.
	if subdir != "" {
		if domain, derr := DomainFromSiteURL(req.SiteURL); derr == nil {
			if err := writeAppRewrite(ctx, "itflow", domain, req.OSUser, subdir); err != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write nginx rewrite: %v", err)}
			}
		}
	}

	return itflowInstallResp{Version: branch}, nil
}

func init() {
	RegisterAppInstaller("itflow", itflowInstallHandler)
}
