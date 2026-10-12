package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// cronRemoveParams is the input for cron.remove command.
type cronRemoveParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	JobID     string `json:"job_id"`
	RunAsRoot bool   `json:"run_as_root,omitempty"`
}

// cronRemoveResponse is the output from cron.remove.
type cronRemoveResponse struct {
	NoChange bool `json:"no_change,omitempty"`
}

func cronRemoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cronRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate inputs
	if p.Username == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "username required",
		}
	}
	// Same rule as cron.apply: job_id lands in root-side unit paths that this
	// handler unlinks, so validate the shape before building any path.
	if !ulidRE.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "job_id must be a 26-char ULID",
		}
	}
	if p.JobID == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "job_id required",
		}
	}

	// Root cron branch (admin-only at API gate): system-scoped unit
	// lives under /etc/systemd/system/. Different path, system-level
	// systemctl, no per-user manager check.
	if p.RunAsRoot {
		return removeRootCron(ctx, p)
	}

	// Resolve user's UID
	u, err := lookupCronUser(p.Username)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("user %s not found: %v", p.Username, err),
		}
	}

	uid, _ := strconv.Atoi(u.Uid)
	runtimeDir := fmt.Sprintf("/run/user/%d", uid)

	// The units live where cron.apply writes them: the user's systemd search
	// path (GH #273). This looked in the old /etc/jabali-panel/cron-units
	// directory instead, so every removal reported no change and left the
	// timer running. The home is tenant-writable and the agent is root, so
	// every check and unlink there goes through a home-rooted scope that
	// refuses a planted symlink (GH #475). Units written before #273 may still
	// sit in the legacy directory, which root owns.
	scope, err := filesafe.NewScope(p.Username, p.Username, []string{u.HomeDir})
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "scope init: " + err.Error()}
	}
	unitsDir := cronUnitsDir(u)
	serviceName := fmt.Sprintf("jabali-cron-%s.service", p.JobID)
	timerName := fmt.Sprintf("jabali-cron-%s.timer", p.JobID)
	homeUnits := []string{
		filepath.Join(unitsDir, serviceName),
		filepath.Join(unitsDir, timerName),
		filepath.Join(unitsDir, "timers.target.wants", timerName),
	}
	legacyUnits := []string{
		filepath.Join(legacyCronUnitsRoot, p.Username, serviceName),
		filepath.Join(legacyCronUnitsRoot, p.Username, timerName),
	}

	inHome := false
	for _, path := range homeUnits {
		found, err := cronUnitInScope(scope, path)
		if err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("check %s: %v", path, err),
			}
		}
		inHome = inHome || found
	}
	inLegacy := fileExists(legacyUnits[0]) || fileExists(legacyUnits[1])
	if !inHome && !inLegacy {
		return &cronRemoveResponse{
			NoChange: true,
		}, nil
	}

	if inHome {
		// Disable and stop the timer
		disableErr := systemctlUserExec(ctx, p.Username, runtimeDir, "disable", "--now", timerName)
		if disableErr != nil {
			// If user's systemd manager is unreachable, return a specific error
			// but still attempt cleanup of files
			if strings.Contains(disableErr.Error(), "user manager not running") ||
				strings.Contains(disableErr.Error(), "User manager is not running") ||
				strings.Contains(disableErr.Error(), "Connection refused") {
				// Clean up files anyway, the enable link included: disable
				// did not run, so it is still there.
				_ = removeCronUnits(scope, homeUnits, legacyUnits)
				return nil, &agentwire.AgentError{
					Code:    "user_manager_unreachable",
					Message: fmt.Sprintf("user %s systemd manager unreachable; files cleaned up locally", p.Username),
				}
			}
			// Other errors are fatal
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to disable timer: %v", disableErr),
			}
		}
	}

	// Remove unit files
	if err := removeCronUnits(scope, homeUnits, legacyUnits); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to remove unit files: %v", err),
		}
	}

	// Reload systemd once the files are gone, so the manager drops the unit.
	if inHome {
		if err := systemctlUserExec(ctx, p.Username, runtimeDir, "daemon-reload"); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to reload systemd: %v", err),
			}
		}
	}

	return &cronRemoveResponse{}, nil
}

// cronUnitInScope reports whether path exists in the tenant's home, without
// following a symlink. A missing file or directory is (false, nil); a
// symlinked component is an error.
func cronUnitInScope(scope *filesafe.Scope, path string) (bool, error) {
	li, err := scope.ExistsInScope(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return li != nil, err
}

// removeCronUnits unlinks a job's units: those in the tenant's home through
// the scope, the legacy ones directly. Files already gone are fine.
func removeCronUnits(scope *filesafe.Scope, homeUnits, legacyUnits []string) error {
	for _, path := range homeUnits {
		if err := scope.RemoveInScope(path, false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	for _, path := range legacyUnits {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

// fileExists checks if a file exists without following symlinks.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func init() {
	Default.Register("cron.remove", cronRemoveHandler)
}

// removeRootCron disables + removes the system-scoped systemd timer
// for a root cron. Mirrors the per-user path's "clean up files even
// when systemctl is unhappy" behaviour, scoped to /etc/systemd/system.
func removeRootCron(ctx context.Context, p cronRemoveParams) (any, error) {
	servicePath := filepath.Join("/etc/systemd/system", fmt.Sprintf("jabali-cron-%s.service", p.JobID))
	timerPath := filepath.Join("/etc/systemd/system", fmt.Sprintf("jabali-cron-%s.timer", p.JobID))
	if !fileExists(servicePath) && !fileExists(timerPath) {
		return &cronRemoveResponse{NoChange: true}, nil
	}
	_ = execCommandContext(ctx, "systemctl", "disable", "--now", fmt.Sprintf("jabali-cron-%s.timer", p.JobID)).Run()
	_ = os.Remove(servicePath)
	_ = os.Remove(timerPath)
	_ = execCommandContext(ctx, "systemctl", "daemon-reload").Run()
	return &cronRemoveResponse{}, nil
}
