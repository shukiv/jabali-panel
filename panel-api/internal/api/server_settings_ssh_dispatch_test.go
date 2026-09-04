package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// This file characterizes the exact SSH agent wire the REST PATCH dispatches:
// system.set_ssh_config (sshd port + password-auth, touched-based re-sync) and
// system.set_ssh_sandbox_mode (shell-sandbox mode + default image, value-diff).
// Every assertion was written and run GREEN against the pre-refactor handler to
// lock the wire, so the JAB-295 settingsops extraction is proven byte-identical:
// same verbs, same params, and — critically — the two distinct detection
// semantics (config re-syncs on any touch even when unchanged; sandbox fires
// only on a real value change) are preserved.

// --- system.set_ssh_config: touched-based re-sync ---

func TestServerSettingsPatch_SSHConfig_Wire(t *testing.T) {
	t.Run("port change fires with full param set", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, SSHPasswordAuth: false, SSHUserPasswordAuth: false}
		m := patchSettings(t, existing, map[string]any{"ssh_port": 2222}, "system.set_ssh_config")
		require.Equal(t, map[string]any{
			"port":               float64(2222),
			"password_auth":      false,
			"user_password_auth": false,
		}, moduleCallParams(t, m, "system.set_ssh_config"))
	})
	t.Run("password_auth change fires", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, SSHPasswordAuth: false, SSHUserPasswordAuth: false}
		m := patchSettings(t, existing, map[string]any{"ssh_password_auth": true}, "system.set_ssh_config")
		require.Equal(t, map[string]any{
			"port":               float64(22),
			"password_auth":      true,
			"user_password_auth": false,
		}, moduleCallParams(t, m, "system.set_ssh_config"))
	})
	t.Run("user_password_auth change fires", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, SSHPasswordAuth: false, SSHUserPasswordAuth: false}
		m := patchSettings(t, existing, map[string]any{"ssh_user_password_auth": true}, "system.set_ssh_config")
		require.Equal(t, map[string]any{
			"port":               float64(22),
			"password_auth":      false,
			"user_password_auth": true,
		}, moduleCallParams(t, m, "system.set_ssh_config"))
	})
	// The re-sync semantic: an operator re-saving an unchanged ssh field still
	// re-applies the file, so a drifted sshd_config can be healed by re-submitting.
	t.Run("touched but unchanged still re-syncs", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, SSHPasswordAuth: false, SSHUserPasswordAuth: false}
		m := patchSettings(t, existing, map[string]any{"ssh_port": 22}, "system.set_ssh_config")
		require.Equal(t, map[string]any{
			"port":               float64(22),
			"password_auth":      false,
			"user_password_auth": false,
		}, moduleCallParams(t, m, "system.set_ssh_config"))
	})
	t.Run("untouched patch does not fire", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, SSHPasswordAuth: false, SSHUserPasswordAuth: false}
		m := patchSettings(t, existing, map[string]any{"postgres_enabled": false})
		requireNoCommand(t, m, "system.set_ssh_config")
	})
}

// --- system.set_ssh_sandbox_mode: value-diff, no re-sync ---

func TestServerSettingsPatch_SSHSandbox_Wire(t *testing.T) {
	t.Run("mode change fires with full param set", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, SSHSandboxMode: "bubblewrap", DefaultNspawnImageVersion: "debian-12"}
		m := patchSettings(t, existing, map[string]any{"ssh_sandbox_mode": "nspawn"}, "system.set_ssh_sandbox_mode")
		require.Equal(t, map[string]any{
			"mode":          "nspawn",
			"default_image": "debian-12",
		}, moduleCallParams(t, m, "system.set_ssh_sandbox_mode"))
	})
	t.Run("image change alone fires", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, SSHSandboxMode: "nspawn", DefaultNspawnImageVersion: "debian-12"}
		m := patchSettings(t, existing, map[string]any{"default_nspawn_image_version": "debian-13"}, "system.set_ssh_sandbox_mode")
		require.Equal(t, map[string]any{
			"mode":          "nspawn",
			"default_image": "debian-13",
		}, moduleCallParams(t, m, "system.set_ssh_sandbox_mode"))
	})
	// The value-diff semantic: re-submitting the same sandbox mode does NOT fire
	// (unlike ssh_config, which re-syncs on any touch).
	t.Run("same value re-submitted does not fire", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, SSHSandboxMode: "nspawn", DefaultNspawnImageVersion: "debian-12"}
		m := patchSettings(t, existing, map[string]any{"ssh_sandbox_mode": "nspawn"})
		requireNoCommand(t, m, "system.set_ssh_sandbox_mode")
	})
	t.Run("untouched patch does not fire", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, SSHSandboxMode: "nspawn", DefaultNspawnImageVersion: "debian-12"}
		m := patchSettings(t, existing, map[string]any{"postgres_enabled": false})
		requireNoCommand(t, m, "system.set_ssh_sandbox_mode")
	})
}
