package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// docker_tenant_set.go — admin opt-in "Enable Docker Apps for users".
//
// Enabling tenant docker is a heavy, host-wide operation (daemon userns-remap +
// dockerd restart + per-app chown/restart), already implemented and tested as
// `jabali docker enable-tenant`. Rather than reimplement it, this verb shells
// out to that CLI on enable. Disabling does NOT revert userns-remap (that is
// destructive and has no CLI); it just removes the host tenant flag so the
// panel's tenant docker surface is gated off again.
//
//	docker.tenant_set {enabled:bool} -> {ok, enabled}

// dockerTenantFlagPath mirrors panel-api's defaultTenantDockerFlag — its
// presence is what gates the tenant docker API.
const dockerTenantFlagPath = "/etc/jabali/docker-tenant-enabled"

// dockerTenantExec runs `jabali docker enable-tenant --yes`. A var for tests.
var dockerTenantExec = func(ctx context.Context) error {
	bin := "jabali"
	if p, err := exec.LookPath("jabali"); err == nil {
		bin = p
	} else if _, serr := os.Stat("/usr/local/bin/jabali"); serr == nil {
		bin = "/usr/local/bin/jabali"
	}
	out, err := execCommandContext(ctx, bin, "docker", "enable-tenant", "--yes").CombinedOutput()
	if err != nil {
		return fmt.Errorf("jabali docker enable-tenant: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

type dockerTenantSetParams struct {
	Enabled bool `json:"enabled"`
}

type dockerTenantSetResponse struct {
	Ok      bool `json:"ok"`
	Enabled bool `json:"enabled"`
}

func dockerTenantSetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerTenantSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.Enabled {
		// Idempotent: enable-tenant itself no-ops when the flag already exists.
		if err := dockerTenantExec(ctx); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
		}
		return dockerTenantSetResponse{Ok: true, Enabled: true}, nil
	}
	// Disable: remove the host flag so the tenant API gate closes. Leave
	// userns-remap in place (reverting it is destructive — operator-only).
	if err := os.Remove(dockerTenantFlagPath); err != nil && !os.IsNotExist(err) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove tenant flag: %v", err)}
	}
	return dockerTenantSetResponse{Ok: true, Enabled: false}, nil
}

func init() {
	Default.Register("docker.tenant_set", dockerTenantSetHandler)
}
