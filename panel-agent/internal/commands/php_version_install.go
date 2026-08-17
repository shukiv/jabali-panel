package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// phpVersionInstallParams is the input shape for php.version.install.
type phpVersionInstallParams struct {
	Version string `json:"version"`
}

// phpVersionInstallResponse is the output shape for php.version.install.
type phpVersionInstallResponse struct {
	Version    string `json:"version"`
	Installed  bool   `json:"installed"`
	FPMRunning bool   `json:"fpm_running"`
}

// versionRegex validates PHP version format: X.Y where X and Y are digits.
var versionRegex = regexp.MustCompile(`^\d+\.\d+$`)

// isVersionSupported checks if a version is in the supported list.
func isVersionSupported(version string) bool {
	for _, v := range SupportedPHPVersions {
		if v == version {
			return true
		}
	}
	return false
}

// isFPMAlreadyInstalledFunc is a function variable for testing.
var isFPMAlreadyInstalledFunc = func(version string) bool {
	// `command` is a shell BUILTIN, not a binary — execCommand("command",…)
	// always errored, so this used to report "not installed" for every
	// version. That made install never short-circuit on an already-installed
	// version and re-run the whole apt+pool+mask flow each time (GH #293).
	// LookPath is the correct "is phpX.Y on PATH" probe.
	_, err := exec.LookPath(fmt.Sprintf("php%s", version))
	return err == nil
}

// isFPMAlreadyInstalled checks if a PHP version is already installed.
func isFPMAlreadyInstalled(version string) bool {
	return isFPMAlreadyInstalledFunc(version)
}

// phpRequiredExts are the extension packages every supported one-click app
// (WordPress, Drupal, Joomla, MediaWiki) and most migrated sites need. mysql
// provides mysqli + pdo_mysql; a version installed without it 500s on every
// mysqli_connect() (GH #531). intl is required too: idn_to_ascii backs IDN
// handling in commercial panels (WiseCP) and Laravel/Symfony apps, and every
// sury build 7.4–8.5 ships php<v>-intl.
//
// Keep this list and phpOptionalExts in sync with install.sh's
// install_base_packages + provision_php_extensions — install.sh converges the
// fleet on `jabali update`; these lists close the gap for versions added via
// the panel between updates.
var phpRequiredExts = []string{"mysql", "mbstring", "zip", "gd", "curl", "xml", "intl"}

// phpOptionalExts are installed when the apt archive for the version has them,
// and skipped otherwise (packaging drifts between sury versions — 8.5 folds
// opcache into -common). redis + igbinary back WordPress object caching
// (GH #606); sqlite3 backs restored Nextcloud instances (JAB-39).
var phpOptionalExts = []string{"bcmath", "opcache", "redis", "igbinary", "sqlite3"}

// isPkgInstalled reports whether a dpkg package is installed and fully
// configured. Used to backfill only the missing required extensions instead of
// shelling out to apt when a version is already complete.
func isPkgInstalled(pkg string) bool {
	out, err := execCommand("dpkg-query", "-W", "-f=${Status}", pkg).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "install ok installed")
}

// ensureRequiredPHPExts installs any extension package (php<v>-mysql etc.)
// that is missing for an ALREADY-installed version. isFPMAlreadyInstalled
// only checks for php<v> on PATH, so a version pulled in as a dependency,
// apt-installed by hand without -mysql, or left partial by an interrupted
// install can satisfy that check yet still lack mysqli — and a migrated domain
// binding to its pool then 500s (GH #531). dpkg-checks each package first so a
// complete version is a no-op (no apt invocation).
//
// Required extensions failing to install fails the call; optional ones only
// log. When anything was installed, tenant FPM masters pinned to the version
// are reloaded so the running runtime actually gains the extension.
// Seams for ensureRequiredPHPExts tests — the function's batching and
// failure semantics matter (a flaky optional package must never fail the
// call) and the real implementations shell out to dpkg/apt/systemctl.
var (
	isPkgInstalledFunc        = isPkgInstalled
	probePackageFunc          = probePackage
	installPackagesFunc       = installPackages
	reloadVersionFPMUnitsFunc = reloadVersionFPMUnits
)

