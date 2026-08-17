// wordpress_cache.go — GH #406. Enable/disable the jabali-cache plugin on a
// WordPress install, as the tenant, via wp-cli. The Redis object cache is gated
// by the per-tenant ACL user (ADR-0148): panel-api creates wp_<osuser> scoped to
// ~jc:<prefix>:* and passes the token here; the plugin connects with it.
//
// The plugin is installed from WordPress.org (the canonical source, GH #613)
// via wp-cli; a read-only bundled copy at bundledWPCachePluginDir (install.sh
// syncs it) is the fallback when WordPress.org is unreachable. Either way
// tenants never supply plugin code. Idempotent.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const bundledWPCachePluginDir = "/usr/local/share/jabali/wp-plugins/jabali-cache"

// redisClientsGroup gates /run/redis/redis.sock. Distinct from jabali-sockets
// (which also fronts the root agent socket) so tenants get Redis but nothing else.
const redisClientsGroup = "jabali-redis-clients"

type wordpressCacheSetParams struct {
	InstallPath   string `json:"install_path"`
	OSUser        string `json:"os_user"`
	Enable        bool   `json:"enable"`
	RedisSocket   string `json:"redis_socket"`
	RedisDB       int    `json:"redis_db"`
	Prefix        string `json:"prefix"`         // the jc:<...>: namespace (matches the ACL ~jc:<...>:*)
	RedisPassword string `json:"redis_password"` // the per-tenant ACL token
	// GH #612: behavioral settings, stamped as constants alongside the connection.
	MaxTTL    int  `json:"max_ttl"`    // JABALI_CACHE_MAXTTL (object-cache key TTL, 0 = LRU)
	PageCache bool `json:"page_cache"` // JABALI_CACHE_PAGE_CACHE (WP full-page cache)
	PageTTL   int  `json:"page_ttl"`   // JABALI_CACHE_PAGE_TTL (seconds)
	MaxMemMB  int  `json:"max_mem_mb"` // JABALI_CACHE_MAXMEMORY_MB (object-cache budget; 0 = unlimited)
}

type wordpressCacheSetResult struct {
	Ok      bool   `json:"ok"`
	Enabled bool   `json:"enabled"`
	Detail  string `json:"detail,omitempty"`
}

func wordpressCacheSetHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p wordpressCacheSetParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !strings.HasPrefix(p.InstallPath, "/home/") || strings.Contains(p.InstallPath, "..") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "install_path must be under /home/ with no .."}
	}
	if p.OSUser == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user required"}
	}

	if !p.Enable {
		// Disable: drop-ins out, plugin deactivated. Best-effort (a missing
		// plugin shouldn't fail the disable).
		_ = runWPAsTenant(ctx, p.OSUser, p.InstallPath, "jabali-cache", "disable")
		_ = runWPAsTenant(ctx, p.OSUser, p.InstallPath, "plugin", "deactivate", "jabali-cache")
		_ = setWPConfigCacheConstants(p.InstallPath, "", 0, "", "", "", false, 0, false, 0, 0) // strip the managed block
		return wordpressCacheSetResult{Ok: true, Enabled: false}, nil
	}

	// 1. Stage the plugin. WordPress.org is the canonical source (GH #613): the
	//    panel installs `jabali-cache` from the public plugin repository via
	//    wp-cli — the same channel `wp core download` already uses — so the
	//    published plugin, not a panel-bundled copy, is the source of truth.
	//    The bundled copy at bundledWPCachePluginDir is a FALLBACK only, used
	//    when the tenant can't reach wordpress.org (per-user egress policy /
	//    offline host / WP.org outage) or when explicitly forced via
	//    JABALI_WP_CACHE_SOURCE=bundled (debug/override path).
	dest := filepath.Join(p.InstallPath, "wp-content", "plugins", "jabali-cache")
	staged := false
	// JAB-64: the release-bundled plugin is the DEFAULT trusted artifact. Cache
	// enable must NOT pull mutable PHP into tenant installs from WordPress.org by
	// default — a compromised package/CDN would be fleet-wide code exec, and this
	// plugin handles the cache drop-ins + Redis credentials. Only an explicit
	// opt-in to the "latest" channel (JABALI_WP_CACHE_SOURCE=wordpress-org)
	// fetches from WP.org; anything else (including unset) uses the bundled copy.
	if strings.EqualFold(os.Getenv("JABALI_WP_CACHE_SOURCE"), "wordpress-org") {
		if out, err := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "plugin", "install", "jabali-cache", "--force"); err == nil {
			staged = true
		} else {
			slog.WarnContext(ctx, "jabali-cache: opt-in wordpress.org install failed, using bundled",
				"os_user", p.OSUser, "install_path", p.InstallPath, "detail", truncateReason(out, 300))
		}
	}
	if !staged {
		if err := stageBundledCachePlugin(ctx, dest, p.OSUser); err != nil {
			return nil, err
		}
	}
	// Normalize ownership regardless of source: nginx (www-data) must be able to
	// read the plugin files the per-user PHP-FPM pool serves.
	if err := execCommandContext(ctx, "chown", "-R", p.OSUser+":www-data", dest).Run(); err != nil {
		return nil, bkInternal("chown plugin", err)
	}

	socket := p.RedisSocket
	if socket == "" {
		socket = "/run/redis/redis.sock"
	}
	db := p.RedisDB
	if db == 0 {
		db = 1
	}

	// 2. Pin the jabali settings as CONSTANTS in wp-config.php — the single,
	//    authoritative config source. The plugin reads its settings from the
	//    options table (wp.org compliance) but apply_constants() makes these
	//    JABALI_CACHE_* defines win, so the per-tenant socket / DB / ACL token /
	//    key prefix (~jc:<osuser>:*) are always correct regardless of any value
	//    saved via the admin screen. We no longer write a jabali-cache-config.php
	//    file (the plugin stopped reading one; it deletes any legacy copy).
	// JAB-62: the ACL username is PER-INSTALL (wp_<osuser>_<installID>). p.Prefix is
	// "<osuser>:<installID>", so wp_ + prefix-with-:-as-_ reproduces exactly the
	// name panel-api provisioned (installACLUser). Never revert to wp_<osuser> —
	// that credential could read sibling installs' keys.
	aclUser := "wp_" + strings.ReplaceAll(p.Prefix, ":", "_")
	if err := setWPConfigCacheConstants(p.InstallPath, socket, db, p.Prefix, p.RedisPassword, aclUser, true, p.MaxTTL, p.PageCache, p.PageTTL, p.MaxMemMB); err != nil {
		return nil, bkInternal("write wp-config constants", err)
	}

	// 3. Grant the tenant access to the Redis client socket group so its
	// php-fpm workers can open /run/redis/redis.sock (the socket is group
	// jabali-redis-clients, NOT jabali-sockets — tenants must never reach the
	// root agent socket; ADR-0148). Group membership is fixed at the fpm master
	// start, so the per-user master is RESTARTED (not reloaded) to pick it up.
	_ = execCommandContext(ctx, "groupadd", "-f", redisClientsGroup).Run()
	if err := execCommandContext(ctx, "usermod", "-aG", redisClientsGroup, p.OSUser).Run(); err != nil {
		return nil, bkInternal("add tenant to "+redisClientsGroup, err)
	}
	// Grant the group access to the Redis socket — both PERSISTENTLY (a redis
	// ExecStartPost drop-in that re-applies on every restart) and IMMEDIATELY
	// (one-shot, no redis restart needed). Doing this in the agent means a plain
	// `jabali update` (binaries only, no install.sh) is enough — the socket grant
	// no longer depends on a full installer run. Idempotent + best-effort.
	ensureRedisSocketGroupAccess(ctx, socket)
	// Best-effort: a missing/!active fpm master (e.g. CLI-only install) isn't fatal.
	_ = execCommandContext(ctx, "systemctl", "restart", "jabali-fpm@"+p.OSUser+".service").Run()

	// 4. Activate + enable (drop-ins + WP_CACHE), as the tenant.
	if out, err := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "plugin", "activate", "jabali-cache"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("wp plugin activate: %v: %s", err, out)}
	}
	if out, err := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "jabali-cache", "enable"); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("wp jabali-cache enable: %v: %s", err, out)}
	}

	// 5. Verify live Redis connectivity with a real SET/GET round-trip (GH #410).
	// `enable` only flips a flag + installs drop-ins; it never touches Redis, so a
	// silent NOPERM (wrong prefix, stale/mis-derived ACL token, no socket access)
	// would otherwise return success with a dead cache. Gate on the probe, and
	// roll the plugin back to inert so we never report a green-but-broken cache.
	if out, err := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "jabali-cache", "verify"); err != nil {
		_ = runWPAsTenant(ctx, p.OSUser, p.InstallPath, "jabali-cache", "disable")
		_ = runWPAsTenant(ctx, p.OSUser, p.InstallPath, "plugin", "deactivate", "jabali-cache")
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("cache enabled but Redis verify failed (rolled back): %s", out)}
	}
	return wordpressCacheSetResult{Ok: true, Enabled: true}, nil
}

