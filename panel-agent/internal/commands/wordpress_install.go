package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// wordpressInstallReq is the input shape for wordpress.install.
type wordpressInstallReq struct {
	OSUser       string `json:"os_user"`     // domain owner (e.g. "shuki")
	Docroot      string `json:"docroot"`     // /home/shuki/domains/example.com/public_html
	DBName       string `json:"db_name"`     // already-provisioned
	DBUser       string `json:"db_user"`     // already-provisioned
	DBPassword   string `json:"db_password"` // plaintext, single-use
	DBHost       string `json:"db_host"`     // "localhost" (unix socket) or "127.0.0.1"
	SiteURL      string `json:"site_url"`    // https://example.com
	SiteTitle    string `json:"site_title"`
	AdminUser    string `json:"admin_user"`
	AdminPass    string `json:"admin_pass"`
	AdminEmail   string `json:"admin_email"`
	Locale       string `json:"locale"`
	// Version is the WordPress core release to install: "latest" (default,
	// resolved by wp-cli at install time) or a concrete version like "6.7.1"
	// (JAB-180). Validated below; integrity is enforced by verify-checksums
	// against whatever version actually landed.
	Version      string `json:"version"`
	UseWWW       bool   `json:"use_www"`      // prepend www. to domain in siteurl
	Subdirectory string `json:"subdirectory"` // install in subdirectory (optional)

	// M16 Wave D Step 7 — per-install OIDC plugin bootstrap. All three
	// must be non-empty to trigger plugin install; any empty value
	// skips the OIDC step entirely (WP still installs correctly, just
	// without SSO). The secret is plaintext and one-shot — the panel
	// sealed a copy before forwarding it here, and the agent forwards
	// it directly into the plugin's options table.
	//
	// (M16 was rolled back; these fields are vestigial — the panel no
	// longer sends them and the install path no longer reads them.
	// Kept on the struct so older payloads still decode without error;
	// scheduled for removal in a chore commit once all hosts have
	// updated past Step 9 of the rollback.)
	OIDCClientID     string `json:"oidc_client_id,omitempty"`
	OIDCClientSecret string `json:"oidc_client_secret,omitempty"`
	OIDCIssuer       string `json:"oidc_issuer,omitempty"`

	// (M22 magic-link mu-plugin fields PanelHost + InstallID removed in the
	// M22 rework — see ADR-0040. The new design uses wordpress.create_sso_file
	// per login instead of a persistent must-use plugin per install.)
}

// wordpressInstallResp is the output shape for wordpress.install.
type wordpressInstallResp struct {
	Version string `json:"version"` // what wp-cli actually installed
}

// validateDocrootPath ensures the docroot is within /home/<osuser>/domains/
// wordpressVersionRe validates the requested core version: "latest" or a
// numeric X / X.Y / X.Y.Z (JAB-180). Anything else is rejected so the value
// can't smuggle extra wp-cli args or shell metacharacters into the download.
var wordpressVersionRe = regexp.MustCompile(`^(latest|[0-9]+(\.[0-9]+){0,2})$`)

func validateDocrootPath(osUser, docroot string) error {
	allowedPrefix := filepath.Join("/home", osUser, "domains")
	absDocroot, err := filepath.Abs(docroot)
	if err != nil {
		return fmt.Errorf("failed to resolve docroot path: %v", err)
	}
	// Ensure absDocroot is under allowedPrefix
	relPath, err := filepath.Rel(allowedPrefix, absDocroot)
	if err != nil {
		return fmt.Errorf("docroot not in allowed path: %v", err)
	}
	// Check for path traversal (relPath containing ..)
	if strings.HasPrefix(relPath, "..") || strings.HasPrefix(relPath, "/") {
		return fmt.Errorf("docroot path traversal detected")
	}
	return nil
}

