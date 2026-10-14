package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/settingsops"
)

// resyncPanelCSP re-applies Server Settings -> Support chat to the panel's
// nginx CSP snippet at panel start. A save applies it too, but the snippet can
// still disagree with the row: install.sh seeds the chat-off copy on a box
// that has none, a failed apply on a disable keeps the switches off with the
// wider CSP on disk, and a restored DB brings its own switches. The agent
// returns no_change without touching nginx when the snippet already matches.
//
// A row created this boot holds the defaults (chat off), which is also the
// state to apply, so unlike resyncNginxTunables it is not skipped.
func resyncPanelCSP(ctx context.Context, a agent.AgentInterface, row *models.ServerSettings, log *slog.Logger) {
	if row == nil || a == nil {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := a.Call(callCtx, "nginx.panel_csp.apply", settingsops.SupportChatCSPParams(row))
	if err != nil {
		log.Warn("panel CSP re-apply failed", "err", err)
		return
	}
	var res struct {
		NoChange bool `json:"no_change"`
	}
	if json.Unmarshal(raw, &res) == nil && !res.NoChange {
		log.Info("panel CSP re-applied from server settings", "support_chat", row.SupportChatEnabled())
	}
}
