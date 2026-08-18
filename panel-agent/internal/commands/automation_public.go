package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #1161: opt-in exposure of the Automation API on :443.
//
// The panel-hostname :443 vhost (install.sh GH#135 block) `include`s this
// snippet at server scope. Default: empty (the API is :8443-only). When the
// admin enables server_settings.automation_api_public_enabled, the reconciler
// calls nginx.automation_public_set{enabled:true} and the agent writes the
// location that proxies /api/v1/automation/ to the panel socket; disabling
// writes it back to empty.
//
// Only /api/v1/automation/ is exposed — every route there is behind
// RequireAutomationHMAC (canonical string METHOD\nPATH\nts\nsha256(BODY), no
// host/port, so :443 validates exactly as :8443). The internal, unauthenticated
// endpoints (/api/v1/internal/, the malware event) are NOT proxied here; they
// stay :8443-only where the 8443 vhost's 404 guards live. Mirrors the GH#860
// catch-all include toggle (convergeCatchall).
// var (not const) so tests can redirect it to a temp dir, mirroring
// catchallConfPath.
var automation443ConfPath = "/etc/nginx/snippets/jabali-automation-443.conf"

// automation443OnConf is a server-scope location{} pulled into the hostname
// :443 vhost via include. proxy_pass has NO URI part so the full request path
// reaches panel-api unchanged; the 3600s timeouts cover the sync-long
// automation calls (backups.create) that would otherwise 502.
const automation443OnConf = `# Managed by jabali-agent (nginx.automation_public_set) — do not edit.
# Automation API on :443 is ENABLED (Server Settings). Only the HMAC-gated
# /api/v1/automation/ tree; internal endpoints stay :8443-only.
location ^~ /api/v1/automation/ {
    proxy_pass http://unix:/run/jabali-panel/api.sock;
    proxy_set_header Host $http_host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header X-Forwarded-Host $http_host;
    proxy_set_header X-Forwarded-Port $server_port;
    proxy_http_version 1.1;
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
}
`

// automation443OffConf MUST match, byte-for-byte, the seed written by
// install.sh (install_default_vhost) and by the repair self-heal
// (fixAutomation443Include) so the OFF state is a true no-op — otherwise the
// first reconcile after deploy would rewrite the file and pointlessly reload
// nginx on every box. A comment-only file = an empty include (no directives).
const automation443OffConf = "# Managed by jabali-agent (nginx.automation_public_set). Empty = API on :8443 only.\n"

type automationPublicSetParams struct {
	Enabled *bool `json:"enabled"`
}

type automationPublicSetResponse struct {
	Ok      bool `json:"ok"`
	Changed bool `json:"changed"`
	Enabled bool `json:"enabled"`
}

// automationPublicSetHandler converges the automation-on-443 include to the
// requested toggle state and reloads nginx if it changed.
func automationPublicSetHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p automationPublicSetParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid params: %v", err),
		}
	}
	changed, err := convergeAutomation443(ctx, p.Enabled)
	if err != nil {
		return nil, err
	}
	return automationPublicSetResponse{
		Ok:      true,
		Changed: changed,
		Enabled: p.Enabled != nil && *p.Enabled,
	}, nil
}

// convergeAutomation443 writes the automation-on-443 include to match the
// opt-in toggle and reloads nginx only when the file actually changed. A nil
// enabled (panel predates the toggle) is a no-op. Mirrors convergeCatchall.
func convergeAutomation443(ctx context.Context, enabled *bool) (bool, error) {
	if enabled == nil {
		return false, nil
	}
	desired := automation443OffConf
	if *enabled {
		desired = automation443OnConf
	}

	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	if existing, err := os.ReadFile(automation443ConfPath); err == nil && string(existing) == desired {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(automation443ConfPath), 0o755); err != nil {
		return false, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir %s: %v", filepath.Dir(automation443ConfPath), err),
		}
	}
	if err := writeFileAtomic(automation443ConfPath, []byte(desired), 0o644); err != nil {
		return false, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write %s: %v", automation443ConfPath, err),
		}
	}
	// The include only exists inside jabali-default.conf's hostname vhost on
	// boxes whose install.sh has the GH #1161 revision OR whose repair has
	// self-healed the include line; reloading is harmless when it hasn't (the
	// file just sits unreferenced).
	if err := nginxTestAndReload(ctx); err != nil {
		return true, err
	}
	return true, nil
}

func init() {
	Default.Register("nginx.automation_public_set", automationPublicSetHandler)
}
