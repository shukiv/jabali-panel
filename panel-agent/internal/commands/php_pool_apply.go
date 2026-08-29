package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"golang.org/x/sys/unix"
)

// phpPoolApplyParams is the input shape for php.pool.apply.
type phpPoolApplyParams struct {
	Username   string `json:"username"`
	PHPVersion string `json:"php_version"`
	Additive   bool   `json:"additive,omitempty"` // M35.8: keep other-version pools for this user
	// Slug is the pool/instance identity used for all on-disk paths and the
	// systemd instance name (GH #329). Empty => the legacy per-user default
	// pool: slug == username, byte-identical to pre-#329 behaviour. A
	// non-empty slug (e.g. "alice-php8.2") provisions an ADDITIONAL versioned
	// master alongside the default one; user=/group= in the pool conf stay the
	// real OS user (the jabali-pma opaque-instance pattern), so privilege is
	// unchanged — only the PHP version differs.
	Slug                           string `json:"slug,omitempty"`
	PmMode                         string `json:"pm_mode"`
	PmMaxChildren                  uint32 `json:"pm_max_children"`
	ProcessIdleTimeoutSeconds      uint32 `json:"process_idle_timeout_seconds"`
	PmStartServers                 uint32 `json:"pm_start_servers"`
	PmMinSpareServers              uint32 `json:"pm_min_spare_servers"`
	PmMaxSpareServers              uint32 `json:"pm_max_spare_servers"`
	PmMaxRequests                  uint32 `json:"pm_max_requests"`
	RequestTerminateTimeoutSeconds uint32 `json:"request_terminate_timeout_seconds"`
	// SlowlogTimeoutSeconds (GH #1332 item 12): >0 enables the pool slow log with
	// this request threshold. The slowlog PATH is derived here from the slug —
	// never accepted over the wire — and lands under the tenant-owned
	// /home/<user>/logs so the tenant-run FPM master can write it.
	SlowlogTimeoutSeconds uint32 `json:"slowlog_timeout_seconds,omitempty"`
	AdminValues           []KV   `json:"admin_values"`
	AdminFlags            []KV   `json:"admin_flags"`
	// DisableFunctions (GH #402) overrides the php_admin_value[disable_functions]
	// line. nil = caller did not specify -> the safe default (#401) is used;
	// "" = explicit admin opt-out (emit no line); any other value is rendered
	// verbatim. Admin-only channel: bypasses the tenant-facing
	// forbiddenDirectives guard (which still rejects disable_functions in
	// admin_values), so a tenant can never shorten their own blocklist.
	DisableFunctions *string `json:"disable_functions"`
}

// KV represents a key-value pair for ini directives.
type KV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// phpPoolApplyResponse is the output shape for php.pool.apply.
type phpPoolApplyResponse struct {
	SocketPath string `json:"socket_path"`
	PoolName   string `json:"pool_name"`
}

// phpPoolSpecTemplate represents the template data for rendering the pool config.
type phpPoolSpecTemplate struct {
	PoolName                       string
	User                           string
	Group                          string
	SocketPath                     string
	PmMode                         string
	PmMaxChildren                  uint32
	ProcessIdleTimeoutSeconds      uint32
	PmStartServers                 uint32
	PmMinSpareServers              uint32
	PmMaxSpareServers              uint32
	PmMaxRequests                  uint32
	RequestTerminateTimeoutSeconds uint32
	SlowlogTimeoutSeconds          uint32
	SlowlogPath                    string
	AdminValues                    []KV
	AdminFlags                     []KV
	DisableFunctions               string
}

// phpVersionRegex validates PHP version format: X.Y where X and Y are digits.
var phpVersionRegex = regexp.MustCompile(`^\d+\.\d+$`)

// phpPoolUsernameRegex validates PHP pool username format: must start with lowercase
// letter, contain only lowercase letters, digits, underscores, max 32 chars.
var phpPoolUsernameRegex = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// phpPoolSlugRegex validates a pool slug (GH #329). Superset of the username
// regex that also permits the "-php<X.Y>" versioned suffix (dot + hyphen).
// Panel constructs slugs as "<user>-php<version>"; the regex is defense in
// depth against a malformed slug ever reaching a filesystem path.
var phpPoolSlugRegex = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// adminValueAllowlist is the set of allowed php_admin_value directives.
var adminValueAllowlist = map[string]bool{
	"memory_limit":        true,
	"upload_max_filesize": true,
	"post_max_size":       true,
	"max_execution_time":  true,
	"max_input_vars":      true,
	"max_input_time":      true,
	"date.timezone":       true,
}

