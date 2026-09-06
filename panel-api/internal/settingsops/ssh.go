package settingsops

import (
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// sshApplyTimeout is the per-call agent timeout for both SSH settings verbs
// (matches the pre-extraction REST goroutine budget of 30s).
const sshApplyTimeout = 30 * time.Second

// SSHTouched reports whether the adapter merged any ssh-config field this
// request. Only the config verb is touched-based (a re-save re-syncs a drifted
// sshd_config even when the value is unchanged — the operator-driven counterpart
// to the boot-time reconcile in serve.go); the sandbox verb is pure value-diff
// and needs no touched signal. Kept as adapter input (not re-derived here) so the
// module preserves the exact re-sync semantics of the handler.
type SSHTouched struct {
	Config bool
}

// SSHPlan is the declarative plan for the SSH settings family: at most one
// dispatch per verb.
//
//   - Config  → system.set_ssh_config (sshd port + password-auth). Touched-based
//     re-sync; the agent runs `sshd -t` and reloads, rolling the file back on a
//     bad config.
//   - Sandbox → system.set_ssh_sandbox_mode (shell-sandbox mode + default image).
//     Value-diff; the connect wrapper reads the files on every exec, so no reload.
//
// Both carry RevertOnFailure when the effect is a real value change (Kind ==
// Changed): a failed apply must not leave the persisted row claiming an SSH
// security state the host never accepted (JAB-295 AC5). The agent rolls the file
// back on `sshd -t` failure, but the panel DB would otherwise keep the new value,
// so the adapter reverts the matching fields to their pre-merge values. A
// touched-but-unchanged config re-sync (Kind == Reapply) has nothing to revert —
// before == after — so it is left unflagged.
type SSHPlan struct {
	Config  Effect
	Sandbox Effect
}

// SSHEffects derives the SSH settings effect plan from the validated before/after
// settings and whether any ssh-config field was touched. before is the pre-merge
// snapshot (old values); after is the merged settings that will be persisted.
func SSHEffects(before, after *models.ServerSettings, touched SSHTouched) SSHPlan {
	cfg := effect(touched.Config, "system.set_ssh_config",
		sshConfigParams(before), sshConfigParams(after), sshApplyTimeout)
	cfg.RevertOnFailure = cfg.Kind == Changed

	sandbox := diffEffect("system.set_ssh_sandbox_mode",
		sshSandboxParams(before), sshSandboxParams(after), sshApplyTimeout)
	sandbox.RevertOnFailure = sandbox.Kind == Changed

	return SSHPlan{Config: cfg, Sandbox: sandbox}
}

// sshConfigParams is the exact system.set_ssh_config wire (3 keys). This is the
// single source the REST handler previously copied verbatim.
func sshConfigParams(s *models.ServerSettings) map[string]any {
	return map[string]any{
		"port":               s.SSHPort,
		"password_auth":      s.SSHPasswordAuth,
		"user_password_auth": s.SSHUserPasswordAuth,
	}
}

// sshSandboxParams is the exact system.set_ssh_sandbox_mode wire (2 keys).
func sshSandboxParams(s *models.ServerSettings) map[string]any {
	return map[string]any{
		"mode":          s.SSHSandboxMode,
		"default_image": s.DefaultNspawnImageVersion,
	}
}
