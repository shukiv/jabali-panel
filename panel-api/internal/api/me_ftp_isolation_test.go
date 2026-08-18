package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

func resetFtpIsoCache() {
	ftpIsoMu.Lock()
	ftpIsoKnown, ftpIsoCached = false, false
	ftpIsoMu.Unlock()
}

// GH #1171: ftpIsolationAvailable drives the UI's Isolated default. It must be
// false whenever we can't confirm quota (no agent / empty mount), and mirror the
// agent probe otherwise.
func TestFtpIsolationAvailable(t *testing.T) {
	t.Run("no agent → false (default Isolated off)", func(t *testing.T) {
		resetFtpIsoCache()
		h := &meExtHandler{cfg: MeHandlerConfig{QuotaMount: "/"}}
		if h.ftpIsolationAvailable(context.Background()) {
			t.Fatal("want false without an agent")
		}
	})
	t.Run("empty QuotaMount → false, no probe", func(t *testing.T) {
		resetFtpIsoCache()
		mock := agent.NewMockClient().On("ftpaccount.isolation_status", map[string]any{"available": true})
		h := &meExtHandler{cfg: MeHandlerConfig{Agent: mock, QuotaMount: ""}}
		if h.ftpIsolationAvailable(context.Background()) {
			t.Fatal("want false with empty mount")
		}
		if mockCalled(mock, "ftpaccount.isolation_status") != 0 {
			t.Fatal("must not probe the agent when QuotaMount is empty")
		}
	})
	t.Run("agent reports available → true", func(t *testing.T) {
		resetFtpIsoCache()
		mock := agent.NewMockClient().On("ftpaccount.isolation_status", map[string]any{"available": true, "quota_mount": "/"})
		h := &meExtHandler{cfg: MeHandlerConfig{Agent: mock, QuotaMount: "/"}}
		if !h.ftpIsolationAvailable(context.Background()) {
			t.Fatal("want true when the agent reports available")
		}
	})
	t.Run("agent reports unavailable → false", func(t *testing.T) {
		resetFtpIsoCache()
		mock := agent.NewMockClient().On("ftpaccount.isolation_status", map[string]any{"available": false, "quota_mount": "/"})
		h := &meExtHandler{cfg: MeHandlerConfig{Agent: mock, QuotaMount: "/"}}
		if h.ftpIsolationAvailable(context.Background()) {
			t.Fatal("want false when the agent reports unavailable")
		}
	})
}