// adminFlagAllowlist is the set of allowed php_admin_flag directives.
var adminFlagAllowlist = map[string]bool{
	"display_errors": true,
	"log_errors":     true,
	"file_uploads":   true,
}

// defaultDisableFunctions is the GH #401 command-exec lockdown: the safe,
// non-overridable php_admin_value[disable_functions] applied to every pool
// unless an admin opts the owner's package out (GH #402, params.DisableFunctions
// == ""). Single source of truth — the template renders {{.DisableFunctions}},
// it is NOT hard-coded in the .tmpl anymore. Blocks process-spawning only;
// curl/file_get_contents stay enabled (WordPress HTTP API).
const defaultDisableFunctions = "exec,passthru,shell_exec,system,proc_open,popen,pcntl_exec,pcntl_fork,proc_nice,dl"

// forbiddenDirectives are directives that must never appear in overrides,
// even if they pass the allowlist check. Belt-and-suspenders defense.
var forbiddenDirectives = map[string]bool{
	"open_basedir":      true,
	"disable_functions": true,
	"extension_dir":     true,
	"zend_extension":    true,
}

// beltAndSuspendersCheck performs a final check on directive names to ensure
// no jailbreak-relevant directives slip through.
func isForbiddenDirective(name string) bool {
	if forbiddenDirectives[name] {
		return true
	}
	// Also reject any name containing a newline, regardless of allowlist.
	if strings.ContainsAny(name, "\n\r") {
		return true
	}
	return false
}

// globDeletePoolFiles removes pool files for the given username, optionally
// keeping the named version intact. Pass excludeVersion="" for the legacy
// wipe-all-versions behavior; pass a concrete version to leave that one
// in place — used by M35.8 per-domain PHP restore so multi-version pools
// for a single user coexist. Returns a map of PHP versions whose pool
// files were deleted (for subsequent reload).
func globDeletePoolFiles(username string, excludeVersion ...string) (map[string]bool, error) {
	pattern := fmt.Sprintf("/etc/php/*/fpm/pool.d/jabali-%s.conf", username)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("glob failed: %w", err)
	}
	keep := map[string]bool{}
	for _, v := range excludeVersion {
		if v != "" {
			keep[v] = true
		}
	}

	deletedVersions := make(map[string]bool)
	for _, path := range matches {
		// Extract version from /etc/php/<version>/fpm/pool.d/...
		parts := strings.Split(path, "/")
		var version string
		if len(parts) >= 3 && parts[1] == "etc" && parts[2] == "php" {
			version = parts[3]
		}
		if keep[version] {
			continue
		}
		if version != "" {
			deletedVersions[version] = true
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to remove %s: %w", path, err)
		}
	}
	return deletedVersions, nil
}

// restartOrReloadUserFPM handles per-user FPM service restart/reload.
// If oldVersion == newVersion (and not empty), attempts reload via USR2.
// If the reload fails (unit not loaded/inactive), falls back to restart.
// On version change or first-time apply, does full restart.
// Also enables the service for auto-start on boot.
func restartOrReloadUserFPM(ctx context.Context, username string, oldVersion, newVersion string) error {
	// Skip systemctl operations in test environments.
	if os.Getenv("JABALI_PHP_POOL_SKIP_RELOAD") != "" {
		return nil
	}

	serviceName := fmt.Sprintf("jabali-fpm@%s.service", username)

	// Try reload if versions match and oldVersion is not empty.
	if oldVersion == newVersion && oldVersion != "" {
		reloadCmd := execCommandContext(ctx, "systemctl", "reload", serviceName)
		if err := reloadCmd.Run(); err != nil {
			// Reload failed; check if unit is not loaded or inactive, then restart.
			// Otherwise return the error.
			isActiveCmd := execCommandContext(ctx, "systemctl", "is-active", serviceName)
			if err := isActiveCmd.Run(); err != nil {
				// Unit not loaded or inactive; fall through to restart.
			} else {
				// Unit is active but reload failed — this is an error.
				return fmt.Errorf("failed to reload %s: %w", serviceName, err)
			}
		} else {
			// Reload succeeded; enable and return.
			_ = execCommandContext(ctx, "systemctl", "enable", "--quiet", serviceName).Run()
			return nil
		}
	}

	// Restart (version changed or first-time apply).
	restartCmd := execCommandContext(ctx, "systemctl", "restart", serviceName)
	if err := restartCmd.Run(); err != nil {
		return fmt.Errorf("failed to restart %s: %w", serviceName, err)
	}

	// Enable the service for auto-start on boot.
	enableCmd := execCommandContext(ctx, "systemctl", "enable", "--quiet", serviceName)
	if err := enableCmd.Run(); err != nil {
		return fmt.Errorf("failed to enable %s: %w", serviceName, err)
	}

	return nil
}

