package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ServiceStatus describes a single systemd service's state.
type ServiceStatus struct {
	Name   string `json:"name"`
	Active string `json:"active"` // "active", "inactive", "failed", "unknown"
	// LoadState surfaces whether the unit can be controlled:
	//   "loaded" — normal; restartable.
	//   "masked" — unit is deliberately blocked; restart would fail.
	//   "not-found" — unit doesn't exist (filtered out upstream).
	//   "error" — systemd couldn't parse the unit file.
	LoadState string `json:"load_state"`
	// Enabled reports `systemctl is-enabled` output. Common values:
	//   "enabled" | "disabled" | "static" | "alias" | "masked" |
	//   "enabled-runtime" | "indirect" | "generated" | "transient"
	// UIs typically render "enabled"/"enabled-runtime"/"static" as
	// "Enabled" and "disabled"/"indirect" as "Disabled".
	Enabled string `json:"enabled"`
}

// ServiceListResponse is the payload for service.list.
type ServiceListResponse struct {
	Services []ServiceStatus `json:"services"`
}

// BaseAllowedServices is the fixed set of services the agent will report
// on. This is a security boundary: callers can't probe arbitrary
// systemd units.
//
// Global `php<v>-fpm.service` units are deliberately NOT listed. Per
// ADR-0025 (per-user slices, shipped 2026-04-18) install.sh masks every
// global FPM service on every host — the real workers run as
// `jabali-fpm@<user>.service` inside per-user slices. Listing the masked
// global units just showed noise on the dashboard (greyed-out Restart
// button beside a service that's architecturally guaranteed to be dead).
var BaseAllowedServices = []string{
	"nginx",
	"mariadb",
	"postgresql", // M37 PG parity (ADR-0091). Optional — hidden unless active.
	"redis-server",
	// Mail stack (M6). The jabali-branded names are what install.sh
	// actually creates — the upstream's own "stalwart-mail.service" is
	// never used; we ship a vendored unit at
	// install/systemd/jabali-stalwart.service so rollback + upgrade
	// remain under the panel's control. Same for the Bulwark webmail.
	"jabali-stalwart",
	"jabali-webmail",
	// Identity (M20).
	"jabali-kratos",
	"pdns", // PowerDNS, not BIND — see ADR-0003
	// Docker engine — present only on hosts running the M48 docker-app
	// marketplace. Not jabali-managed beyond the app lifecycle, but the
	// operator expects to see + control it when it's installed. The
	// LoadState "not-found" filter hides it on hosts without Docker, so
	// it's listed unconditionally rather than in optionalServices (which
	// would also hide a stopped-but-installed engine the operator may
	// want to Start).
	"docker",
	"jabali-panel",
	"jabali-agent",
	"ssh", // Debian unit name for OpenSSH is ssh.service, not sshd.service
	// NOTE: the system cron.service is intentionally NOT listed. jabali cron
	// runs as per-user systemd-user timers (M8), never the stock cron daemon,
	// so showing cron.service here was misleading — it sat OFF and "Start" did
	// nothing for jabali cron jobs (GH #296). Cron jobs are managed in the Cron
	// UI, not via a service toggle.
}

// optionalServices are included in the list only when active. Services that
// jabali doesn't require but may co-exist with (e.g. postgresql installed by
// another app) are hidden when inactive to avoid confusing operators.
var optionalServices = map[string]bool{
	"postgresql":     true,
	"jabali-webmail": true,
}

// AllowedServices returns the agent-probe allow-list.
func AllowedServices() []string {
	out := make([]string, 0, len(BaseAllowedServices))
	out = append(out, BaseAllowedServices...)
	return out
}

// systemctlRunner abstracts exec.Command for testing.
var systemctlRunner = realSystemctl

func realSystemctl(ctx context.Context, args ...string) (string, error) {
	cmd := execCommandContext(ctx, "systemctl", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func serviceListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	names := AllowedServices()
	services := make([]ServiceStatus, 0, len(names))
	for _, svc := range names {
		status := probeService(ctx, svc)
		// Skip services that aren't installed on this host — the
		// dashboard should reflect reality, not a wishlist. LoadState
		// "not-found" means systemd can't find the unit file. "masked"
		// means the unit is deliberately blocked from starting (install.sh
		// masks global php-fpm services per ADR-0025). Either way, showing
		// it to the operator with a greyed-out Restart button is noise.
		if status.LoadState == "not-found" || status.LoadState == "" || status.LoadState == "masked" {
			continue
		}
		// Optional services (e.g. postgresql) are hidden unless active.
		// They may be installed by co-resident apps that jabali doesn't
		// manage; surfacing them as "inactive" just confuses operators.
		if optionalServices[svc] && status.Active != "active" {
			continue
		}
		services = append(services, status)
	}
	return ServiceListResponse{Services: services}, nil
}

func probeService(ctx context.Context, name string) ServiceStatus {
	// Validate service name to prevent injection. Only allow
	// alphanumeric, hyphen, dot, and @ (for template units).
	for _, c := range name {
		if !isServiceNameChar(c) {
			return ServiceStatus{Name: name, Active: "unknown"}
		}
	}

	unit := fmt.Sprintf("%s.service", name)

	// LoadState tells us whether the unit file exists on disk
	// ("loaded" | "masked" | "not-found" | "error"). We use it both
	// to filter out uninstalled services and to let the UI decide
	// whether a restart button makes sense (masked => no).
	loadState, _ := systemctlRunner(ctx, "show", "-p", "LoadState", "--value", unit)
	loadState = strings.TrimSpace(loadState)

	// is-enabled exits non-zero for "disabled" / "static" / etc. — the
	// state word is in stdout regardless, so we accept the error.
	enabledOut, _ := systemctlRunner(ctx, "is-enabled", unit)
	enabled := strings.TrimSpace(enabledOut)
	// Multi-line output (e.g. "alias" with target) — take the first token.
	if nl := strings.IndexByte(enabled, '\n'); nl >= 0 {
		enabled = enabled[:nl]
	}

	out, err := systemctlRunner(ctx, "is-active", unit)
	if err != nil {
		// systemctl exits non-zero for inactive/failed; the output still
		// contains the state word.
		if out == "" {
			return ServiceStatus{Name: name, Active: "unknown", LoadState: loadState, Enabled: enabled}
		}
	}
	state := strings.TrimSpace(out)
	// Socket-activated units (ssh.socket on Ubuntu 24.04 / Debian 13+)
	// leave the .service "inactive" until a connection arrives, even
	// though the listener is up and the service is fully functional. If
	// the matching .socket is active, report the service as active so a
	// working SSH isn't shown as "stopped" on the dashboard (GH#133).
	if state != "active" {
		if sockOut, _ := systemctlRunner(ctx, "is-active", name+".socket"); strings.TrimSpace(sockOut) == "active" {
			state = "active"
		}
	}
	switch state {
	case "active", "inactive", "failed", "activating", "deactivating":
		return ServiceStatus{Name: name, Active: state, LoadState: loadState, Enabled: enabled}
	default:
		return ServiceStatus{Name: name, Active: "unknown", LoadState: loadState, Enabled: enabled}
	}
}

func isServiceNameChar(c rune) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '.' || c == '@' || c == '_'
}

func init() {
	Default.Register("service.list", serviceListHandler)
}
