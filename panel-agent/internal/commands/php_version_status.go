package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// SupportedPHPVersions is the list of PHP versions that can be managed.
// Exported so API and other packages can reference it.
var SupportedPHPVersions = []string{"7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.5"}

// phpVersionStatusResponse is the output shape for php.version.status.
type phpVersionStatusResponse struct {
	DefaultVersion string                   `json:"default_version"`
	Versions       []phpVersionStatusDetail `json:"versions"`
}

// phpVersionStatusDetail represents the status of a single PHP version.
type phpVersionStatusDetail struct {
	Version    string `json:"version"`
	Installed  bool   `json:"installed"`
	FPMRunning bool   `json:"fpm_running"`
	// GH#180: per-user FPM worker health for this version. The global
	// php<v>-fpm.service is masked (ADR-0025); the real workers are the
	// jabali-fpm@<user>.service instances pinned to this version.
	// WorkersTotal = users pinned to <v>; WorkersRunning = of those, active.
	WorkersRunning int `json:"workers_running"`
	WorkersTotal   int `json:"workers_total"`
}

// isInstalledPHPVersion reports whether the PHP version's runtime binaries are
// present. The binaries are the source of truth — NOT /etc/php/<v>/fpm/pool.d,
// which jabali fills with its own (non-apt) per-user pool files that survive
// `apt purge`, so a pool.d check reports a purged version as still installed
// (GH #187).
func isInstalledPHPVersion(version string) bool {
	for _, bin := range []string{
		"/usr/bin/php" + version,
		"/usr/sbin/php-fpm" + version,
	} {
		if _, err := os.Stat(bin); err == nil {
			return true
		}
	}
	return false
}

// fpmWorkerStatus reports how many per-user jabali-fpm@<user>.service
// workers are pinned to `version` (total) and how many are active
// (running). GH#180: the global php<v>-fpm.service is MASKED (ADR-0025),
// so checking it always reported Stopped — the real workers are per-user.
var userPhpverDir = "/etc/jabali-panel/user-phpver"

var fpmWorkerStatus = func(version string) (running, total int) {
	entries, err := os.ReadDir(userPhpverDir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		user := e.Name()
		if !userPinnedToVersion(user, version) {
			continue
		}
		total++
		if fpmInstanceActive(user) {
			running++
		}
	}
	return running, total
}

// fpmInstanceActive reports whether jabali-fpm@<user>.service is active.
var fpmInstanceActive = func(user string) bool {
	cmd := execCommand("systemctl", "is-active", "--quiet", "jabali-fpm@"+user+".service")
	return cmd.Run() == nil
}

// checkFPMRunning reports whether ANY per-user FPM worker for `version` is
// active. Back-compat wrapper for install/uninstall callers (GH#180: was a
// is-active check on the masked global php<v>-fpm.service).
var checkFPMRunning = func(version string) bool {
	running, _ := fpmWorkerStatus(version)
	return running > 0
}

func phpVersionStatusHandler(ctx context.Context, params json.RawMessage) (any, error) {
	// No params expected for php.version.status.
	if params != nil && len(params) > 0 {
		var p map[string]interface{}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("failed to parse params: %v", err),
			}
		}
	}

	versions := make([]phpVersionStatusDetail, len(SupportedPHPVersions))
	for i, v := range SupportedPHPVersions {
		running, total := fpmWorkerStatus(v)
		versions[i] = phpVersionStatusDetail{
			Version:        v,
			Installed:      isInstalledPHPVersion(v),
			FPMRunning:     running > 0,
			WorkersRunning: running,
			WorkersTotal:   total,
		}
	}

	return phpVersionStatusResponse{
		DefaultVersion: "8.5",
		Versions:       versions,
	}, nil
}

func init() {
	Default.Register("php.version.status", phpVersionStatusHandler)
}