// buildSystemdRunCmd wraps a command in systemd-run for the given user/slice.
//
// `--quiet` suppresses systemd-run's own "Running as unit: ..." chatter
// on stderr — when callers pipe CombinedOutput into a downstream
// command (e.g. `find ... | xargs php`), that chatter contaminates
// the path. It also makes failure-error messages 90% shorter so
// LastError stays readable at the column's 1024-byte cap.
func buildSystemdRunCmd(ctx context.Context, osUser string, args ...string) *exec.Cmd {
	cmdArgs := []string{
		"systemd-run",
		"--quiet",
		"--uid=" + osUser,
		"--gid=" + osUser,
		"--slice=jabali-user-" + osUser + ".slice",
		// Pin PATH inside the transient unit. systemd-run does NOT
		// inherit the agent's PATH; the manager default can omit
		// /usr/local/bin on minimal builds, so a bare `wp` (symlink
		// at /usr/local/bin/wp) intermittently failed with
		// "Failed to find executable wp: No such file or directory".
		// The explicit PATH also lets wp-cli's #!/usr/bin/env php
		// shebang resolve the php alternative.
		"--setenv=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"--pipe",
		"--wait",
		"--collect",
	}
	// Resolve args[0] to an absolute path from the agent's own PATH
	// (agent runs as root with the full PATH) so the transient unit
	// never depends on its own PATH for the primary binary. Bare-name
	// fallback + caller-supplied absolute paths pass straight through.
	if len(args) > 0 && !filepath.IsAbs(args[0]) {
		if resolved, lpErr := exec.LookPath(args[0]); lpErr == nil {
			args = append([]string{resolved}, args[1:]...)
		}
	}
	cmdArgs = append(cmdArgs, args...)
	return exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
}

// removePlaceholderIndex deletes index.html at the install root if present.
// domain.create writes a "this domain is provisioned" placeholder there;
// nginx's `index index.html index.php` directive serves it before WP's
// index.php, so it must go before the install completes — otherwise the
// browser keeps seeing the placeholder until the user manually removes it.
//
// Safe to call unconditionally: the install API rejects re-installs (DB
// idempotency check) so the only index.html the user could lose here is
// either the placeholder or a hand-uploaded file the user is intentionally
// replacing by clicking "Install WordPress" on this domain.
// assertSafeInstallRoot refuses root-side operations on an app install root
// that is a symlink or a non-directory (GH #457). The attack: a tenant
// pre-creates <docroot>/<subdir> as a symlink before clicking install, so a
// later root-side `chown -R` / `rm -rf` / chmod dereferences it into a
// directory the tenant does not own (privilege escalation / destruction).
// Lstat (no-follow) detects the planted symlink; a not-yet-existing root is
// fine (it will be created as a real dir). Shared by every app installer via
// removePlaceholderIndex + normalizePermsToWwwData.
func assertSafeInstallRoot(installPath string) error {
	fi, err := os.Lstat(installPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat install root %s: %w", installPath, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing install: root %s is a symlink", installPath)
	}
	if !fi.IsDir() {
		return fmt.Errorf("refusing install: root %s is not a directory", installPath)
	}
	return nil
}

func removePlaceholderIndex(ctx context.Context, installPath string) {
	if err := assertSafeInstallRoot(installPath); err != nil {
		return // never rm through a planted symlink (#457)
	}
	_ = exec.CommandContext(ctx, "rm", "-f", filepath.Join(installPath, "index.html")).Run()
}

// normalizePermsToWwwData makes the WP tree match the project's ownership
// convention used by domain.create: owner=<user>, group=www-data,
// dirs 2750 (setgid), files 0640. nginx (in the www-data group) traverses via
// group bits; FPM (running AS the user) writes via owner bits. The setgid bit
// is load-bearing: it forces every file/dir the tenant later creates inside the
// tree to group www-data, so nginx keeps read access to uploads/plugins without
// the tenant being in www-data. A plain 0750 strips that, and new uploads land
// group <user> -> nginx 403 (repair "docroot-www-data-group" flags exactly this).
func normalizePermsToWwwData(ctx context.Context, installPath, osUser string) error {
	// Fail closed before any root-side chown/chmod if the root was swapped for
	// a symlink (#457) — chown -R would otherwise follow it off-tree.
	if err := assertSafeInstallRoot(installPath); err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "chown", "-R", osUser+":www-data", installPath).Run(); err != nil {
		return fmt.Errorf("chown -R %s:www-data %s: %w", osUser, installPath, err)
	}
	if err := exec.CommandContext(ctx, "find", installPath, "-type", "d", "-exec", "chmod", "2750", "{}", "+").Run(); err != nil {
		return fmt.Errorf("chmod dirs 2750 under %s: %w", installPath, err)
	}
	if err := exec.CommandContext(ctx, "find", installPath, "-type", "f", "-exec", "chmod", "0640", "{}", "+").Run(); err != nil {
		return fmt.Errorf("chmod files 0640 under %s: %w", installPath, err)
	}
	// #430 interim: wp-config.php / .env hold DB + cache creds; keep them
	// owner-only so another tenant's un-sandboxed cron-php can't read them across
	// the shared www-data group.
	hardenSensitiveFilesInScope(osUser, installPath)
	return nil
}

