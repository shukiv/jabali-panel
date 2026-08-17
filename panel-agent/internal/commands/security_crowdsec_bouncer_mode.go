package commands

// security.crowdsec.bouncer.mode.apply — rewrite the MODE= line in
// /etc/crowdsec/bouncers/crowdsec-nginx-bouncer.conf and reload
// nginx so the bouncer picks up the new value on next request.
//
// Two valid modes:
//
//   live   per-request LAPI lookup. Instant ban application but the
//          bouncer hits LAPI (SQLite) on every HTTP request, which
//          drives crowdsec CPU to ~23% sustained on small VMs.
//
//   stream bouncer caches the full decision set in nginx Lua
//          shared_dict and refreshes every UPDATE_FREQUENCY (60s).
//          Lookups become O(1) in-process and LAPI hits collapse to
//          one poll/minute. ~10% CPU on the same load. Up-to-60s lag
//          for newly-issued bans; bounded to L7 because the firewall
//          bouncer already polls at 60s so the L3 ban arrives at the
//          same time anyway.
//
// We do NOT touch any other key in the conf file. The full conf is
// owned by install.sh — this verb only flips MODE=. If the line is
// missing (operator hand-edit / partial install) we surface
// codeNotFound rather than silently appending; install.sh re-run
// fixes it.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

var bouncerModeLineRE = regexp.MustCompile(`(?m)^MODE=.*$`)

type bouncerModeApplyParams struct {
	Mode string `json:"mode"`
}

type bouncerModeApplyResponse struct {
	Mode    string `json:"mode"`
	Applied bool   `json:"applied"`
}

func bouncerModeApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p bouncerModeApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse: %v", err),
		}
	}
	switch p.Mode {
	case "live", "stream":
	default:
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "mode must be live|stream",
		}
	}

	raw, err := os.ReadFile(bouncerConfPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeNotFound,
				Message: bouncerConfPath + " not found — run jabali update to reinstall crowdsec-nginx-bouncer",
			}
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("read %s: %v", bouncerConfPath, err),
		}
	}

	cur := string(raw)
	if !bouncerModeLineRE.MatchString(cur) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: "MODE= line missing from " + bouncerConfPath + " — run jabali update",
		}
	}

	desiredLine := "MODE=" + p.Mode
	updated := bouncerModeLineRE.ReplaceAllString(cur, desiredLine)
	if updated == cur {
		return bouncerModeApplyResponse{Mode: p.Mode, Applied: false}, nil
	}

	tmp, err := os.CreateTemp("/etc/crowdsec/bouncers", ".jabali-bouncer-mode-*.tmp")
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(updated); err != nil {
		tmp.Close()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := tmp.Close(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if err := os.Rename(tmpPath, bouncerConfPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	// nginx-lua gotcha: `init_by_lua_block` runs ONCE per nginx
	// master start, NOT on `nginx -s reload`. The crowdsec bouncer
	// loads MODE into `runtime.conf` via cs.init() inside
	// init_by_lua_block at /etc/nginx/conf.d/crowdsec_nginx.conf:6.
	// Reload re-forks workers from the existing master so Lua
	// globals (incl. runtime.conf["MODE"]) are preserved — workers
	// still see the OLD mode regardless of what's on disk.
	//
	// Use restart, not reload, so the master re-execs and
	// init_by_lua re-runs against the updated bouncer conf.
	// Downside: ~100ms of "connection refused" during the
	// restart window. Acceptable trade-off — a mode flip is a
	// manual operator action, not a hot-path event. Alternative
	// (per-request file read in lua) would require patching the
	// upstream crowdsec-nginx-bouncer; out of scope here.
	//
	// Caught 2026-06-04 on puzzle.linux-hosting.net: agent wrote
	// MODE=live to disk + reloaded nginx + DB persisted live + UI
	// said "applied" — but bouncer kept running in stream mode.
	if out, err := execCommandContext(ctx, "nginx", "-t").CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "nginx -t failed after writing " + bouncerConfPath + ": " + strings.TrimSpace(string(out)),
		}
	}
	if out, err := execCommandContext(ctx, "systemctl", "restart", "nginx").CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "systemctl restart nginx failed: " + strings.TrimSpace(string(out)),
		}
	}

	return bouncerModeApplyResponse{Mode: p.Mode, Applied: true}, nil
}

func init() {
	Default.Register("security.crowdsec.bouncer.mode.apply", bouncerModeApplyHandler)
}
