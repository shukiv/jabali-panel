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

// php_ioncube_pool.go — per-user (per-pool) ionCube Loader enable/disable, plus
// live status. The ionCube Loader is a version-locked, closed-source
// zend_extension; jabali loads it for a SINGLE tenant's FPM master (never
// box-wide) by dropping a `00-ioncube.ini` into that user's per-version scan
// dir, which fpm-exec exposes via PHP_INI_SCAN_DIR. See
// docs/adr (per-user-master amendment of ADR-0031) + the spike notes.
//
// State is LIVE, not persisted (mirrors ADR-0031): loader-installed = the .so
// exists under /usr/lib/php/ioncube/<ver>/; pool-enabled = the per-user ini
// exists. No DB row, no reconciler, no drift.

// ionCube path roots. Overridable for tests; production defaults match the
// AppArmor-permitted locations (/etc/php/** r, /usr/lib/php/** mr — no profile
// change needed).
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

// ioncubeScanDir is the per-user extra-ini directory fpm-exec points
// PHP_INI_SCAN_DIR at: /etc/php/<ver>/fpm/jabali-ext/<user>.
func ioncubeScanDir(version, username string) string {
	return filepath.Join(ioncubePHPEtcRoot(), version, "fpm", "jabali-ext", username)
}

// ioncubeIniPath is the drop-in that enables the loader for one user's master.
func ioncubeIniPath(version, username string) string {
	return filepath.Join(ioncubeScanDir(version, username), "00-ioncube.ini")
}

// ioncubeLoaderSoPath is where php.ioncube.install writes the loader .so.
func ioncubeLoaderSoPath(version string) string {
	return filepath.Join(ioncubeLoaderLibRoot(), version, "ioncube_loader_lin_"+version+".so")
}

type ioncubePoolSetParams struct {
	Username string `json:"username"`
	Version  string `json:"version"`
	Enabled  bool   `json:"enabled"`
}

type ioncubePoolSetResponse struct {
	Username string `json:"username"`
	Version  string `json:"version"`
	Enabled  bool   `json:"enabled"`
	Changed  bool   `json:"changed"`
}

// phpIoncubePoolSetHandler enables/disables the ionCube Loader for one user's
// FPM master on a specific PHP version. Enable requires the loader .so to
// already be installed for that version (CodeFailedPrecondition otherwise);
// the panel gates on this too, but the agent re-checks at the trust boundary.
func phpIoncubePoolSetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p ioncubePoolSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !looksLikeUnixUsername(p.Username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid username %q", p.Username)}
	}
	if !phpVersionRegex.MatchString(p.Version) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid PHP version %q (want X.Y)", p.Version)}
	}

	iniPath := ioncubeIniPath(p.Version, p.Username)
	changed := false

	if p.Enabled {
		soPath := ioncubeLoaderSoPath(p.Version)
		if _, err := os.Stat(soPath); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeFailedPrecondition,
				Message: fmt.Sprintf("ionCube Loader is not installed for PHP %s (missing %s); an admin must install it first", p.Version, soPath),
			}
		}
		// Idempotent: only write + reload when the desired content differs.
		want := "zend_extension=" + soPath + "\n"
		if cur, err := os.ReadFile(iniPath); err != nil || string(cur) != want {
			if err := os.MkdirAll(ioncubeScanDir(p.Version, p.Username), 0o755); err != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir scan dir: %v", err)}
			}
			if err := os.WriteFile(iniPath, []byte(want), 0o644); err != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write ini: %v", err)}
			}
			changed = true
		}
	} else {
		if _, err := os.Stat(iniPath); err == nil {
			if err := os.Remove(iniPath); err != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove ini: %v", err)}
			}
			changed = true
		}
	}

	// Reload the user's master so the zend_extension set takes effect. Only
	// when something changed — a no-op set shouldn't bounce a live pool.
	if changed {
		if err := restartOrReloadUserFPM(ctx, p.Username, p.Version, p.Version); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("reload FPM for %s: %v", p.Username, err)}
		}
	}

	return ioncubePoolSetResponse{Username: p.Username, Version: p.Version, Enabled: p.Enabled, Changed: changed}, nil
}

type ioncubeStatusResponse struct {
	// InstalledVersions is every PHP version with a loader .so on disk.
	InstalledVersions []string `json:"installed_versions"`
	// Enabled maps username -> sorted list of versions the user has enabled.
	Enabled map[string][]string `json:"enabled"`
}

// phpIoncubeStatusHandler reports live state: which versions have the loader
// installed, and which users have it enabled on which versions. Pure fs read.
func phpIoncubeStatusHandler(_ context.Context, _ json.RawMessage) (any, error) {
	resp := ioncubeStatusResponse{InstalledVersions: []string{}, Enabled: map[string][]string{}}

	// Installed loaders: /usr/lib/php/ioncube/<ver>/ioncube_loader_lin_<ver>.so
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

	// Enabled pools: /etc/php/<ver>/fpm/jabali-ext/<user>/00-ioncube.ini
	if matches, _ := filepath.Glob(filepath.Join(ioncubePHPEtcRoot(), "*", "fpm", "jabali-ext", "*", "00-ioncube.ini")); len(matches) > 0 {
		for _, m := range matches {
			user := filepath.Base(filepath.Dir(m))
			// .../<ver>/fpm/jabali-ext/<user>/00-ioncube.ini → version is 4 dirs up.
			ver := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(m)))))
			if !phpVersionRegex.MatchString(ver) || !looksLikeUnixUsername(user) {
				continue
			}
			resp.Enabled[user] = append(resp.Enabled[user], ver)
		}
		for u := range resp.Enabled {
			sort.Strings(resp.Enabled[u])
		}
	}

	return resp, nil
}

func init() {
	Default.Register("php.ioncube.pool_set", phpIoncubePoolSetHandler)
	Default.Register("php.ioncube.status", phpIoncubeStatusHandler)
}