// wpConfigBeginMarker / wpConfigEndMarker fence the jabali-managed define block
// in wp-config.php so it can be replaced/removed idempotently.
const (
	wpConfigBeginMarker = "// BEGIN Jabali WP Cache (managed by jabali #406) — do not edit"
	wpConfigEndMarker   = "// END Jabali WP Cache"
)

// setWPConfigCacheConstants writes (enable) or strips (disable) the jabali-managed
// JABALI_CACHE_* define block in <installPath>/wp-config.php. The block is inserted
// just before the "stop editing" marker (or wp-settings.php require), so the defines
// land before WordPress — and the plugin's drop-ins — load.
func phpBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func setWPConfigCacheConstants(installPath, socket string, db int, prefix, password, username string, enable bool, maxTTL int, pageCache bool, pageTTL int, maxMemMB int) error {
	cfgPath := filepath.Join(installPath, "wp-config.php")
	// GH #411: the root agent must NEVER read or write THROUGH a tenant-planted
	// symlink — a tenant owns their docroot and could point wp-config.php (or a
	// parent dir) at /etc/shadow or another user's file, turning this read +
	// write-through + chown into arbitrary-file disclosure / overwrite / LPE.
	// openat2 with RESOLVE_NO_SYMLINKS makes the KERNEL refuse if the file OR any
	// path component is a symlink — race-free, no TOCTOU. We then do every op on
	// the returned fd (read / truncate / write / fchown / fchmod), never via a
	// path that could be re-resolved through a symlink.
	rawFD, oerr := unix.Openat2(unix.AT_FDCWD, cfgPath, &unix.OpenHow{
		Flags:   unix.O_RDWR | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	})
	if oerr != nil {
		return fmt.Errorf("open wp-config refused (symlink in path? #411): %w", oerr)
	}
	f := os.NewFile(uintptr(rawFD), cfgPath)
	defer f.Close()

	raw, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	content := stripWPCacheBlock(string(raw))

	if enable {
		esc := func(s string) string { // PHP single-quote: escape \ then '
			s = strings.ReplaceAll(s, "\\", "\\\\")
			return strings.ReplaceAll(s, "'", "\\'")
		}
		block := wpConfigBeginMarker + "\n" +
			"if ( ! defined( 'JABALI_CACHE_SOCKET' ) )   define( 'JABALI_CACHE_SOCKET', '" + esc(socket) + "' );\n" +
			"if ( ! defined( 'JABALI_CACHE_DB' ) )       define( 'JABALI_CACHE_DB', " + strconv.Itoa(db) + " );\n" +
			"if ( ! defined( 'JABALI_CACHE_PASSWORD' ) ) define( 'JABALI_CACHE_PASSWORD', '" + esc(password) + "' );\n" +
			"if ( ! defined( 'JABALI_CACHE_USER' ) )     define( 'JABALI_CACHE_USER', '" + esc(username) + "' );\n" +
			"if ( ! defined( 'JABALI_CACHE_PREFIX' ) )   define( 'JABALI_CACHE_PREFIX', '" + esc(prefix) + "' );\n" +
			"if ( ! defined( 'JABALI_CACHE_MAXTTL' ) )     define( 'JABALI_CACHE_MAXTTL', " + strconv.Itoa(maxTTL) + " );\n" +
			"if ( ! defined( 'JABALI_CACHE_PAGE_CACHE' ) ) define( 'JABALI_CACHE_PAGE_CACHE', " + phpBool(pageCache) + " );\n" +
			"if ( ! defined( 'JABALI_CACHE_PAGE_TTL' ) )   define( 'JABALI_CACHE_PAGE_TTL', " + strconv.Itoa(pageTTL) + " );\n" +
			wpConfigEndMarker + "\n"
		content = insertBeforeWPSettings(content, block)
	}

	// Rewrite in place on the SAME fd (no path re-resolution → no symlink race).
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		return err
	}
	// Preserve the intended owner/mode (<user>:www-data 0640). The openat2 above
	// guarantees installPath has no symlink components, so stat'ing it is safe;
	// fchown/fchmod act on the fd, never a path.
	if fi, serr := os.Stat(installPath); serr == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			_ = f.Chown(int(st.Uid), int(st.Gid))
		}
	}
	_ = f.Chmod(0o640)
	return nil // deferred f.Close() flushes
}

