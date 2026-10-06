package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestServiceRestart_OK(t *testing.T) {
	state := map[string]fakeServiceState{
		"nginx": {active: "inactive", loadState: "loaded"},
	}
	installFakeSystemctl(t, state)

	params, _ := json.Marshal(serviceRestartParams{Name: "nginx"})
	out, err := serviceRestartHandler(context.Background(), params)
	require.NoError(t, err)

	resp := out.(serviceRestartResponse)
	assert.Equal(t, "nginx", resp.Name)
	assert.Equal(t, "active", resp.Active, "fake restart should flip active")
	assert.Equal(t, "loaded", resp.LoadState)
}

// TestServiceRestart_RejectsMasked — masked units cannot be restarted;
// surface FailedPrecondition so the API can render a helpful message.
// We use jabali-stalwart as the test subject: it's in the allow-list, so
// the handler gets past the allow-list gate and reaches the mask check.
// (Global php<v>-fpm was removed from the allow-list per ADR-0025 —
// those are always masked and never restartable by design.)
func TestServiceRestart_RejectsMasked(t *testing.T) {
	installFakeSystemctl(t, map[string]fakeServiceState{
		"jabali-stalwart": {active: "inactive", loadState: "masked"},
	})

	params, _ := json.Marshal(serviceRestartParams{Name: "jabali-stalwart"})
	_, err := serviceRestartHandler(context.Background(), params)

	require.Error(t, err)
	ae, ok := err.(*agentwire.AgentError)
	require.True(t, ok, "expected AgentError, got %T", err)
	assert.Equal(t, agentwire.CodeFailedPrecondition, ae.Code)
	assert.Contains(t, ae.Message, "masked")
}

func TestServiceRestart_RejectsNotInstalled(t *testing.T) {
	installFakeSystemctl(t, map[string]fakeServiceState{})

	params, _ := json.Marshal(serviceRestartParams{Name: "nginx"})
	_, err := serviceRestartHandler(context.Background(), params)

	require.Error(t, err)
	ae, ok := err.(*agentwire.AgentError)
	require.True(t, ok)
	assert.Equal(t, agentwire.CodeNotFound, ae.Code)
}

// TestServiceRestart_RejectsOffAllowList — a compromised panel must not
// be able to restart arbitrary systemd units.
func TestServiceRestart_RejectsOffAllowList(t *testing.T) {
	installFakeSystemctl(t, map[string]fakeServiceState{
		"sshd": {active: "active", loadState: "loaded"},
	})

	params, _ := json.Marshal(serviceRestartParams{Name: "sshd"})
	_, err := serviceRestartHandler(context.Background(), params)

	require.Error(t, err)
	ae, ok := err.(*agentwire.AgentError)
	require.True(t, ok)
	assert.Equal(t, agentwire.CodePermissionDenied, ae.Code)
	assert.Contains(t, ae.Message, "allow-list")
}

func TestServiceRestart_InvalidInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		payload string
		wantMsg string
	}{
		{"empty_body", ``, "params required"},
		{"malformed", `{not json`, "parse params"},
		{"empty_name", `{"name":"   "}`, "name required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := serviceRestartHandler(context.Background(), json.RawMessage(tc.payload))
			require.Error(t, err)
			ae, ok := err.(*agentwire.AgentError)
			require.True(t, ok)
			assert.Equal(t, agentwire.CodeInvalidArgument, ae.Code)
			assert.Contains(t, ae.Message, tc.wantMsg)
		})
	}
}

func TestServiceRestart_Registered(t *testing.T) {
	t.Parallel()
	for _, name := range Default.Commands() {
		if name == "service.restart" {
			return
		}
	}
	t.Fatal("service.restart not registered")
}

// recordSystemdRun swaps the exec seam and records each argv it is asked to
// run. With failOut set, every run prints failOut and exits 1.
func recordSystemdRun(t *testing.T, failOut string) *[][]string {
	t.Helper()
	var runs [][]string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		runs = append(runs, append([]string{name}, args...))
		if failOut != "" {
			return exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$0"; exit 1`, failOut)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &runs
}

// recordSystemctl wraps the installed fake systemctl and records each verb.
func recordSystemctl(t *testing.T) *[]string {
	t.Helper()
	var verbs []string
	inner := systemctlRunner
	systemctlRunner = func(ctx context.Context, args ...string) (string, error) {
		if len(args) > 0 {
			verbs = append(verbs, args[0])
		}
		return inner(ctx, args...)
	}
	t.Cleanup(func() { systemctlRunner = inner })
	return &verbs
}

// GH #1992: a deferred restart is handed to systemd as a transient timer a
// couple of seconds out and the handler returns at once. Running `systemctl
// restart nginx` inline cut off the very request asking for it.
func TestServiceRestart_DeferredSchedulesInsteadOfRestarting(t *testing.T) {
	installFakeSystemctl(t, map[string]fakeServiceState{
		"nginx": {active: "active", loadState: "loaded"},
	})
	verbs := recordSystemctl(t)
	runs := recordSystemdRun(t, "")

	out, err := serviceRestartHandler(context.Background(), json.RawMessage(`{"name":"nginx","deferred":true}`))
	require.NoError(t, err)

	assert.NotContains(t, *verbs, "restart", "a deferred restart must not run systemctl restart inline")
	require.Len(t, *runs, 1)
	assert.Equal(t, []string{
		"systemd-run", "--quiet", "--collect",
		"--unit=jabali-service-restart-nginx",
		"--on-active=2s", "--timer-property=AccuracySec=100ms",
		"systemctl", "restart", "nginx.service",
	}, (*runs)[0])

	b, _ := json.Marshal(out)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(b, &resp))
	assert.Equal(t, true, resp["scheduled"])
	assert.Equal(t, "nginx", resp["name"])
}

// The masked / not-installed checks still run before anything is scheduled.
func TestServiceRestart_DeferredStillRejectsMasked(t *testing.T) {
	installFakeSystemctl(t, map[string]fakeServiceState{
		"nginx": {active: "inactive", loadState: "masked"},
	})
	runs := recordSystemdRun(t, "")

	_, err := serviceRestartHandler(context.Background(), json.RawMessage(`{"name":"nginx","deferred":true}`))
	require.Error(t, err)
	ae, ok := err.(*agentwire.AgentError)
	require.True(t, ok)
	assert.Equal(t, agentwire.CodeFailedPrecondition, ae.Code)
	assert.Empty(t, *runs)
}

// A second click while the first restart is still pending: systemd refuses
// the duplicate timer name, and that reads as "already scheduled".
func TestServiceRestart_DeferredAlreadyScheduled(t *testing.T) {
	installFakeSystemctl(t, map[string]fakeServiceState{
		"nginx": {active: "active", loadState: "loaded"},
	})
	recordSystemdRun(t, "Failed to start transient timer unit: Unit jabali-service-restart-nginx.timer was already loaded or has a fragment file.")

	_, err := serviceRestartHandler(context.Background(), json.RawMessage(`{"name":"nginx","deferred":true}`))
	require.Error(t, err)
	ae, ok := err.(*agentwire.AgentError)
	require.True(t, ok)
	assert.Equal(t, agentwire.CodeAlreadyExists, ae.Code)
	assert.Contains(t, ae.Message, "already scheduled")
}
