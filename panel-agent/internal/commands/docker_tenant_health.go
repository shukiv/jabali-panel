package commands

import (
	"context"
	"encoding/json"
	"os"
	"strings"
)

// tenantDockerFlagPath mirrors panel-api's defaultTenantDockerFlag — the marker
// `jabali docker enable-tenant` writes once userns-remap is live.
const tenantDockerFlagPath = "/etc/jabali/docker-tenant-enabled"

type dockerTenantHealthResp struct {
	UsernsActive bool `json:"userns_active"`
	FlagPresent  bool `json:"flag_present"`
	FlagRemoved  bool `json:"flag_removed"`
}

// dockerTenantHealthHandler keeps the tenant-Docker gate honest (Gitea #512).
// panel-api's requireTenantDockerEnabled only stat()s the flag file, so if the
// live daemon lost userns-remap out of band the gate would still admit tenants
// to a daemon where container-root == host-root. This checks the LIVE daemon
// and, when the flag is set but userns is NOT active, removes the flag so the
// gate fails closed. It acts ONLY on a successful `docker info` — a transient
// daemon error must never wrongly disable tenant Docker (so we leave the flag
// untouched in that case). #506 covers the install/update path; this covers the
// out-of-band drift between updates.
func dockerTenantHealthHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	flagPresent := false
	if _, err := os.Stat(tenantDockerFlagPath); err == nil {
		flagPresent = true
	}
	// Not enabled → nothing to verify.
	if !flagPresent {
		return dockerTenantHealthResp{}, nil
	}
	out, err := execCommandContext(ctx, "docker", "info", "--format", "{{json .SecurityOptions}}").Output()
	if err != nil {
		// Daemon unreachable / restarting — report present, touch nothing.
		return dockerTenantHealthResp{FlagPresent: true}, nil
	}
	usernsActive := strings.Contains(string(out), "userns")
	resp := dockerTenantHealthResp{UsernsActive: usernsActive, FlagPresent: true}
	if !usernsActive {
		if rmErr := os.Remove(tenantDockerFlagPath); rmErr == nil {
			resp.FlagRemoved = true
			resp.FlagPresent = false
		}
	}
	return resp, nil
}

func init() {
	Default.Register("docker.tenant_health", dockerTenantHealthHandler)
}
