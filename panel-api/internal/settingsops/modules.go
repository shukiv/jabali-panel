package settingsops

import (
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Optional-module agent timeouts. Pinned here as the single owner so the REST
// handler and the CLI cannot drift (they each carried their own copies before
// JAB-294). Values match the pre-extraction goroutine / callAgentTimeout budgets.
const (
	postgresApplyTimeout = 5 * time.Minute
	moduleInstallTimeout = 10 * time.Minute
	moduleDisableTimeout = 2 * time.Minute
	ftpDisableTimeout    = 2 * time.Minute
	dockerApplyTimeout   = 10 * time.Minute
	dockerTenantTimeout  = 12 * time.Minute
	pythonInstallTimeout = 10 * time.Minute
)

// loopModuleKeys is the generic optional-module set driven by the agent's
// system.module.* verbs, in REST dispatch order. FTP is in the set but takes a
// fail-closed disable verb (see moduleLoopEffect). These flags are REST-only in
// practice (the CLI exposes no setters for them), like nginx cache in JAB-290.
var loopModuleKeys = []string{"dns", "mail", "quota", "security", "ftp"}

// ModuleEffect pairs an optional-module key with its computed transition Effect.
type ModuleEffect struct {
	Key string
	Effect
}

// ModulePlan is the declarative optional-module dispatch plan. Each named field
// is one module's enable/disable/reapply transition; Modules holds the generic
// system.module.* loop in REST dispatch order; FTPReapply re-renders vsftpd.conf
// when FTP config changes while the module stays enabled. Every Effect with a
// non-nil Call is dispatched by the adapter under its own policy (REST detached
// best-effort, CLI synchronous). The module never dispatches.
//
// CLI adapters consume only {Postgres, Docker, DockerTenant, Python} — the
// transitions they have setters for. Modules and FTPReapply are REST-only.
type ModulePlan struct {
	Postgres     Effect
	Docker       Effect // Docker marketplace engine
	DockerTenant Effect // tenant Docker apps (docker.tenant_set) — reverts flag on failed enable
	Python       Effect // enable-only (disable leaves packages)
	Modules      []ModuleEffect
	FTPReapply   Effect
}

// ModuleEffects derives the optional-module effect plan from the validated
// before/after settings. before is the pre-merge snapshot (old flag values);
// after is the merged settings that will be persisted. Detection is by value
// change (before != after) — a toggle, not a re-sync — so the module fully owns
// transition detection here (unlike nginx, which stays touched-based).
func ModuleEffects(before, after *models.ServerSettings) ModulePlan {
	modules := make([]ModuleEffect, 0, len(loopModuleKeys))
	for _, key := range loopModuleKeys {
		modules = append(modules, ModuleEffect{
			Key:    key,
			Effect: moduleLoopEffect(key, moduleFlag(before, key), moduleFlag(after, key)),
		})
	}
	return ModulePlan{
		Postgres: toggleEffect(before.PostgresEnabled, after.PostgresEnabled,
			"db.postgres.install", "db.postgres.disable", postgresApplyTimeout),
		Docker: toggleEffect(before.DockerMarketplaceEnabled, after.DockerMarketplaceEnabled,
			"docker.install", "docker.disable", dockerApplyTimeout),
		DockerTenant: dockerTenantEffect(before.DockerAppsForUsersEnabled, after.DockerAppsForUsersEnabled),
		Python:       pythonEffect(before.PythonAppsEnabled, after.PythonAppsEnabled),
		Modules:      modules,
		FTPReapply:   ftpReapplyEffect(before, after),
	}
}

// moduleFlag reads one generic-loop module's flag off s.
func moduleFlag(s *models.ServerSettings, key string) bool {
	switch key {
	case "dns":
		return s.DNSEnabled
	case "mail":
		return s.MailEnabled
	case "quota":
		return s.QuotaEnabled
	case "security":
		return s.SecurityEnabled
	case "ftp":
		return s.FTPEnabled
	}
	return false
}

// toggleEffect is a plain on/off module: enable dispatches methodOn, disable
// methodOff, both with an empty param map. NoOp when the flag did not change.
func toggleEffect(before, after bool, methodOn, methodOff string, timeout time.Duration) Effect {
	if before == after {
		return Effect{Kind: NoOp}
	}
	method := methodOff
	if after {
		method = methodOn
	}
	return Effect{
		Kind: Changed,
		Call: &AgentCall{Method: method, Params: map[string]any{}, Timeout: timeout},
	}
}

// moduleLoopEffect is one generic system.module.* transition. Enable installs;
// disable stops+disables — EXCEPT ftp, which takes the fail-closed ftp.disable
// verb (JAB-259): a "disabled" FTP that stays reachable is a security hole, so
// it is shut off hard (and reconciled toward closed every tick), never fail-soft.
func moduleLoopEffect(key string, before, after bool) Effect {
	switch {
	case after && !before:
		return Effect{
			Kind: Changed,
			Call: &AgentCall{Method: "system.module.install", Params: map[string]any{"key": key}, Timeout: moduleInstallTimeout},
		}
	case !after && before:
		if key == "ftp" {
			return Effect{
				Kind: Changed,
				Call: &AgentCall{Method: "ftp.disable", Params: map[string]any{}, Timeout: ftpDisableTimeout},
			}
		}
		return Effect{
			Kind: Changed,
			Call: &AgentCall{Method: "system.module.disable", Params: map[string]any{"key": key}, Timeout: moduleDisableTimeout},
		}
	default:
		return Effect{Kind: NoOp}
	}
}

// dockerTenantEffect toggles tenant Docker via docker.tenant_set{enabled}. On the
// enable direction the effect is flagged RevertOnEnableFailure so the adapter
// clears the persisted flag if the host setup fails (GH #272 — unprivileged LXC).
func dockerTenantEffect(before, after bool) Effect {
	if before == after {
		return Effect{Kind: NoOp}
	}
	return Effect{
		Kind:                  Changed,
		Call:                  &AgentCall{Method: "docker.tenant_set", Params: map[string]any{"enabled": after}, Timeout: dockerTenantTimeout},
		RevertOnEnableFailure: after, // revert only on a failed ENABLE
	}
}

// pythonEffect installs the runtime on enable only; disable leaves packages in
// place (there is no disable verb), so a true→false transition is a NoOp.
func pythonEffect(before, after bool) Effect {
	if after && !before {
		return Effect{
			Kind: Changed,
			Call: &AgentCall{Method: "app.python.install_runtime", Params: map[string]any{}, Timeout: pythonInstallTimeout},
		}
	}
	return Effect{Kind: NoOp}
}

// ftpReapplyEffect re-renders vsftpd.conf (via the idempotent system.module.install)
// when an FTP config field changes while the module stays enabled (GH #1053).
// It is a Reapply (module unchanged, config re-synced), distinct from an
// enable/disable toggle — so it does NOT fire when FTP itself is toggling.
func ftpReapplyEffect(before, after *models.ServerSettings) Effect {
	if after.FTPEnabled && before.FTPEnabled && ftpConfigDiffers(before, after) {
		return Effect{
			Kind: Reapply,
			Call: &AgentCall{Method: "system.module.install", Params: map[string]any{"key": "ftp"}, Timeout: moduleInstallTimeout},
		}
	}
	return Effect{Kind: NoOp}
}

// ftpConfigDiffers reports whether any vsftpd.conf-driving field changed.
func ftpConfigDiffers(before, after *models.ServerSettings) bool {
	return after.FTPAllowPlaintext != before.FTPAllowPlaintext ||
		after.FTPPasvAddress != before.FTPPasvAddress ||
		after.FTPMaxClients != before.FTPMaxClients ||
		after.FTPMaxPerIP != before.FTPMaxPerIP ||
		after.FTPLocalMaxRateKBs != before.FTPLocalMaxRateKBs
}
