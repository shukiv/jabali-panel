package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// app.python.control — start | stop | restart | rebuild | status a Python or
// Node.js app.
type pythonAppControlParams struct {
	AppID  string `json:"app_id"`
	Action string `json:"action"` // start|stop|restart|rebuild|status
}

type pythonAppControlResult struct {
	Active bool   `json:"active"`
	State  string `json:"state"` // systemctl is-active output
}

func pythonAppControlHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p pythonAppControlParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !appIDRe.MatchString(p.AppID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid app_id"}
	}
	unit := pythonAppUnitName(p.AppID)
	switch p.Action {
	case "start", "stop", "restart":
		// GH #357: if the app never finished provisioning (its build failed
		// at the venv/pip step, before the unit was written) the raw
		// systemctl error is a cryptic "Unit ... not found". Detect the
		// missing unit and return an actionable message instead.
		if _, statErr := os.Stat(filepath.Join(pythonAppUnitDir, unit)); statErr != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeFailedPrecondition,
				Message: "app is not provisioned — its build has not completed or failed (check the app logs / requirements). It rebuilds automatically once the problem is fixed.",
			}
		}
		if out, err := execCommandContext(ctx, "systemctl", p.Action, unit).CombinedOutput(); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemctl %s %s: %v: %s", p.Action, unit, err, strings.TrimSpace(string(out)))}
		}
	case "rebuild":
		// An app is built only when its manifests change (package.json and
		// the lockfile, or requirements.txt), so new code alone, such as a
		// git pull of a Next.js app, is never built. Rebuild forgets the
		// last build: the next apply (the reconciler applies every app on
		// each pass, about once a minute) installs and builds the app again
		// and restarts it once the build is done. The unit is not touched
		// until then, though the build runs in the app's directory under the
		// running app, as every build does. An app with no manifests has
		// nothing to build and is not restarted. No unit is needed: an app whose first build
		// failed has none, and this skips that failure's backoff. A build
		// already running would record its result when it ends and undo
		// this, so it is refused until that build has finished.
		if appBuildRunning(p.AppID) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "a build of this app is already running; rebuild it once that build has finished"}
		}
		clearAppBuildMarkers(pythonAppEnvDir, p.AppID)
	case "status":
		// no-op; just report below
	default:
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "action must be start|stop|restart|rebuild|status"}
	}
	state, _ := execCommandContext(ctx, "systemctl", "is-active", unit).Output()
	st := strings.TrimSpace(string(state))
	return pythonAppControlResult{Active: st == "active", State: st}, nil
}

// app.python.remove — stop + disable + delete the unit + env file. The venv
// (under the user's app_root) is intentionally left in place; deleting it is
// the panel's job via the File Manager / a separate purge, so app code +
// data aren't destroyed by a unit teardown.
type pythonAppRemoveParams struct {
	AppID string `json:"app_id"`
}

func pythonAppRemoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p pythonAppRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !appIDRe.MatchString(p.AppID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid app_id"}
	}
	cancelPythonBuild(p.AppID)
	stopAppBuildScopes(p.AppID)
	unit := pythonAppUnitName(p.AppID)
	_ = execCommandContext(ctx, "systemctl", "stop", unit).Run()
	// GH #2289: a unit still running after the stop keeps its port. Keep its
	// files and fail, so the caller knows and the reconciler's sweep, which
	// finds units by their files, tries again.
	out, _ := execCommandContext(ctx, "systemctl", "is-active", unit).Output()
	if st := strings.TrimSpace(string(out)); st != "inactive" && st != "failed" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("%s is still %q after systemctl stop", unit, st)}
	}
	_ = execCommandContext(ctx, "systemctl", "disable", "--quiet", unit).Run()
	_ = os.Remove(filepath.Join(pythonAppUnitDir, unit))
	// The env file and every runtime's build markers (pip: .reqsha/.reqfail,
	// npm: .npmsha/.npmfail), so a later app with the same id rebuilds.
	_ = os.Remove(filepath.Join(pythonAppEnvDir, p.AppID+".env"))
	clearAppBuildMarkers(pythonAppEnvDir, p.AppID)
	_ = execCommandContext(ctx, "systemctl", "daemon-reload").Run()
	return map[string]any{"removed": true}, nil
}

// app.python.logs — tail the unit journal.
type pythonAppLogsParams struct {
	AppID string `json:"app_id"`
	Lines int    `json:"lines,omitempty"`
}

func pythonAppLogsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p pythonAppLogsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !appIDRe.MatchString(p.AppID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid app_id"}
	}
	n := p.Lines
	if n <= 0 || n > 1000 {
		n = 200
	}
	out, _ := execCommandContext(ctx, "journalctl", "-u", pythonAppUnitName(p.AppID),
		"-n", fmt.Sprintf("%d", n), "--no-pager", "-o", "short-iso").CombinedOutput()
	return map[string]any{"logs": string(out)}, nil
}

// appBuildMarkerExts are the files in the env dir that record an app's last
// build: pip's requirements sha and cached failure, npm's manifests sha and
// cached failure. With none of them the next apply builds the app.
var appBuildMarkerExts = []string{".reqsha", ".reqfail", ".npmsha", ".npmfail"}

// clearAppBuildMarkers removes the app's build markers from dir (the env dir),
// so its next apply builds again whatever its manifests say.
func clearAppBuildMarkers(dir, appID string) {
	for _, ext := range appBuildMarkerExts {
		_ = os.Remove(filepath.Join(dir, appID+ext))
	}
}

func init() {
	Default.Register("app.python.control", pythonAppControlHandler)
	Default.Register("app.python.remove", pythonAppRemoveHandler)
	Default.Register("app.python.logs", pythonAppLogsHandler)
}
