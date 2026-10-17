package settingsops

import (
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// hostnameCallTimeout bounds each hostname-change agent call (the REST
// handler's and the CLI's timeout before this module owned them).
const hostnameCallTimeout = 30 * time.Second

// HostnamePlan is what a panel hostname change applies on the host. The REST
// PATCH, `jabali settings set hostname=` and the free-hostname claim all
// execute this one plan, each under its own policy (REST and the claim
// detached and best-effort, the CLI synchronously); before it, the CLI and the
// claim skipped the landing vhost and none of them passed the public IP.
//
// The two calls are independent: the DB row is the truth, so the landing
// vhost follows it even when set_hostname fails.
type HostnamePlan struct {
	// SetHostname: hostnamectl, /etc/hosts (the new name → public IPv4, no
	// 127.0.1.1 line) and config.toml's [server] hostname.
	SetHostname Effect
	// LandingRehost: the :443 landing vhost's server_name (JAB-389).
	LandingRehost Effect
}

// HostnameEffects derives the plan from the before/after rows. No change, or
// a cleared hostname (validation rejects it before here anyway), is a no-op.
func HostnameEffects(before, after *models.ServerSettings) HostnamePlan {
	if after.Hostname == "" || after.Hostname == before.Hostname {
		return HostnamePlan{}
	}
	return HostnamePlan{
		SetHostname: Effect{Kind: Changed, Call: &AgentCall{
			Method:  "system.set_hostname",
			Params:  map[string]any{"hostname": after.Hostname, "public_ipv4": after.PublicIPv4},
			Timeout: hostnameCallTimeout,
		}},
		LandingRehost: Effect{Kind: Changed, Call: &AgentCall{
			Method:  "nginx.panel_landing_rehost",
			Params:  map[string]any{"hostname": after.Hostname},
			Timeout: hostnameCallTimeout,
		}},
	}
}

// Calls returns the plan's agent calls in dispatch order (empty for a no-op).
func (p HostnamePlan) Calls() []AgentCall {
	var calls []AgentCall
	for _, e := range []Effect{p.SetHostname, p.LandingRehost} {
		if e.Call != nil {
			calls = append(calls, *e.Call)
		}
	}
	return calls
}