func ensureRequiredPHPExts(ctx context.Context, version string) error {
	missingPkgs := func(exts []string) []string {
		var missing []string
		for _, ext := range exts {
			pkg := fmt.Sprintf("php%s-%s", version, ext)
			if isPkgInstalledFunc(pkg) {
				continue
			}
			if !probePackageFunc(pkg) {
				continue // not in apt sources for this version — mirror base-install skip
			}
			missing = append(missing, pkg)
		}
		return missing
	}

	install := func(pkgs []string) error {
		done := make(chan error, 1)
		go func() { done <- installPackagesFunc(pkgs) }()
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout installing %v", pkgs)
		case err := <-done:
			return err
		}
	}

	required := missingPkgs(phpRequiredExts)
	optional := missingPkgs(phpOptionalExts)
	if len(required) == 0 && len(optional) == 0 {
		return nil
	}

	// Required and optional install separately so a flaky optional package
	// (redis, sqlite3, …) can never fail the call for a version whose required
	// runtime is intact.
	if len(required) > 0 {
		if err := install(required); err != nil {
			return err
		}
	}
	if len(optional) > 0 {
		if err := install(optional); err != nil {
			slog.Warn("optional PHP extensions failed to install", "version", version, "err", err)
			optional = nil
		}
	}

	// The dpkg postinst enables the new inis, but tenant masters already
	// running on this version keep the old runtime until reloaded. Best-effort:
	// a master that fails reload is a unit problem, not an extension problem.
	if len(required) > 0 || len(optional) > 0 {
		if _, failures := reloadVersionFPMUnitsFunc(ctx, version); len(failures) > 0 {
			slog.Warn("FPM reload after extension backfill failed for some units",
				"version", version, "failures", strings.Join(failures, "; "))
		}
	}
	return nil
}

// ensureRequiredPHPExtsFunc is a seam for tests.
var ensureRequiredPHPExtsFunc = ensureRequiredPHPExts

// probePackage checks if an apt package exists via apt-cache show.
func probePackage(pkg string) bool {
	cmd := execCommand("apt-cache", "show", pkg)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	return cmd.Run() == nil
}

// runAptUpdate runs apt-get update to refresh the package index.
func runAptUpdate() error {
	cmd := execCommand("apt-get", "update", "-qq")
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apt-get update failed: %w; output: %s", err, out.String())
	}
	return nil
}

// installPackages installs a list of apt packages with error handling.
func installPackages(pkgs []string) error {
	if len(pkgs) == 0 {
		return nil
	}

	cmd := execCommand("apt-get", append(
		[]string{"install", "-y", "--no-install-recommends"},
		pkgs...,
	)...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr // capture both stdout and stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apt-get install failed: %w; output: %s", err, stderr.String())
	}

	return nil
}

// disableDefaultPool moves the default www.conf pool to .disabled. The
// distro-shipped www.conf references /run/php/php<v>-fpm.sock which
// conflicts with our per-user UDS sockets; we never want it active.
func disableDefaultPool(version string) {
	poolFile := filepath.Join("/etc/php", version, "fpm/pool.d/www.conf")
	if _, err := os.Stat(poolFile); err == nil {
		_ = os.Rename(poolFile, poolFile+".disabled")
	}
}

// placeholderPoolContent is what install.sh writes — mirrored here so
// the panel-driven install path ends in the exact same on-disk state as
// a fresh install.sh run. An empty pool.d/ would make FPM fail with
// "no pool defined"; the placeholder gives the global unit something
// parseable even though we mask the unit afterwards.
const placeholderPoolContent = `; Placeholder pool installed by jabali-agent so php-fpm has at least one
; valid pool. No-op ondemand pool listening on an unused loopback socket.
; Per ADR-0025 the global php<v>-fpm.service is masked in favour of
; per-user jabali-fpm@<user>.service masters.

[_jabali_placeholder]
user = www-data
group = www-data
listen = /run/php/php-fpm-jabali-placeholder.sock
listen.owner = www-data
listen.group = www-data
listen.mode = 0600
pm = ondemand
pm.max_children = 1
pm.process_idle_timeout = 10s
`

