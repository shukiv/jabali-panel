package reconciler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// notFoundAgent answers the given methods with the agent's not_found, as on a
// server where the jabali-webmail unit was never installed.
type notFoundAgent struct {
	*fakeAgent
	methods map[string]bool
}

func (a *notFoundAgent) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := a.fakeAgent.Call(ctx, method, params)
	if a.methods[method] {
		return nil, &agent.AgentError{Code: agent.CodeNotFound, Message: "jabali-webmail is not installed"}
	}
	return raw, err
}

// GH #2474: webmail off on a server without the jabali-webmail unit. Stop and
// disable answer not_found, which already is the off state: the daemon phase
// is stamped, so the next tick doesn't call them again and nothing is logged.
func TestWebmailDaemon_OffWithoutUnitIsSatisfied(t *testing.T) {
	r, ag := steadyStateFixture(t, true)
	var logs bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&logs, nil))
	r.agent = &notFoundAgent{fakeAgent: ag, methods: map[string]bool{"service.stop": true, "service.disable": true}}
	r.serverSettings.(*fakeServerSettingsRepo).settings.WebmailEnabled = false

	calls, _ := tickCalls(t, r, ag)
	if countOf(calls, "service.stop") != 1 || countOf(calls, "service.disable") != 1 {
		t.Fatalf("turning webmail off must still try stop and disable: stop=%d disable=%d",
			countOf(calls, "service.stop"), countOf(calls, "service.disable"))
	}
	calls, _ = tickCalls(t, r, ag)
	if n := countOf(calls, "service.stop") + countOf(calls, "service.disable"); n != 0 {
		t.Fatalf("a unit that isn't installed is already off, but the next tick called stop/disable %d times", n)
	}
	if strings.Contains(logs.String(), "jabali-webmail failed") {
		t.Errorf("not_found on the off path must not be logged as a failure:\n%s", logs.String())
	}
}

// Turning webmail on still needs the unit: not_found on start stays a failure,
// is logged and is retried.
func TestWebmailDaemon_OnWithoutUnitStaysAFailure(t *testing.T) {
	r, ag := steadyStateFixture(t, true)
	settings := r.serverSettings.(*fakeServerSettingsRepo).settings
	settings.WebmailEnabled = false
	tickCalls(t, r, ag)

	var logs bytes.Buffer
	r.log = slog.New(slog.NewTextHandler(&logs, nil))
	r.agent = &notFoundAgent{fakeAgent: ag, methods: map[string]bool{"service.start": true, "service.enable": true}}
	settings.WebmailEnabled = true
	tickCalls(t, r, ag)
	calls, _ := tickCalls(t, r, ag)
	if countOf(calls, "service.start") != 1 {
		t.Fatalf("a missing unit while webmail is wanted must be retried: start=%d", countOf(calls, "service.start"))
	}
	if !strings.Contains(logs.String(), "service.start jabali-webmail failed") {
		t.Errorf("a missing unit while webmail is wanted must be logged:\n%s", logs.String())
	}
}
