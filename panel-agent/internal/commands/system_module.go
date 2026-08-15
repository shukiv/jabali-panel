package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// system.module.{install,status}
//
// M353 module install-on-enable. Enabling an optional module in Server
// Settings → Modules must INSTALL its packages if they're missing (and start
// the services), not just flip the DB flag. On a --minimal install the flags
// can read On while nothing is installed (the reported "DNS On / pdns
// inactive" bug). panel-api dispatches system.module.install both on a
// false→true toggle and on a convergence pass over enabled-but-missing
// modules; this handler shells to the release-bundled install.sh in its
// targeted `--install-module <key>` mode (install.sh owns the per-module
// install/idempotency + service convergence).
//
// Installs are serialized behind aptMu (shared with php-ext) so a toggle and
// the convergence pass never run apt concurrently and collide on the dpkg
// global lock (the M29 apt-lock scar).

// moduleInstallKeys is the allowlist of module keys the agent will pass to
// install.sh. Never interpolate an arbitrary string into the shell — validate
// against this set first. install.sh independently rejects keys it doesn't yet
// support at runtime (mail/security _die until their env reconstruction lands).
var moduleInstallKeys = map[string]bool{
	"dns":      true,
	"mail":     true,
	"security": true,
	"quota":    true,
	"ftp":      true, // GH #1053: vsftpd, opt-in via server_settings.ftp_enabled
}

// moduleProbe describes how to detect a module's install + active state.
type moduleProbe struct {
	bins    []string // LookPath: any present → installed
	binGlob []string // filepath.Glob: any match → installed
	service string   // systemctl is-active → active ("" → active mirrors installed)
}

// moduleDisableUnits lists the systemd units to stop+disable when a module is
// turned OFF. It stops the daemon that IS the feature — never baseline plumbing
// or network-exposure guardrails:
//   - dns keeps pdns-recursor (it owns 127.0.0.1:53 + /etc/resolv.conf — stopping
//     it would kill the host's own DNS resolution). Only the authoritative
//     pdns server stops.
//   - security keeps ufw (dropping the firewall on a toggle is fail-open),
//     AppArmor and AIDE (unloading live MAC confinement / integrity monitoring
//     from a UI switch is a posture regression with no availability benefit).
//
// Disable never purges packages or data (mailbox rows, zones, DBs are kept);
// re-enabling the module re-starts these units via the idempotent install path.
var moduleDisableUnits = map[string][]string{
	"mail":     {"jabali-stalwart", "jabali-webmail", "jabali-mailhook"},
	"security": {"crowdsec", "crowdsec-firewall-bouncer"},
	"dns":      {"pdns"}, // NEVER pdns-recursor
	"quota":    {},       // filesystem feature — no service to stop
	"ftp":      {"vsftpd"},
}

var moduleProbes = map[string]moduleProbe{
	"dns":      {bins: []string{"pdns_server"}, binGlob: []string{"/usr/sbin/pdns_server"}, service: "pdns"},
	"mail":     {bins: []string{"stalwart"}, binGlob: []string{"/usr/local/bin/stalwart"}, service: "jabali-stalwart"}, // symlink + LookPath; filepath.Glob has no **
	"security": {bins: []string{"cscli", "crowdsec"}, service: "crowdsec"},
	"quota":    {bins: []string{"quota"}, binGlob: []string{"/usr/sbin/quota"}, service: ""}, // tooling, no service
	"ftp":      {bins: []string{"vsftpd"}, binGlob: []string{"/usr/sbin/vsftpd"}, service: "vsftpd"},
}

type moduleStatusRequest struct {
	Key string `json:"key"`
}

type moduleStatusResponse struct {
	Key       string `json:"key"`
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
}

func probeModule(ctx context.Context, key string) moduleStatusResponse {
	resp := moduleStatusResponse{Key: key}
	p := moduleProbes[key]
	for _, b := range p.bins {
		if _, err := exec.LookPath(b); err == nil {
			resp.Installed = true
			break
		}
	}
	if !resp.Installed {
		for _, g := range p.binGlob {
			if m, _ := filepath.Glob(g); len(m) > 0 {
				resp.Installed = true
				break
			}
		}
	}
	if p.service == "" {
		// Tooling module (quota): "active" has no service — mirror installed.
		resp.Active = resp.Installed
		return resp
	}
	if out, err := exec.CommandContext(ctx, "systemctl", "is-active", p.service).Output(); err == nil {
		resp.Active = strings.TrimSpace(string(out)) == "active"
	}
	return resp
}

func systemModuleStatusHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req moduleStatusRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "bad payload: " + err.Error()}
		}
	}
	if !moduleInstallKeys[req.Key] {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("unknown module key %q (want: dns|mail|security|quota)", req.Key),
		}
	}
	return probeModule(ctx, req.Key), nil
}

// systemModuleInstallHandler shells to install.sh --install-module <key>. The
// install runs inside a transient systemd unit (systemd-run --pipe) so apt +
// dpkg + postinst escape the jabali-agent.service mount namespace (PrivateTmp +
// ProtectKernel* would otherwise break apt) — identical to db.postgres.install.
// Idempotent: install.sh no-ops on an already-installed host. Full output is
// logged via CombinedOutput on error; the caller gets a status probe on success.
func systemModuleInstallHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req moduleStatusRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "bad payload: " + err.Error()}
	}
	if !moduleInstallKeys[req.Key] {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("unknown module key %q (want: dns|mail|security|quota)", req.Key),
		}
	}
	if _, err := os.Stat(installShPath); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("install.sh missing at %s", installShPath),
		}
	}

	// Serialize installs across the agent so a toggle-triggered install and the
	// convergence pass don't run apt concurrently (dpkg global lock).
	aptMu.Lock()
	defer aptMu.Unlock()

	// A per-key transient unit name keeps concurrent (post-lock, sequential)
	// installs of different modules from colliding on the unit name.
	unit := "jabali-module-install-" + req.Key
	cmd := exec.CommandContext(ctx, "systemd-run",
		"--pipe", "--wait", "--quiet", "--collect",
		"--unit="+unit,
		"--service-type=oneshot",
		"--", "bash", installShPath, "--install-module", req.Key)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("install.sh --install-module %s failed: %v: %s",
				req.Key, err, strings.TrimSpace(string(out))),
		}
	}
	return probeModule(ctx, req.Key), nil
}

// systemModuleDisableHandler stops + disables a module's services when the
// operator turns the module OFF (data preserved — never purged). Best-effort per
// unit: a missing/absent unit is not an error (the module may be partially
// installed). Idempotent — disabling an already-stopped unit is a no-op. Does NOT
// touch baseline plumbing (pdns-recursor) or security guardrails (ufw/apparmor/
// aide) — see moduleDisableUnits.
func systemModuleDisableHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var req moduleStatusRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "bad payload: " + err.Error()}
	}
	if !moduleInstallKeys[req.Key] {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("unknown module key %q (want: dns|mail|security|quota)", req.Key),
		}
	}
	for _, unit := range moduleDisableUnits[req.Key] {
		// Skip units that don't exist on this host (partial install). `systemctl
		// disable --now` on a missing unit errors; a LoadState probe avoids noise.
		if !unitExists(ctx, unit) {
			continue
		}
		if out, err := exec.CommandContext(ctx, "systemctl", "disable", "--now", unit).CombinedOutput(); err != nil {
			// Fail-soft: log-worthy but don't abort the rest. Return a generic
			// error only if EVERY unit failed would be over-engineering — a
			// single stubborn unit shouldn't strand the others already stopped.
			slog.Warn("system.module.disable: unit stop failed",
				"module", req.Key, "unit", unit, "err", err, "output", strings.TrimSpace(string(out)))
		}
	}
	return probeModule(ctx, req.Key), nil
}

// unitExists reports whether systemd knows the unit (LoadState != not-found).
func unitExists(ctx context.Context, unit string) bool {
	out, err := exec.CommandContext(ctx, "systemctl", "show", "-p", "LoadState", "--value", unit).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "loaded"
}

func init() {
	Default.Register("system.module.status", systemModuleStatusHandler)
	Default.Register("system.module.install", systemModuleInstallHandler)
	Default.Register("system.module.disable", systemModuleDisableHandler)
}