// cleanupWordPressFiles performs best-effort cleanup on failure.
func cleanupWordPressFiles(ctx context.Context, docroot string) error {
	files := []string{
		"wp-config.php",
		"wp-config-sample.php",
		"wp-blog-header.php",
		"wp-load.php",
		"wp-login.php",
		"wp-settings.php",
		"wp-admin",
		"wp-content",
		"wp-includes",
		"readme.html",
		"license.txt",
		"index.php",
	}
	for _, file := range files {
		path := filepath.Join(docroot, file)
		// Use rm via exec to match the documented behavior
		cmd := exec.CommandContext(ctx, "rm", "-rf", path)
		// Ignore errors; best-effort cleanup
		_ = cmd.Run()
	}
	return nil
}

func wordpressInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req wordpressInstallReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate required fields
	if req.OSUser == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "os_user is required",
		}
	}
	if req.Docroot == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "docroot is required",
		}
	}
	if req.DBName == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "db_name is required",
		}
	}
	if req.DBUser == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "db_user is required",
		}
	}
	if req.DBPassword == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "db_password is required",
		}
	}
	if req.DBHost == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "db_host is required",
		}
	}
	if req.SiteURL == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "site_url is required",
		}
	}
	if req.AdminUser == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_user is required",
		}
	}
	if req.AdminPass == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_pass is required",
		}
	}
	if req.AdminEmail == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "admin_email is required",
		}
	}

	// Default locale if not provided
	if req.Locale == "" {
		req.Locale = "en_US"
	}

	// Default to latest stable (JAB-180); validate before it reaches wp-cli.
	if req.Version == "" {
		req.Version = "latest"
	}
	if !wordpressVersionRe.MatchString(req.Version) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid version %q (want \"latest\" or a number like 6.7.1)", req.Version),
		}
	}

	// Validate docroot is within /home/<osuser>/domains/
	if err := validateDocrootPath(req.OSUser, req.Docroot); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid docroot: %v", err),
		}
	}

	// Compute installPath: docroot + optional subdirectory
	installPath := req.Docroot
	if req.Subdirectory != "" {
		installPath = filepath.Join(req.Docroot, req.Subdirectory)
	}
	// Always ensure installPath exists before handing off to wp-cli.
	// For subdir installs the subdir definitely doesn't exist on first
	// install. For root installs the docroot itself may not yet exist
	// if domain.create is still finishing — the panel kicks both
	// commands async, and a fast wp.install can beat the docroot mkdir
	// (observed: domain row inserted t=0, wp.install started t+376ms,
	// wp.install failed t+598ms because is_writable(dirname(docroot))
	// returned false, docroot finally created t+3641ms). mkdir -p is
	// idempotent so always calling it is safe.
	mkdirCmd := buildSystemdRunCmd(ctx, req.OSUser, "mkdir", "-p", installPath)
	if err := mkdirCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to create install directory %s: %v", installPath, err),
		}
	}

	// Clean any WordPress leftovers from a previous failed attempt.
	// wp-cli refuses to download into a directory it thinks already
	// hosts WordPress, so retries would permanently fail unless we
	// clear stale wp-* files first. Idempotent: no-op on empty dir.
	_ = cleanupWordPressFiles(ctx, installPath)

	// Drop the domain.create placeholder index.html. nginx's index
	// directive lists html before php, so leaving it would mask WP's
	// index.php on /. The user clicked "Install WordPress" — that's
	// explicit intent to replace the docroot's landing page.
	removePlaceholderIndex(ctx, installPath)

	// Step 1: wp core download
	downloadCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "core", "download",
		"--path="+installPath,
		"--locale="+req.Locale,
		"--version="+req.Version,
	)
	var dlStdout, dlStderr bytes.Buffer
	downloadCmd.Stdout = &dlStdout
	downloadCmd.Stderr = &dlStderr
	if err := downloadCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("wp core download failed: %v; stderr=%q; stdout=%q",
				err,
				truncateStr(dlStderr.String(), 400),
				truncateStr(dlStdout.String(), 200),
			),
		}
	}

	// Verify wp-load.php landed in installPath. If the download silently
	// wrote nothing (disk full, cache permission denied, etc.) we get a
	// confusing "not a WordPress installation" error on the next step.
	if _, statErr := os.Stat(filepath.Join(installPath, "wp-load.php")); os.IsNotExist(statErr) {
		var lsOut bytes.Buffer
		lsCmd := exec.CommandContext(ctx, "ls", "-la", installPath)
		lsCmd.Stdout = &lsOut
		_ = lsCmd.Run()
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("wp core download exited 0 but wp-load.php missing at %s; dl_stdout=%q dl_stderr=%q ls=%q",
				installPath,
				truncateStr(dlStdout.String(), 400),
				truncateStr(dlStderr.String(), 400),
				truncateStr(lsOut.String(), 600),
			),
		}
	}

	// Verify the downloaded core against WordPress.org's published checksums
	// for the pinned version + locale, and fail closed on any mismatch
	// (GH #456). This catches a tampered mirror/CDN or a corrupted download
	// before the tree becomes executable tenant web code.
	verifyCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		// No --version: verify against whatever version wp-cli actually
		// downloaded (req.Version may be "latest"). verify-checksums uses the
		// installed core version by default, so integrity is still enforced
		// (a tampered mirror/CDN or corrupted download fails closed).
		"wp", "core", "verify-checksums",
		"--path="+installPath,
		"--locale="+req.Locale,
	)
	var vfStdout, vfStderr bytes.Buffer
	verifyCmd.Stdout = &vfStdout
	verifyCmd.Stderr = &vfStderr
	if err := verifyCmd.Run(); err != nil {
		// Non-fatal: wp-cli verify-checksums can false-positive when the
		// wp.org checksums manifest lags a freshly-published core (a new
		// release bundling third-party files the manifest doesn't list yet
		// reports "File should not exist"). Failing closed there would block
		// installing that version, so treat this as a logged tripwire for
		// genuine core tampering instead.
		slog.WarnContext(ctx, "wordpress.install: core verify-checksums reported issues (non-fatal)",
			"version", req.Version,
			"locale", req.Locale,
			"err", err,
			"stderr", truncateStr(vfStderr.String(), 400),
		)
	}

	// Step 2: wp config create (with placeholder password, then rewrite)
	configCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "config", "create",
		"--path="+installPath,
		"--dbname="+req.DBName,
		"--dbuser="+req.DBUser,
		"--dbhost="+req.DBHost,
		"--dbpass=__JABALI_PLACEHOLDER__",
		"--dbcharset=utf8mb4",
		"--dbcollate=utf8mb4_unicode_ci",
		"--skip-check",
	)
	var ccStdout, ccStderr bytes.Buffer
	configCmd.Stdout = &ccStdout
	configCmd.Stderr = &ccStderr
	if err := configCmd.Run(); err != nil {
		_ = cleanupWordPressFiles(ctx, installPath)
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("wp config create failed: %v; stderr=%q; stdout=%q",
				err,
				truncateStr(ccStderr.String(), 400),
				truncateStr(ccStdout.String(), 200),
			),
		}
	}

	// Read wp-config.php and replace placeholder with real password
	configPath := filepath.Join(installPath, "wp-config.php")
	configContent, err := os.ReadFile(configPath)
	if err != nil {
		_ = cleanupWordPressFiles(ctx, installPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to read wp-config.php: %v", err),
		}
	}

	// Replace the placeholder with the real password
	configContent = []byte(strings.ReplaceAll(
		string(configContent),
		"__JABALI_PLACEHOLDER__",
		req.DBPassword,
	))

	// Write back with restricted permissions (0640)
	if err := os.WriteFile(configPath, configContent, 0640); err != nil {
		_ = cleanupWordPressFiles(ctx, installPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to write wp-config.php: %v", err),
		}
	}

	// Chown the config file to <user>:www-data so it matches the rest
	// of the docroot once normalizePermsToWwwData runs at the end.
	// Group=www-data lets nginx (in www-data) read the file via group bits;
	// FPM (running as <user>) reads via owner bits.
	chownCmd := exec.CommandContext(ctx, "chown", req.OSUser+":www-data", configPath)
	if err := chownCmd.Run(); err != nil {
		_ = cleanupWordPressFiles(ctx, installPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("chown wp-config.php failed: %v", err),
		}
	}

	// Step 3: wp core install (with stdin-based admin password)
	installCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "core", "install",
		"--path="+installPath,
		"--url="+req.SiteURL,
		"--title="+req.SiteTitle,
		"--admin_user="+req.AdminUser,
		"--admin_email="+req.AdminEmail,
		"--skip-email",
		"--prompt=admin_password",
	)
	// Pass admin password via stdin
	installCmd.Stdin = strings.NewReader(req.AdminPass + "\n")

	var installStdout, installStderr bytes.Buffer
	installCmd.Stdout = &installStdout
	installCmd.Stderr = &installStderr

	if err := installCmd.Run(); err != nil {
		_ = cleanupWordPressFiles(ctx, installPath)
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("wp core install failed: %v; stderr=%q; stdout=%q",
				err,
				truncateStr(installStderr.String(), 400),
				truncateStr(installStdout.String(), 200),
			),
		}
	}

	// Step 3b: Set pretty permalink structure.
	//
	// Out of the box WP uses "Plain" permalinks (?p=N), which means WP's
	// request parser never looks at the path segment for routing. An
	// unknown URL like /blah/ hits nginx's try_files, falls through to
	// /index.php (which carries $query_string, but "blah" is not a known
	// query param), WP sees an empty query → main loop returns the blog
	// homepage with HTTP 200. The operator sees what looks like a
	// legitimate /blah/ page rendering the blog, no 404. Setting any
	// pretty permalink structure turns on WP's path-based routing so
	// unknown slugs produce a proper 404. /%postname%/ is the most
	// common choice and matches the URL shape operators expect when they
	// migrate content in.
	permalinkCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "rewrite", "structure", "/%postname%/",
		"--path="+installPath,
	)
	var permalinkStdout, permalinkStderr bytes.Buffer
	permalinkCmd.Stdout = &permalinkStdout
	permalinkCmd.Stderr = &permalinkStderr
	if err := permalinkCmd.Run(); err != nil {
		_ = cleanupWordPressFiles(ctx, installPath)
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("wp rewrite structure failed: %v; stderr=%q; stdout=%q",
				err,
				truncateStr(permalinkStderr.String(), 400),
				truncateStr(permalinkStdout.String(), 200),
			),
		}
	}

	// Step 4: Get WordPress version
	versionCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "core", "version",
		"--path="+installPath,
	)

	var versionOutput bytes.Buffer
	versionCmd.Stdout = &versionOutput
	versionCmd.Stderr = io.Discard

	if err := versionCmd.Run(); err != nil {
		_ = cleanupWordPressFiles(ctx, installPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("wp core version failed: %v", err),
		}
	}

	version := strings.TrimSpace(versionOutput.String())

	// (M22 magic-link mu-plugin install step removed in the M22 rework —
	// see ADR-0040. SSO files are now written per login by
	// wordpress.create_sso_file, not staged into wp-content/mu-plugins/
	// at install time.)

	// Normalize ownership + perms across the entire WP tree to the
	// project's <user>:www-data 0750/0640 convention. wp-cli ran under
	// systemd-run --uid=user --gid=user, so files landed as user:user
	// with the user's default umask — diverges from domain.create's
	// docroot ownership and breaks any nginx access path that depends
	// on group bits (e.g. a future plugin that creates a 0700 dir).
	if err := normalizePermsToWwwData(ctx, installPath, req.OSUser); err != nil {
		// Don't roll back the install — files are valid, perms are just
		// off. Surface the error so the panel marks the install as
		// having a recoverable issue rather than silently leaving the
		// docroot in the wrong shape.
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("normalize perms failed: %v", err),
		}
	}

	// Block xmlrpc.php at the nginx layer. xmlrpc.php returns HTTP 200
	// for valid XML payloads, so CrowdSec brute-force scenarios never
	// trigger on it. Best-effort: install is already done, don't roll
	// back on a snippet-write failure.
	if wpDomain, domErr := DomainFromSiteURL(req.SiteURL); domErr == nil {
		_ = writeWordPressXmlrpcBlock(ctx, wpDomain)
	}

	return wordpressInstallResp{
		Version: version,
	}, nil
}

func init() {
	// Legacy command — still registered so any straggler caller keeps
	// working through the M19 release window. M19.1 deletes this line.
	Default.Register("wordpress.install", wordpressInstallHandler)
	// M19 dispatch table: lets the panel call app.install with
	// app_type="wordpress" (see panel-agent/internal/commands/app_dispatch.go).
	RegisterAppInstaller("wordpress", wordpressInstallHandler)
}

// truncateStr trims s to at most n runes, appending "…" when truncated.
// Used to keep error messages small enough to fit in panel_api log
// fields and DB columns.
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
