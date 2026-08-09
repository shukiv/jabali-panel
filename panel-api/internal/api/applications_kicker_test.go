package api

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// kickerAgentFake records agent calls and signals when app.install arrives.
type kickerAgentFake struct {
	mu     sync.Mutex
	params map[string]any
	done   chan struct{}
}

func (f *kickerAgentFake) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	if command == "app.install" {
		f.mu.Lock()
		f.params, _ = params.(map[string]any)
		f.mu.Unlock()
		close(f.done)
	}
	return json.RawMessage(`{"version":"1.18.4"}`), nil
}

// kickerInstallsFake absorbs the kicker's status writes. Every other repo
// method panics via the embedded nil interface — the kicker must not need it.
type kickerInstallsFake struct {
	repository.ApplicationInstallRepository
}

func (f *kickerInstallsFake) UpdateStatus(context.Context, string, string, *string, *string) error {
	return nil
}

// The osTicket kicker must seed the SAME admin username the service stored on
// the install row (shown in the UI and CLI). It used to re-derive from Params
// with a "sysadmin" fallback, so every install without an explicit
// admin_username displayed a generated name that could not log in — caught by
// the JAB-231 box E2E (panel showed "ebwxhl", osTicket seeded "sysadmin").
func TestOsTicketKicker_SeedsStoredAdminUsername(t *testing.T) {
	ag := &kickerAgentFake{done: make(chan struct{})}
	deps := ApplicationHandlerConfig{
		ApplicationInstalls: &kickerInstallsFake{},
		Agent:               ag,
		// CronJobs nil → cron creation skipped inside the kicker.
	}

	pass := dispatchInstallKicker(context.Background(), "osticket", kickContext{
		InstallID:     "01TESTINSTALLID000000000000",
		OSUser:        "bob",
		DocRoot:       "/home/bob/public_html",
		SiteURL:       "https://helpdesk.example.com",
		AdminUsername: "zqxwvu", // what the service generated AND stored
		AdminEmail:    "admin@example.com",
		Params:        map[string]any{"helpdesk_email": "help@example.com"},
	}, deps)

	select {
	case <-ag.done:
	case <-time.After(5 * time.Second):
		t.Fatal("agent app.install was never called")
	}

	ag.mu.Lock()
	defer ag.mu.Unlock()
	if got := ag.params["admin_username"]; got != "zqxwvu" {
		t.Fatalf("agent seeded admin_username %v; the stored credential is %q — the UI shows the stored one", got, "zqxwvu")
	}
	if pass == "" || ag.params["admin_pass"] != pass {
		t.Fatalf("returned admin password %q must be what the agent seeds (%v)", pass, ag.params["admin_pass"])
	}
}
