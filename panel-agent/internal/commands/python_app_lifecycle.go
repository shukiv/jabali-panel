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

// app.python.control — start | stop | restart | status a Python app unit.
type pythonAppControlParams struct {
	AppID  string `json:"app_id"`
	Action string `json:"action"` // start|stop|restart|status
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
	case "status":
		// no-op; just report below
	default:
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "action must be start|stop|restart|status"}
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
	unit := pythonAppUnitName(p.AppID)
	_ = execCommandContext(ctx, "systemctl", "stop", unit).Run()
	_ = execCommandContext(ctx, "systemctl", "disable", "--quiet", unit).Run()
	_ = os.Remove(filepath.Join(pythonAppUnitDir, unit))
	_ = os.Remove(filepath.Join(pythonAppEnvDir, p.AppID+".env"))
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

func init() {
	Default.Register("app.python.control", pythonAppControlHandler)
	Default.Register("app.python.remove", pythonAppRemoveHandler)
	Default.Register("app.python.logs", pythonAppLogsHandler)
}
