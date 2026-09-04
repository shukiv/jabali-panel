package api

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/settingsops"
)

// countingRepo wraps mockServerSettingsRepo to count Upserts, so a revert test can
// assert a persisted rollback happened (or, for a re-sync failure, did not).
type countingRepo struct {
	*mockServerSettingsRepo
	upserts int
}

func (c *countingRepo) Upsert(ctx context.Context, s *models.ServerSettings) error {
	c.upserts++
	return c.mockServerSettingsRepo.Upsert(ctx, s)
}

// revertHandler builds a handler over a counting repo whose persisted row is the
// post-merge (after) state — the DB has already been written before the SSH
// dispatch runs. dispatchReverting is exercised SYNCHRONOUSLY (no `go`), so there
// is no goroutine racing the repo (the shared mock is not mutex-guarded).
func revertHandler(after *models.ServerSettings, mockAgent agent.AgentInterface) (*serverSettingsHandler, *countingRepo) {
	cr := &countingRepo{mockServerSettingsRepo: &mockServerSettingsRepo{getResult: after}}
	h := &serverSettingsHandler{cfg: ServerSettingsHandlerConfig{Repo: cr, Agent: mockAgent, Log: slog.Default()}}
	return h, cr
}

// AC5: a failed system.set_ssh_config apply must roll the persisted sshd fields
// back to their pre-merge values, so the DB never claims a security state the
// host rejected (the agent already rolled the file back on `sshd -t` failure).
// Unrelated fields on the row stay at their after values.
func TestSSHConfigRevert_OnFailedApplyRestoresBeforeFields(t *testing.T) {
	before := models.ServerSettings{ID: 1, SSHPort: 22, SSHPasswordAuth: false, SSHUserPasswordAuth: false}
	after := &models.ServerSettings{ID: 1, SSHPort: 2222, SSHPasswordAuth: true, SSHUserPasswordAuth: true, Hostname: "keep.example.com"}

	mockAgent := agent.NewMockClient()
	mockAgent.OnError("system.set_ssh_config", errors.New("sshd -t rejected config"))
	h, cr := revertHandler(after, mockAgent)

	revert := func(cur *models.ServerSettings) {
		cur.SSHPort = before.SSHPort
		cur.SSHPasswordAuth = before.SSHPasswordAuth
		cur.SSHUserPasswordAuth = before.SSHUserPasswordAuth
	}
	call := settingsops.AgentCall{Method: "system.set_ssh_config", Params: map[string]any{"port": uint16(2222)}, Timeout: 30 * time.Second}
	h.dispatchReverting(call, "agent set_ssh_config failed", revert)

	require.Equal(t, 1, cr.upserts, "a failed apply must persist exactly one revert")
	got := cr.getResult
	require.Equal(t, uint16(22), got.SSHPort, "port reverted to before")
	require.False(t, got.SSHPasswordAuth, "password_auth reverted to before")
	require.False(t, got.SSHUserPasswordAuth, "user_password_auth reverted to before")
	require.Equal(t, "keep.example.com", got.Hostname, "unrelated field must not be touched by the SSH revert")
}

// AC5: the sandbox revert restores mode + default image, and nothing else.
func TestSSHSandboxRevert_OnFailedApplyRestoresBeforeFields(t *testing.T) {
	before := models.ServerSettings{ID: 1, SSHSandboxMode: "bubblewrap", DefaultNspawnImageVersion: "debian-12"}
	after := &models.ServerSettings{ID: 1, SSHSandboxMode: "nspawn", DefaultNspawnImageVersion: "debian-13", Hostname: "keep.example.com"}

	mockAgent := agent.NewMockClient()
	mockAgent.OnError("system.set_ssh_sandbox_mode", errors.New("agent unreachable"))
	h, cr := revertHandler(after, mockAgent)

	revert := func(cur *models.ServerSettings) {
		cur.SSHSandboxMode = before.SSHSandboxMode
		cur.DefaultNspawnImageVersion = before.DefaultNspawnImageVersion
	}
	call := settingsops.AgentCall{Method: "system.set_ssh_sandbox_mode", Params: map[string]any{"mode": "nspawn"}, Timeout: 30 * time.Second}
	h.dispatchReverting(call, "agent set_ssh_sandbox_mode failed", revert)

	require.Equal(t, 1, cr.upserts)
	got := cr.getResult
	require.Equal(t, "bubblewrap", got.SSHSandboxMode, "mode reverted to before")
	require.Equal(t, "debian-12", got.DefaultNspawnImageVersion, "default image reverted to before")
	require.Equal(t, "keep.example.com", got.Hostname)
}

// A touched-but-unchanged re-sync (Kind == Reapply, revert == nil) that fails must
// NOT write: before == after, so there is nothing to roll back and a pointless
// Upsert on the drift-heal path is avoided.
func TestSSHConfigRevert_NilRevertDoesNotWrite(t *testing.T) {
	after := &models.ServerSettings{ID: 1, SSHPort: 22, SSHPasswordAuth: true}

	mockAgent := agent.NewMockClient()
	mockAgent.OnError("system.set_ssh_config", errors.New("agent unreachable"))
	h, cr := revertHandler(after, mockAgent)

	call := settingsops.AgentCall{Method: "system.set_ssh_config", Params: map[string]any{"port": uint16(22)}, Timeout: 30 * time.Second}
	h.dispatchReverting(call, "agent set_ssh_config failed", nil)

	require.Equal(t, 0, cr.upserts, "a re-sync failure with no value change must not write")
	require.Equal(t, uint16(22), cr.getResult.SSHPort)
}

// A successful apply never reverts, even when a revert func is supplied.
func TestSSHConfigRevert_SuccessDoesNotRevert(t *testing.T) {
	after := &models.ServerSettings{ID: 1, SSHPort: 2222, SSHPasswordAuth: true}

	mockAgent := agent.NewMockClient()
	mockAgent.On("system.set_ssh_config", map[string]any{"status": "ok"})
	h, cr := revertHandler(after, mockAgent)

	revert := func(cur *models.ServerSettings) { cur.SSHPort = 22 }
	call := settingsops.AgentCall{Method: "system.set_ssh_config", Params: map[string]any{"port": uint16(2222)}, Timeout: 30 * time.Second}
	h.dispatchReverting(call, "agent set_ssh_config failed", revert)

	require.Equal(t, 0, cr.upserts, "a successful apply must not revert")
	require.Equal(t, uint16(2222), cr.getResult.SSHPort, "persisted after-state stands")
}
