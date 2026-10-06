package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// serviceRestartParams names the service to restart. `name` is the bare
// unit basename (no `.service` suffix), matching what service.list
// returns — keeps round-trip symmetry obvious at the API layer.
type serviceRestartParams struct {
	Name string `json:"name"`
	// Deferred schedules the restart a moment out and returns at once. The
	// panel sets it for units its own request runs through (nginx,
	// jabali-panel, jabali-agent, redis-server — GH #1992); restarting one
	// of those inline cut off the response asking for it.
	Deferred bool `json:"deferred,omitempty"`
}

// serviceRestartResponse reports the post-restart state so the UI can
// re-render the status tag without a second round-trip. A deferred restart
// reports the state before it runs, with Scheduled set.
type serviceRestartResponse struct {
	Name         string `json:"name"`
	Active       string `json:"active"`
	LoadState    string `json:"load_state"`
	Scheduled    bool   `json:"scheduled,omitempty"`
	DelaySeconds int    `json:"delay_seconds,omitempty"`
}

// deferredRestartDelaySeconds is how far out a deferred restart runs: long
// enough for the response to leave nginx (and any proxy in front of it).
const deferredRestartDelaySeconds = 2

// serviceRestartHandler is the inverse of service.list — takes one name
// from the same allow-list and hits `systemctl restart`. Refuses
// services we don't manage (security boundary) and refuses masked units
// (restarting a masked unit always fails — better to return a clean
// error than a systemctl stderr dump).
func serviceRestartHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "params required",
		}
	}
	var p serviceRestartParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse params: %v", err),
		}
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "name required",
		}
	}

	// Allow-list check: only services we publish via service.list may
	// be restarted. Prevents a compromised panel token from turning
	// the restart endpoint into arbitrary systemctl access.
	if !isAllowedService(p.Name) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("service %q not in allow-list", p.Name),
		}
	}

	// Validate characters too (belt-and-braces against injection).
	for _, c := range p.Name {
		if !isServiceNameChar(c) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: "invalid service name",
			}
		}
	}

	unit := fmt.Sprintf("%s.service", p.Name)

	// Bail early on masked units — systemctl restart on a masked unit
	// fails with "Unit X is masked", which is a surprising error to
	// surface in a UI toast. Report it as FailedPrecondition so the
	// API layer can render a helpful message.
	loadState, _ := systemctlRunner(ctx, "show", "-p", "LoadState", "--value", unit)
	if strings.TrimSpace(loadState) == "masked" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("%s is masked; unmask via systemctl before restarting", p.Name),
		}
	}
	if strings.TrimSpace(loadState) == "not-found" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("%s is not installed", p.Name),
		}
	}

	if p.Deferred {
		return scheduleServiceRestart(ctx, p.Name, unit)
	}

	if out, err := systemctlRunner(ctx, "restart", unit); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl restart %s failed: %s", unit, strings.TrimSpace(out)),
		}
	}

	// Read the post-restart state so the UI can render immediately.
	post := probeService(ctx, p.Name)
	return serviceRestartResponse{
		Name:      post.Name,
		Active:    post.Active,
		LoadState: post.LoadState,
	}, nil
}

// scheduleServiceRestart hands the restart to systemd as a transient timer
// and returns before it runs — the same way system.reboot schedules itself.
// PID 1 owns the timer, so the restart still happens when it takes down the
// agent or the panel that asked for it. The fixed unit name makes a second
// request while one is pending fail instead of queueing another restart;
// --collect frees the name once the restart has run. (Same deadlock family as
// the panel-cert self-restart in ssl_panel_issue.go.)
func scheduleServiceRestart(ctx context.Context, name, unit string) (any, error) {
	out, err := execCommandContext(ctx, "systemd-run", "--quiet", "--collect",
		"--unit=jabali-service-restart-"+name,
		fmt.Sprintf("--on-active=%ds", deferredRestartDelaySeconds),
		"--timer-property=AccuracySec=100ms",
		"systemctl", "restart", unit).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "already loaded") {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeAlreadyExists,
				Message: fmt.Sprintf("a restart of %s is already scheduled", name),
			}
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("schedule restart of %s: %v: %s", unit, err, msg),
		}
	}
	pre := probeService(ctx, name)
	return serviceRestartResponse{
		Name:         pre.Name,
		Active:       pre.Active,
		LoadState:    pre.LoadState,
		Scheduled:    true,
		DelaySeconds: deferredRestartDelaySeconds,
	}, nil
}

// isAllowedService reports whether name is in the combined allow-list.
// Kept local so the allow-list stays authoritative in service_list.go.
func isAllowedService(name string) bool {
	for _, s := range AllowedServices() {
		if s == name {
			return true
		}
	}
	return false
}

func init() {
	Default.Register("service.restart", serviceRestartHandler)
}
