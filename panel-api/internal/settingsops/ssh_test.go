package settingsops

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// --- system.set_ssh_config: touched-based re-sync ---

func TestSSHEffects_Config_Wire(t *testing.T) {
	before := &models.ServerSettings{SSHPort: 22, SSHPasswordAuth: false, SSHUserPasswordAuth: false}
	after := &models.ServerSettings{SSHPort: 2222, SSHPasswordAuth: true, SSHUserPasswordAuth: false}
	plan := SSHEffects(before, after, SSHTouched{Config: true})

	require.Equal(t, Changed, plan.Config.Kind)
	require.Equal(t, &AgentCall{
		Method: "system.set_ssh_config",
		Params: map[string]any{
			"port":               uint16(2222),
			"password_auth":      true,
			"user_password_auth": false,
		},
		Timeout: 30 * time.Second,
	}, plan.Config.Call)
	require.True(t, plan.Config.RevertOnFailure, "a real config change must revert on failure (AC5)")
	require.Nil(t, plan.Config.Rollback, "SSH compensation is a repo revert, not an agent Rollback")
}

func TestSSHEffects_Config_ReapplyWhenTouchedButUnchanged(t *testing.T) {
	s := &models.ServerSettings{SSHPort: 22, SSHPasswordAuth: true, SSHUserPasswordAuth: false}
	plan := SSHEffects(s, s, SSHTouched{Config: true})

	// Touched + unchanged still dispatches (re-sync a drifted sshd_config)...
	require.Equal(t, Reapply, plan.Config.Kind)
	require.NotNil(t, plan.Config.Call)
	// ...but there is nothing to revert (before == after), so it is not flagged.
	require.False(t, plan.Config.RevertOnFailure, "a no-value-change re-sync has nothing to revert")
}

func TestSSHEffects_Config_NoOpWhenUntouched(t *testing.T) {
	before := &models.ServerSettings{SSHPort: 22}
	after := &models.ServerSettings{SSHPort: 2222} // value differs, but not touched
	plan := SSHEffects(before, after, SSHTouched{Config: false})

	require.Equal(t, NoOp, plan.Config.Kind)
	require.Nil(t, plan.Config.Call)
	require.False(t, plan.Config.RevertOnFailure)
}

// --- system.set_ssh_sandbox_mode: value-diff, no re-sync ---

func TestSSHEffects_Sandbox_Wire(t *testing.T) {
	before := &models.ServerSettings{SSHSandboxMode: "bubblewrap", DefaultNspawnImageVersion: "debian-12"}
	after := &models.ServerSettings{SSHSandboxMode: "nspawn", DefaultNspawnImageVersion: "debian-12"}
	plan := SSHEffects(before, after, SSHTouched{})

	require.Equal(t, Changed, plan.Sandbox.Kind)
	require.Equal(t, &AgentCall{
		Method: "system.set_ssh_sandbox_mode",
		Params: map[string]any{
			"mode":          "nspawn",
			"default_image": "debian-12",
		},
		Timeout: 30 * time.Second,
	}, plan.Sandbox.Call)
	require.True(t, plan.Sandbox.RevertOnFailure, "a sandbox mode change must revert on failure (AC5)")
}

func TestSSHEffects_Sandbox_ImageOnlyChangeFires(t *testing.T) {
	before := &models.ServerSettings{SSHSandboxMode: "nspawn", DefaultNspawnImageVersion: "debian-12"}
	after := &models.ServerSettings{SSHSandboxMode: "nspawn", DefaultNspawnImageVersion: "debian-13"}
	plan := SSHEffects(before, after, SSHTouched{})

	require.Equal(t, Changed, plan.Sandbox.Kind)
	require.Equal(t, "debian-13", plan.Sandbox.Call.Params["default_image"])
	require.True(t, plan.Sandbox.RevertOnFailure)
}

func TestSSHEffects_Sandbox_NoOpWhenUnchanged(t *testing.T) {
	s := &models.ServerSettings{SSHSandboxMode: "nspawn", DefaultNspawnImageVersion: "debian-12"}
	plan := SSHEffects(s, s, SSHTouched{})

	require.Equal(t, NoOp, plan.Sandbox.Kind)
	require.Nil(t, plan.Sandbox.Call)
	require.False(t, plan.Sandbox.RevertOnFailure)
}

// TestSSHEffects_TwoDetectionModesDiffer pins the AC3 distinction: with the SAME
// before/after (a value that is unchanged), the touched config re-syncs while the
// value-diff sandbox stays silent. The two verbs must not collapse to one rule.
func TestSSHEffects_TwoDetectionModesDiffer(t *testing.T) {
	s := &models.ServerSettings{
		SSHPort: 22, SSHPasswordAuth: true,
		SSHSandboxMode: "nspawn", DefaultNspawnImageVersion: "debian-12",
	}
	plan := SSHEffects(s, s, SSHTouched{Config: true})

	require.NotNil(t, plan.Config.Call, "touched config re-syncs even when unchanged")
	require.Equal(t, Reapply, plan.Config.Kind)
	require.Nil(t, plan.Sandbox.Call, "value-diff sandbox does not fire when unchanged")
	require.Equal(t, NoOp, plan.Sandbox.Kind)
}