// stripWPCacheBlock removes a previously-inserted BEGIN..END jabali block
// (idempotent re-apply / clean disable). No block → returned unchanged.
func stripWPCacheBlock(content string) string {
	start := strings.Index(content, wpConfigBeginMarker)
	if start < 0 {
		return content
	}
	end := strings.Index(content[start:], wpConfigEndMarker)
	if end < 0 {
		return content // malformed; leave alone rather than truncate
	}
	end = start + end + len(wpConfigEndMarker)
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return content[:start] + content[end:]
}

// insertBeforeWPSettings places block before the "stop editing" marker or the
// wp-settings.php require (whichever appears first); falls back to appending.
func insertBeforeWPSettings(content, block string) string {
	for _, anchor := range []string{"/* That's all, stop editing!", "require_once ABSPATH . 'wp-settings.php'", "require_once(ABSPATH . 'wp-settings.php')"} {
		if i := strings.Index(content, anchor); i >= 0 {
			return content[:i] + block + content[i:]
		}
	}
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + block
}

// redisSocketAccessDropIn is the systemd drop-in that re-grants the
// jabali-redis-clients group access to the Redis runtime dir + socket on every
// redis start (RuntimeDirectory + socket are recreated each boot). MUST stay
// byte-identical to the copy install.sh writes (install_redis_acl) so the two
// never fight over the file. sha256 24ed9aaf...
const redisSocketAccessDropIn = "# Managed by jabali - #406 / ADR-0148. Do NOT hand-edit.\n" +
	"[Service]\n" +
	"ExecStartPost=+/bin/sh -c 'for i in 1 2 3 4 5; do [ -S /run/redis/redis.sock ] && break; sleep 1; done; " +
	"setfacl -m g:jabali-redis-clients:rx /run/redis 2>/dev/null; " +
	"setfacl -m g:jabali-redis-clients:rw /run/redis/redis.sock 2>/dev/null; true'\n"

// ensureRedisSocketGroupAccess installs the persistent drop-in (idempotent;
// daemon-reload only when it changes) and applies the ACL to the live socket so
// caching works without waiting for a redis restart. All best-effort.
func ensureRedisSocketGroupAccess(ctx context.Context, socket string) {
	const unitPath = "/etc/systemd/system/redis-server.service.d/20-jabali-redis-clients.conf"
	if cur, err := os.ReadFile(unitPath); err != nil || string(cur) != redisSocketAccessDropIn {
		if mkErr := os.MkdirAll("/etc/systemd/system/redis-server.service.d", 0o755); mkErr == nil {
			if wErr := os.WriteFile(unitPath, []byte(redisSocketAccessDropIn), 0o644); wErr == nil {
				_ = execCommandContext(ctx, "systemctl", "daemon-reload").Run()
			}
		}
	}
	// Immediate: apply to the live runtime dir + socket (the drop-in only fires on
	// the next redis (re)start).
	_ = execCommandContext(ctx, "setfacl", "-m", "g:"+redisClientsGroup+":rx", "/run/redis").Run()
	_ = execCommandContext(ctx, "setfacl", "-m", "g:"+redisClientsGroup+":rw", socket).Run()
}

func runWPAsTenant(ctx context.Context, osUser, installPath string, args ...string) error {
	_, err := runWPAsTenantOut(ctx, osUser, installPath, args...)
	return err
}