// readVersionPinFile reads the version pin from disk, or returns empty string if not found.
func readVersionPinFile(username string) (string, error) {
	verPinRoot := os.Getenv("JABALI_PHP_VER_PIN_ROOT")
	if verPinRoot == "" {
		verPinRoot = "/etc/jabali-panel/user-phpver"
	}
	verPinPath := filepath.Join(verPinRoot, username)
	data, err := os.ReadFile(verPinPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read version pin: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// writeVersionPinFile writes the version pin to disk.
func writeVersionPinFile(username, version string) error {
	verPinRoot := os.Getenv("JABALI_PHP_VER_PIN_ROOT")
	if verPinRoot == "" {
		verPinRoot = "/etc/jabali-panel/user-phpver"
	}
	if err := os.MkdirAll(verPinRoot, 0755); err != nil {
		return fmt.Errorf("failed to create version pin dir: %w", err)
	}
	verPinPath := filepath.Join(verPinRoot, username)
	if err := os.WriteFile(verPinPath, []byte(version+"\n"), 0644); err != nil {
		return fmt.Errorf("failed to write version pin: %w", err)
	}
	return nil
}

// writePerUserFPMConfig writes the per-user FPM config that includes only this user's pool.
func writePerUserFPMConfig(username, version string) error {
	fpmConfRoot := os.Getenv("JABALI_FPM_CONFIG_ROOT")
	if fpmConfRoot == "" {
		fpmConfRoot = "/etc/jabali-panel/fpm"
	}
	if err := os.MkdirAll(fpmConfRoot, 0755); err != nil {
		return fmt.Errorf("failed to create fpm config dir: %w", err)
	}

	fpmConfPath := filepath.Join(fpmConfRoot, username+".conf")
	poolConfigPath := fmt.Sprintf("/etc/php/%s/fpm/pool.d/jabali-%s.conf", version, username)

	confContent := fmt.Sprintf(`[global]
pid = /run/php/jabali-%s/fpm.pid
error_log = /var/log/php-fpm-%s.log
daemonize = no

; Include only this user's pool file — prevents multi-master-loads-all-pools bug.
include=%s
`, username, username, poolConfigPath)

	if err := os.WriteFile(fpmConfPath, []byte(confContent), 0644); err != nil {
		return fmt.Errorf("failed to write per-user fpm config: %w", err)
	}
	return nil
}

// ensureVersionedFPMDropin writes the systemd drop-in for an additional
// versioned FPM master (GH #329) at
// /etc/systemd/system/jabali-fpm@<slug>.service.d/slice.conf. The content
// points the instance at the user's EXISTING slice and real OS user/group —
// identical to the default pool's drop-in (written by user.slice.ensure) but
// for the slug instance. Idempotent: only writes + daemon-reloads on change.
func ensureVersionedFPMDropin(ctx context.Context, slug, username string) error {
	dropinDir := filepath.Join(systemdRoot(), fmt.Sprintf("jabali-fpm@%s.service.d", slug))
	if err := os.MkdirAll(dropinDir, 0755); err != nil {
		return fmt.Errorf("mkdir drop-in dir: %w", err)
	}
	dropinPath := filepath.Join(dropinDir, "slice.conf")
	content := []byte(buildFPMDropinContent(username))
	if filesMatch(dropinPath, content) {
		return nil
	}
	if err := writeFileAtomically(dropinPath, content, 0644); err != nil {
		return fmt.Errorf("write drop-in: %w", err)
	}
	// A new instance drop-in requires a daemon-reload before the instance is
	// started so systemd picks up Slice=/User=/Group=.
	if os.Getenv("JABALI_PHP_POOL_SKIP_RELOAD") == "" {
		_ = execCommandContext(ctx, "systemctl", "daemon-reload").Run()
	}
	return nil
}

// ensureUserLogsDir makes /home/<user>/logs (0750, tenant-owned) if missing and
// returns it (GH #1332 item 12). The per-user FPM master runs AS the tenant, so
// its slow log must live somewhere the tenant can write; this also sets up the
// per-domain log shortcut. Chown is best-effort — the dir already being
// tenant-owned (the common case) is fine.
func ensureUserLogsDir(username string) (string, error) {
	dir := filepath.Join("/home", username, "logs")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	if u, err := user.Lookup(username); err == nil {
		uid, _ := strconv.Atoi(u.Uid)
		gid, _ := strconv.Atoi(u.Gid)
		_ = os.Chown(dir, uid, gid)
	}
	return dir, nil
}

// acquireLock acquires an exclusive flock on a per-user lock file with a 30-second timeout.
func acquireLock(username string) (*os.File, error) {
	lockDir := "/run/jabali"
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create lock dir: %w", err)
	}

	lockPath := filepath.Join(lockDir, fmt.Sprintf("pool-apply-%s.lock", username))
	file, err := os.Create(lockPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create lock file: %w", err)
	}

	// Attempt to acquire exclusive lock with 30-second timeout.
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- unix.Flock(int(file.Fd()), unix.LOCK_EX)
	}()

	select {
	case err := <-doneCh:
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("failed to acquire flock: %w", err)
		}
		return file, nil
	case <-time.After(30 * time.Second):
		file.Close()
		return nil, fmt.Errorf("lock acquisition timeout (30s) — stuck apply?")
	}
}

func phpPoolApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p phpPoolApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate username.
	if !phpPoolUsernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid username format",
		}
	}

	// Validate php_version format and check directory exists.
	if !phpVersionRegex.MatchString(p.PHPVersion) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid php_version format (expected X.Y)",
		}
	}

	// Resolve the pool slug (GH #329). Empty => legacy per-user default pool
	// (slug == username), byte-identical to pre-#329. A non-empty slug
	// provisions an additional versioned master. Validate as a safe path
	// component before any filesystem work.
	slug := p.Username
	if p.Slug != "" {
		slug = p.Slug
	}
	if !phpPoolSlugRegex.MatchString(slug) || strings.Contains(slug, "..") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid slug format",
		}
	}
	isVersioned := slug != p.Username

	poolDir := fmt.Sprintf("/etc/php/%s/fpm/pool.d/", p.PHPVersion)
	if info, err := os.Stat(poolDir); err != nil || !info.IsDir() {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("php version %s not installed", p.PHPVersion),
		}
	}

	// Validate pm_mode.
	pmModes := map[string]bool{"static": true, "ondemand": true, "dynamic": true}
	if !pmModes[p.PmMode] {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid pm_mode (must be static, ondemand, or dynamic)",
		}
	}

	// Validate pm_max_children.
	if p.PmMaxChildren == 0 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "pm_max_children must be > 0",
		}
	}

	// Validate process_idle_timeout_seconds.
	if p.ProcessIdleTimeoutSeconds == 0 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "process_idle_timeout_seconds must be > 0",
		}
	}

	// GH #339: dynamic pm requires start/spare sizing and FPM enforces
	// min_spare <= start <= max_spare <= max_children. Fail closed so a bad
	// combo never reaches FPM (which would refuse to start the pool).
	if p.PmMode == "dynamic" {
		if p.PmStartServers == 0 || p.PmMinSpareServers == 0 || p.PmMaxSpareServers == 0 {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: "dynamic pm requires pm_start_servers, pm_min_spare_servers, pm_max_spare_servers > 0",
			}
		}
		if !(p.PmMinSpareServers <= p.PmStartServers &&
			p.PmStartServers <= p.PmMaxSpareServers &&
			p.PmMaxSpareServers <= p.PmMaxChildren) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: "dynamic pm requires pm_min_spare_servers <= pm_start_servers <= pm_max_spare_servers <= pm_max_children",
			}
		}
	}

	// Validate admin_values directives.
	for _, av := range p.AdminValues {
		if isForbiddenDirective(av.Name) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("forbidden directive: %s", av.Name),
			}
		}
		if !adminValueAllowlist[av.Name] {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("unknown admin_value directive: %s", av.Name),
			}
		}
	}

	// Validate admin_flags directives and values.
	for _, af := range p.AdminFlags {
		if isForbiddenDirective(af.Name) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("forbidden directive: %s", af.Name),
			}
		}
		if !adminFlagAllowlist[af.Name] {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("unknown admin_flag directive: %s", af.Name),
			}
		}
		if af.Value != "on" && af.Value != "off" {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("admin_flag value must be 'on' or 'off', got: %s", af.Value),
			}
		}
	}

	// The config dir can exist without the FPM binary (a partial install, or a
	// version whose CLI-only packages landed). fpm-exec would then crash-loop
	// the master with a bare exit 127. Fail fast with a clear message so the
	// pool lands in "error" with an actionable reason instead (GH #329). Checked
	// after argument validation so a bad request still returns InvalidArgument.
	fpmBinary := fmt.Sprintf("/usr/sbin/php-fpm%s", p.PHPVersion)
	if _, err := os.Stat(fpmBinary); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("php-fpm binary for %s not installed (%s missing) — install php%s-fpm", p.PHPVersion, fpmBinary, p.PHPVersion),
		}
	}

	// Acquire per-slug flock to serialize pool-apply operations. Keying on the
	// slug (not the user) lets the default pool and a versioned pool for the
	// same user apply independently without blocking each other.
	lockFile, err := acquireLock(slug)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("flock acquisition failed: %v", err),
		}
	}
	defer lockFile.Close()

	// Read old version before making changes.
	oldVersion, err := readVersionPinFile(slug)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to read old version: %v", err),
		}
	}

	// Delete stale pool files for this user. Legacy callers (operator
	// switches primary PHP version via the panel) wipe ALL (user, *)
	// pool files so only one survives. M35.8 migration restore needs
	// per-domain PHP — multiple versions co-exist; only the version
	// being upserted should be deleted-and-rewritten. The `additive`
	// flag toggles between the two: additive=true leaves other-version
	// pools intact; additive=false (legacy default) wipes everything.
	var keep []string
	if p.Additive || isVersioned {
		keep = append(keep, p.PHPVersion)
	}
	// A versioned slug's glob (jabali-<user>-php<ver>.conf) only ever matches
	// its own single version dir, so this never touches the default pool or
	// any sibling version — it just rewrites this slug's own file.
	_, err = globDeletePoolFiles(slug, keep...)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to clean stale pools: %v", err),
		}
	}

	// Build pool config path and socket path.
	// Support JABALI_PHP_POOL_CONFIG_DIR env var for testing.
	poolConfigDir := os.Getenv("JABALI_PHP_POOL_CONFIG_DIR")
	if poolConfigDir == "" {
		poolConfigDir = fmt.Sprintf("/etc/php/%s/fpm/pool.d", p.PHPVersion)
	}
	poolConfigPath := fmt.Sprintf("%s/jabali-%s.conf", poolConfigDir, slug)
	// Socket lives in a slug-owned subdir of /run/php (see fpm-pre-start).
	// For the default pool (slug == user) the path is the legacy
	// /run/php/jabali-<user>/fpm.sock, version-independent so its vhosts
	// survive a version switch without regen. A versioned slug gets its own
	// /run/php/jabali-<user>-php<ver>/fpm.sock (GH #329).
	socketPath := fmt.Sprintf("/run/php/jabali-%s/fpm.sock", slug)
	poolName := fmt.Sprintf("jabali-%s", slug)

	// Render the template.
	// Support JABALI_PHP_POOL_TEMPLATE_PATH env var for testing.
	tmplPath := os.Getenv("JABALI_PHP_POOL_TEMPLATE_PATH")
	if tmplPath == "" {
		tmplPath = "/etc/jabali-panel/php-pool.conf.tmpl"
	}
	tmplData, err := os.ReadFile(tmplPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to read pool template: %v", err),
		}
	}

	tmpl, err := template.New("pool").Parse(string(tmplData))
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to parse pool template: %v", err),
		}
	}

	// Resolve disable_functions: nil param -> the #401 safe default; a
	// non-nil value (incl. "") is the admin per-package decision (#402).
	// Reject control chars so a value can never break out of the pool-conf
	// line (defense-in-depth; the value comes from panel-api, not a tenant).
	disableFunctions := defaultDisableFunctions
	if p.DisableFunctions != nil {
		disableFunctions = *p.DisableFunctions
	}
	if strings.ContainsAny(disableFunctions, "\n\r\x00") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "disable_functions contains control characters",
		}
	}

	// GH #1332 item 12: derive the slow-log path from the slug (never over the
	// wire) and ensure the tenant-owned logs dir exists so the tenant-run FPM
	// master can write it. Clamp defensively — the panel validates 0..600.
	var slowlogPath string
	slowlogTimeout := p.SlowlogTimeoutSeconds
	if slowlogTimeout > 600 {
		slowlogTimeout = 600
	}
	if slowlogTimeout > 0 {
		name := "php-slow.log"
		if isVersioned {
			name = "php-slow-" + slug + ".log"
		}
		logsDir, lerr := ensureUserLogsDir(p.Username)
		if lerr != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("ensure logs dir: %v", lerr),
			}
		}
		slowlogPath = filepath.Join(logsDir, name)
	}

	spec := phpPoolSpecTemplate{
		PoolName:                       poolName,
		User:                           p.Username,
		Group:                          p.Username,
		SocketPath:                     socketPath,
		PmMode:                         p.PmMode,
		PmMaxChildren:                  p.PmMaxChildren,
		ProcessIdleTimeoutSeconds:      p.ProcessIdleTimeoutSeconds,
		PmStartServers:                 p.PmStartServers,
		PmMinSpareServers:              p.PmMinSpareServers,
		PmMaxSpareServers:              p.PmMaxSpareServers,
		PmMaxRequests:                  p.PmMaxRequests,
		RequestTerminateTimeoutSeconds: p.RequestTerminateTimeoutSeconds,
		SlowlogTimeoutSeconds:          slowlogTimeout,
		SlowlogPath:                    slowlogPath,
		AdminValues:                    p.AdminValues,
		AdminFlags:                     p.AdminFlags,
		DisableFunctions:               disableFunctions,
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, spec); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to render pool template: %v", err),
		}
	}

	// Write the pool config file.
	if err := os.WriteFile(poolConfigPath, []byte(buf.String()), 0644); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to write pool config: %v", err),
		}
	}

	// Write the per-user FPM config (includes only this user's pool).
	if err := writePerUserFPMConfig(slug, p.PHPVersion); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to write per-user fpm config: %v", err),
		}
	}

	// Write the version pin file (keyed by slug).
	if err := writeVersionPinFile(slug, p.PHPVersion); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to write version pin: %v", err),
		}
	}

	// A versioned slug is an ADDITIONAL master; it needs its own systemd
	// slice drop-in (jabali-fpm@<slug>.service.d/slice.conf) pointing at the
	// SAME per-user slice + real OS user/group. The default pool's drop-in is
	// owned by user.slice.ensure, so only do this for versioned slugs (#329).
	if isVersioned {
		if err := ensureVersionedFPMDropin(ctx, slug, p.Username); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to write versioned fpm drop-in: %v", err),
			}
		}
	}

	// Per-user CLI php wrapper so the user's shell, Composer, wp-cli, and cron
	// resolve `php` to their pinned version (GH #184). This is the user's SHELL
	// php and belongs to the DEFAULT pool only — a per-domain versioned pool
	// must not repoint the shell. Best-effort: a failure here must not fail the
	// FPM apply (the web path is already converged).
	if !isVersioned {
		if err := ensureUserCLIPHP(p.Username, p.PHPVersion); err != nil {
			slog.Warn("per-user CLI php wrapper", "user", p.Username, "version", p.PHPVersion, "err", err)
		}
	}

	// Restart or reload the slug's FPM service.
	if err := restartOrReloadUserFPM(ctx, slug, oldVersion, p.PHPVersion); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: err.Error(),
		}
	}

	return phpPoolApplyResponse{
		SocketPath: socketPath,
		PoolName:   poolName,
	}, nil
}

func init() {
	Default.Register("php.pool.apply", phpPoolApplyHandler)
}
