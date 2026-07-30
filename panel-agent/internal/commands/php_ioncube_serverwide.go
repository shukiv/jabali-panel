package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// php_ioncube_serverwide.go — server-wide (per PHP version) ionCube Loader
// enable/disable, plus live status. jabali installs the loader per PHP version
// and enables it for EVERY pool on that version by dropping the loader into the
// version's conf.d — the same server-wide-per-version model ADR-0031 uses for
// the rest of the PHP extension catalogue. The ionCube Loader is a benign,
// trusted decoder (not tenant-supplied code), so per-tenant isolation buys
// little; cPanel/Plesk install it server-wide too.
//
// State is LIVE, not persisted (mirrors ADR-0031): loader-installed = the .so
// exists under /usr/lib/php/ioncube/<ver>/; version-enabled = the conf.d ini
// exists. No DB row, no reconciler, no drift.

// ionCube path roots. Overridable for tests; production defaults match the
// AppArmor-permitted locations (/etc/php/** r, /usr/lib/php/** mr).
func ioncubePHPEtcRoot() string {
	if v := os.Getenv("JABALI_PHP_ETC_ROOT"); v != "" {
		return v
	}
	return "/etc/php"
}

func ioncubeLoaderLibRoot() string {
	if v := os.Getenv("JABALI_IONCUBE_LIB_ROOT"); v != "" {
		return v
	}
	return "/usr/lib/php/ioncube"
}

// ioncubeConfdIniPath is the server-wide enable drop-in: the standard FPM
// conf.d scanned by EVERY pool of that version. Named 00-ioncube.ini so it
// sorts before 10-opcache.ini — ionCube fatals unless its loader is the first
// zend_extension parsed.
func ioncubeConfdIniPath(version string) string {
	return filepath.Join(ioncubePHPEtcRoot(), version, "fpm", "conf.d", "00-ioncube.ini")
}

// ioncubeCliConfdIniPath is the CLI SAPI drop-in. JAB-175: enabling only the FPM
// conf.d left wp-cli (CLI SAPI) without the loader, so activating / running an
// ionCube-encoded plugin via wp-cli failed even though the site (FPM) worked.
// Debian's phpenmod enables an extension for both SAPIs; ionCube must match.
func ioncubeCliConfdIniPath(version string) string {
	return filepath.Join(ioncubePHPEtcRoot(), version, "cli", "conf.d", "00-ioncube.ini")
}

// ioncubeConfdIniPaths returns every SAPI conf.d drop-in for a version (FPM +
// CLI). The FPM one stays the canonical "enabled" marker (status + guards key
// on it), but both are written/removed together.
func ioncubeConfdIniPaths(version string) []string {
	return []string{ioncubeConfdIniPath(version), ioncubeCliConfdIniPath(version)}
}

// ioncubeLoaderSoPath is where php.ioncube.install writes the loader .so.
func ioncubeLoaderSoPath(version string) string {
	return filepath.Join(ioncubeLoaderLibRoot(), version, "ioncube_loader_lin_"+version+".so")
}

// ioncubeVersionEnabled reports whether the server-wide conf.d ini exists.
func ioncubeVersionEnabled(version string) bool {
	_, err := os.Stat(ioncubeConfdIniPath(version))
	return err == nil
}

type ioncubeServerSetParams struct {
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
}

type ioncubeServerSetResponse struct {
	Version string `json:"version"`
	Enabled bool   `json:"enabled"`
	Changed bool   `json:"changed"`
}

// phpIoncubeServerSetHandler enables/disables the ionCube Loader server-wide for
// a PHP version. Enable requires the loader .so to already be installed
// (CodeFailedPrecondition otherwise); the panel gates on this too, but the agent
// re-checks at the trust boundary. On change, every running FPM master on that
// version is reloaded so the zend_extension set takes effect.
func phpIoncubeServerSetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ioncubeServerSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !phpVersionRegex.MatchString(p.Version) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid PHP version %q (want X.Y)", p.Version)}
	}

	changed := false

	if p.Enabled {
		soPath := ioncubeLoaderSoPath(p.Version)
		if _, err := os.Stat(soPath); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeFailedPrecondition,
				Message: fmt.Sprintf("ionCube Loader is not installed for PHP %s (missing %s); install it first", p.Version, soPath),
			}
		}
		want := "zend_extension=" + soPath + "\n"
		// Enable for every SAPI (FPM + CLI) so wp-cli / cron load it too (JAB-175).
		for _, iniPath := range ioncubeConfdIniPaths(p.Version) {
			if cur, err := os.ReadFile(iniPath); err != nil || string(cur) != want {
				if err := os.MkdirAll(filepath.Dir(iniPath), 0o755); err != nil {
					return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir conf.d: %v", err)}
				}
				if err := writeFileAtomic(iniPath, []byte(want), 0o644); err != nil {
					return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write ini: %v", err)}
				}
				changed = true
			}
		}
	} else {
		for _, iniPath := range ioncubeConfdIniPaths(p.Version) {
			if _, err := os.Stat(iniPath); err == nil {
				if err := os.Remove(iniPath); err != nil {
					return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove ini: %v", err)}
				}
				changed = true
			}
		}
	}

	// Reload every running master on this version so the change takes effect.
	// Skipped in tests (no systemd); reload failures are logged by the helper,
	// not fatal — the next tick / manual reload converges.
	if changed && os.Getenv("JABALI_PHP_POOL_SKIP_RELOAD") == "" {
		reloadVersionFPMUnits(ctx, p.Version)
	}

	return ioncubeServerSetResponse{Version: p.Version, Enabled: p.Enabled, Changed: changed}, nil
}

type ioncubeStatusResponse struct {
	// InstalledVersions is every PHP version with a loader .so on disk.
	InstalledVersions []string `json:"installed_versions"`
	// EnabledVersions is every PHP version with the server-wide conf.d ini.
	EnabledVersions []string `json:"enabled_versions"`
}

// phpIoncubeStatusHandler reports live state: which versions have the loader
// installed and which have it enabled server-wide. Pure fs read.
func phpIoncubeStatusHandler(_ context.Context, _ json.RawMessage) (any, error) {
	resp := ioncubeStatusResponse{InstalledVersions: []string{}, EnabledVersions: []string{}}

	if matches, _ := filepath.Glob(filepath.Join(ioncubeLoaderLibRoot(), "*", "ioncube_loader_lin_*.so")); len(matches) > 0 {
		seen := map[string]bool{}
		for _, m := range matches {
			ver := filepath.Base(filepath.Dir(m))
			if phpVersionRegex.MatchString(ver) && !seen[ver] {
				seen[ver] = true
				resp.InstalledVersions = append(resp.InstalledVersions, ver)
			}
		}
		sort.Strings(resp.InstalledVersions)
	}

	if matches, _ := filepath.Glob(filepath.Join(ioncubePHPEtcRoot(), "*", "fpm", "conf.d", "00-ioncube.ini")); len(matches) > 0 {
		for _, m := range matches {
			// .../<ver>/fpm/conf.d/00-ioncube.ini → version is 3 dirs up.
			ver := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(m))))
			if phpVersionRegex.MatchString(ver) {
				resp.EnabledVersions = append(resp.EnabledVersions, ver)
			}
		}
		sort.Strings(resp.EnabledVersions)
	}

	return resp, nil
}

func init() {
	Default.Register("php.ioncube.server_set", phpIoncubeServerSetHandler)
	Default.Register("php.ioncube.status", phpIoncubeStatusHandler)
}