// installPlaceholderPool writes the placeholder pool file if it doesn't
// already exist. Idempotent; safe to call on every install.
func installPlaceholderPool(version string) error {
	path := filepath.Join("/etc/php", version, "fpm/pool.d/_jabali-placeholder.conf")
	if _, err := os.Stat(path); err == nil {
		return nil // already in place
	}
	// #nosec G306 — 0644 matches what install.sh writes; pool files must be world-readable to php-fpm.
	return os.WriteFile(path, []byte(placeholderPoolContent), 0o644)
}

// preMaskFPMService creates the /etc/systemd/system/php<v>-fpm.service
// mask symlink BEFORE apt installs the package. Writing the mask first
// means the postinst's `systemctl start` is a no-op instead of a
// failure — which was wedging dpkg on hosts where the service couldn't
// start (e.g. stale pool files from a prior half-configured install).
// The systemd unit directory needs to exist; it always does on Debian.
func preMaskFPMService(version string) error {
	serviceName := fmt.Sprintf("php%s-fpm.service", version)
	maskPath := filepath.Join("/etc/systemd/system", serviceName)
	// Remove any existing symlink/file so we can write fresh. Ignore
	// errors — a missing file is fine, and if we can't remove it the
	// symlink call below will surface the real problem.
	_ = os.Remove(maskPath)
	if err := os.Symlink("/dev/null", maskPath); err != nil {
		return fmt.Errorf("create mask symlink %s: %w", maskPath, err)
	}
	// daemon-reload so systemd picks up the new mask before apt's
	// postinst invokes systemctl.
	cmd := execCommand("systemctl", "daemon-reload")
	_ = cmd.Run()
	return nil
}

// finalizeFPMMask runs after apt succeeds. reset-failed clears any
// residual failed state from a prior half-install, and a redundant
// `systemctl mask` call is a cheap idempotency check — if preMask
// failed for any reason, this catches it.
func finalizeFPMMask(version string) {
	serviceName := fmt.Sprintf("php%s-fpm.service", version)
	_ = execCommand("systemctl", "reset-failed", serviceName).Run()
	_ = execCommand("systemctl", "disable", "--quiet", serviceName).Run()
	_ = execCommand("systemctl", "mask", "--quiet", serviceName).Run()
}

func phpVersionInstallHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if params == nil || len(params) == 0 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "version parameter required",
		}
	}

	var p phpVersionInstallParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate version format
	if !versionRegex.MatchString(p.Version) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid version format: %q (expected X.Y)", p.Version),
		}
	}

	// Check if version is supported
	if !isVersionSupported(p.Version) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("unsupported version: %q", p.Version),
		}
	}

	// If already installed, return current status — but first backfill any
	// missing required extension. isFPMAlreadyInstalled only proves php<v> is
	// on PATH; the runtime can still be incomplete (no php<v>-mysql), which is
	// exactly how a migrated domain ends up 500ing on mysqli (GH #531).
	if isFPMAlreadyInstalled(p.Version) {
		if err := ensureRequiredPHPExtsFunc(ctx, p.Version); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("ensure required extensions for php%s: %v", p.Version, err),
			}
		}
		return phpVersionInstallResponse{
			Version:    p.Version,
			Installed:  isInstalledPHPVersion(p.Version),
			FPMRunning: checkFPMRunning(p.Version),
		}, nil
	}

	// Refresh apt index so probePackage sees newly-added repos (e.g. Sury).
	// A stale cache causes probePackage to report php8.4-* as missing even
	// when the Sury repo is configured, producing a misleading error before
	// installation even starts.
	if err := runAptUpdate(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("apt-get update: %v", err),
		}
	}

	// Build package list. phpRequiredExts are mandatory: every supported
	// one-click app (WordPress, Drupal, Joomla, MediaWiki) refuses to
	// install without them, and the operator sees an opaque "missing
	// MySQL extension" error months later instead of a clear failure
	// here. phpOptionalExts are probed per version (distros sometimes
	// bundle them into -common).
	required := []string{
		fmt.Sprintf("php%s-fpm", p.Version),
		fmt.Sprintf("php%s-cli", p.Version),
	}
	requiredExts := phpRequiredExts
	var missingRequired []string
	for _, ext := range requiredExts {
		pkg := fmt.Sprintf("php%s-%s", p.Version, ext)
		if probePackage(pkg) {
			required = append(required, pkg)
		} else {
			missingRequired = append(missingRequired, pkg)
		}
	}
	if len(missingRequired) > 0 {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf(
				"required PHP extensions missing from apt sources: %v — usually the Sury repo isn't indexed; run `apt-get update` and retry",
				missingRequired),
		}
	}

	var optional []string
	for _, ext := range phpOptionalExts {
		pkg := fmt.Sprintf("php%s-%s", p.Version, ext)
		if probePackage(pkg) {
			optional = append(optional, pkg)
		}
	}

	// Pre-mask the global php<v>-fpm.service BEFORE apt runs. The
	// distro postinst unconditionally `systemctl start`s the unit; if
	// it fails (stale pool files, binding conflict), dpkg marks the
	// package half-configured and subsequent apt transactions wedge on
	// it. Masking in advance turns the start into a no-op so apt
	// completes cleanly, and the mask is what we want anyway per
	// ADR-0025 (per-user jabali-fpm@<user>.service takes over).
	if err := preMaskFPMService(p.Version); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("pre-mask: %v", err),
		}
	}

	// Install all packages with context timeout
	pkgs := append(required, optional...)

	// Create a goroutine to install and signal completion or error
	done := make(chan error, 1)
	go func() {
		done <- installPackages(pkgs)
	}()

	// Wait for install to complete or context to cancel
	select {
	case <-ctx.Done():
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeDeadlineExceeded,
			Message: "installation timeout",
		}
	case err := <-done:
		if err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: err.Error(),
			}
		}
	}

	// Quiet the distro's default pool; install our placeholder so the
	// on-disk state matches a fresh install.sh run. Mask idempotently
	// in case pre-mask got rolled back by dpkg.
	disableDefaultPool(p.Version)
	if err := installPlaceholderPool(p.Version); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("install placeholder pool: %v", err),
		}
	}
	finalizeFPMMask(p.Version)

	// Small delay to let systemd settle after the mask/reset-failed
	// dance; callers query checkFPMRunning right after this and we
	// want a stable reading.
	time.Sleep(500 * time.Millisecond)

	// PHP Defense (Snuffleupagus) covers every installed minor.
	// install_snuffleupagus is idempotent + auto-detects on-disk
	// minors, so re-running it after a UI install adds the new minor
	// without touching minors already built. Best-effort: a build
	// failure (unlikely on a host that already shipped sury php8.5)
	// is logged but doesn't fail the install.
	go runSnuffleupagusBuild(p.Version)

	return phpVersionInstallResponse{
		Version:    p.Version,
		Installed:  isInstalledPHPVersion(p.Version),
		FPMRunning: checkFPMRunning(p.Version),
	}, nil
}

// runSnuffleupagusBuild reruns install.sh's install_snuffleupagus in a
// detached shell. The function auto-detects every phpX.Y-fpm on disk,
// so passing the just-installed version is unnecessary — but logging
// it makes the journal trail readable. Standalone goroutine so the UI
// install request returns immediately; the build can take 30-60s per
// minor to compile against PHP headers.
func runSnuffleupagusBuild(version string) {
	const installSh = "/opt/jabali-panel/install.sh"
	if _, err := os.Stat(installSh); err != nil {
		return
	}
	// install_php_cli_sendmail_path (JAB-230) rides along: the fresh minor's
	// cli/conf.d needs the sendmail_path dropin or cron mail() on that
	// version regresses to "sendmail not found". Both functions loop every
	// on-disk minor and are idempotent.
	cmd := execCommand("bash", "-c",
		"source "+installSh+" && install_snuffleupagus && install_php_cli_sendmail_path")
	cmd.Env = append(os.Environ(), "JABALI_PHP_DEFENSE_TRIGGER_VERSION="+version)
	// Detached: don't wait. Output goes to journalctl via stdout
	// inheriting from the agent's systemd-managed PID.
	_ = cmd.Run()
}

func init() {
	Default.Register("php.version.install", phpVersionInstallHandler)
}