func runWPAsTenantOut(ctx context.Context, osUser, installPath string, args ...string) (string, error) {
	full := append([]string{"wp"}, args...)
	full = append(full, "--path="+installPath)
	cmd := buildSystemdRunCmd(ctx, osUser, full...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// wordpressCachePluginRefreshParams drives wordpress.cache_plugin_refresh.
type wordpressCachePluginRefreshParams struct {
	InstallPath string `json:"install_path"`
	OSUser      string `json:"os_user"`
}

type wordpressCachePluginRefreshResult struct {
	Refreshed bool   `json:"refreshed"` // false when the plugin wasn't installed (skipped)
	Version   string `json:"version,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// wordpressCachePluginRefreshHandler updates the jabali-cache plugin on one WP
// install to the latest WordPress.org release (GH #613 made WP.org canonical).
// Idempotent: a no-op when already current; a skip (not an error) when the
// plugin isn't installed on that site. Used by the `jabali app
// refresh-cache-plugin` sweep so a `jabali update` brings every cache-enabled
// site to the published version without a manual cache re-toggle.
func wordpressCachePluginRefreshHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p wordpressCachePluginRefreshParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !strings.HasPrefix(p.InstallPath, "/home/") || strings.Contains(p.InstallPath, "..") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "install_path must be under /home/ with no .."}
	}
	if p.OSUser == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "os_user required"}
	}

	// Skip sites where the plugin isn't present — refresh only touches installs
	// that actually have jabali-cache (cache-enabled ones).
	if out, err := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "plugin", "is-installed", "jabali-cache"); err != nil {
		return wordpressCachePluginRefreshResult{Refreshed: false, Detail: "plugin not installed: " + truncateReason(out, 200)}, nil
	}

	// Use `plugin install --force`, NOT `plugin update`: `update` honors
	// WordPress's ~12h "update available?" transient, so right after a WP.org
	// release it can report "nothing to do" and silently no-op. `install --force`
	// always fetches the *current* WordPress.org version and overwrites, so the
	// sweep reliably converges every site to the published release (WP.org is
	// canonical per #613). Reinstalling keeps the plugin's activation state.
	// JAB-64: refresh re-stages the trusted release-bundled plugin by default;
	// only the opt-in "latest" channel reinstalls from WordPress.org.
	if strings.EqualFold(os.Getenv("JABALI_WP_CACHE_SOURCE"), "wordpress-org") {
		if out, err := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "plugin", "install", "jabali-cache", "--force"); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
				Message: fmt.Sprintf("wp plugin install --force jabali-cache: %v: %s", err, out)}
		}
	} else {
		dest := filepath.Join(p.InstallPath, "wp-content", "plugins", "jabali-cache")
		if err := stageBundledCachePlugin(ctx, dest, p.OSUser); err != nil {
			return nil, err
		}
	}
	// Report the resulting version (best-effort).
	ver, _ := runWPAsTenantOut(ctx, p.OSUser, p.InstallPath, "plugin", "get", "jabali-cache", "--field=version")
	return wordpressCachePluginRefreshResult{Refreshed: true, Version: strings.TrimSpace(ver)}, nil
}

// stageBundledCachePlugin installs the release-bundled jabali-cache plugin into
// dest (rm + cp -a) and normalizes ownership so the per-user PHP-FPM pool and
// nginx (www-data) can read it. This is the DEFAULT trusted source (JAB-64):
// panel-managed infra code must not come from a mutable external channel.
func stageBundledCachePlugin(ctx context.Context, dest, osUser string) error {
	if _, err := os.Stat(bundledWPCachePluginDir); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("bundled jabali-cache missing at %s \u2014 re-run install.sh", bundledWPCachePluginDir)}
	}
	if err := execCommandContext(ctx, "rm", "-rf", dest).Run(); err != nil {
		return bkInternal("clear old plugin", err)
	}
	if err := execCommandContext(ctx, "cp", "-a", bundledWPCachePluginDir, dest).Run(); err != nil {
		return bkInternal("stage plugin (bundled)", err)
	}
	if err := execCommandContext(ctx, "chown", "-R", osUser+":www-data", dest).Run(); err != nil {
		return bkInternal("chown plugin", err)
	}
	return nil
}

func init() {
	Default.Register("wordpress.cache_set", wordpressCacheSetHandler)
	Default.Register("wordpress.cache_plugin_refresh", wordpressCachePluginRefreshHandler)
}
