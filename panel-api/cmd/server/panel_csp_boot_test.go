package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// panel_csp_boot_test.go — the panel re-applies Server Settings -> Support chat
// to the nginx CSP snippet at start, so the CSP matches the switches even when
// install.sh seeded the chat-off copy or a disable failed to apply.

func TestResyncPanelCSP_AppliesTheSavedSwitches(t *testing.T) {
	for _, tc := range []struct {
		row  models.ServerSettings
		want bool
	}{
		{models.ServerSettings{ID: 1}, false},
		{models.ServerSettings{ID: 1, SupportChatAdmin: true}, true},
		{models.ServerSettings{ID: 1, SupportChatClients: true}, true},
	} {
		a := &tunablesAgent{resp: json.RawMessage(`{"no_change":true}`)}
		row := tc.row
		resyncPanelCSP(context.Background(), a, &row, discardLog())
		if len(a.calls) != 1 || a.calls[0].command != "nginx.panel_csp.apply" {
			t.Fatalf("want one nginx.panel_csp.apply, got %+v", a.calls)
		}
		if got := a.calls[0].params["support_chat"]; got != tc.want {
			t.Errorf("row %+v: support_chat = %v, want %v", tc.row, got, tc.want)
		}
	}
}

func TestResyncPanelCSP_NoRowNoCall(t *testing.T) {
	a := &tunablesAgent{}
	resyncPanelCSP(context.Background(), a, nil, discardLog())
	if len(a.calls) != 0 {
		t.Fatalf("no row must not call the agent: %+v", a.calls)
	}
}

func TestResyncPanelCSP_LogsAFailure(t *testing.T) {
	var buf bytes.Buffer
	a := &tunablesAgent{err: errors.New("agent down")}
	resyncPanelCSP(context.Background(), a, &models.ServerSettings{ID: 1}, slog.New(slog.NewTextHandler(&buf, nil)))
	if !strings.Contains(buf.String(), "panel CSP re-apply failed") {
		t.Fatalf("a failed re-apply must be logged: %q", buf.String())
	}
}

func TestServeResyncsPanelCSPAtStart(t *testing.T) {
	src, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "resyncPanelCSP(") {
		t.Fatal("serve.go never calls resyncPanelCSP: the CSP only follows the switches on a save")
	}
}
