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

// resyncNginxTunables re-applies Server Settings → Nginx from server_settings at
// panel start, the way GH #634 re-applies the FastCGI cache capacity.
//
// Before this, only a save on that page wrote
// /etc/nginx/conf.d/05-jabali-tunables.conf. install.sh seeds it on a fresh
// install, not on `jabali update`, so a box installed before the file existed
// never got one: tenant vhosts took nginx's built-in 1m body limit while the
// page showed 512m. A box seeded with the old 50m default kept 50m after
// migration 000252 moved the setting to 512m.
//
// The agent compares the rendered fragment and nginx.conf with what is on disk
// and returns no_change without touching nginx when they match, so a normal
// start costs two file reads. When they differ, the same nginx -t gate and
// rollback as a save applies. Like a save, this also sets the worker,
// keepalive, gzip and server_tokens lines in nginx.conf to the saved values.
func resyncNginxTunables(ctx context.Context, a agent.AgentInterface, row *models.ServerSettings, created bool, log *slog.Logger) {
	// A row created this boot holds struct-zero fields, not saved settings. An
	// empty body size would render a fragment without client_max_body_size and
	// put nginx back on its 1m default.
	if created || row == nil || row.NginxClientMaxBodySize == "" {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := a.Call(callCtx, "nginx.tunables.apply", settingsops.NginxTunablesParams(row))
	if err != nil {
		log.Warn("nginx tunables re-apply failed", "err", err)
		return
	}
	var res struct {
		NoChange        bool `json:"no_change"`
		MainConfPatched bool `json:"main_conf_patched"`
	}
	if json.Unmarshal(raw, &res) == nil && !res.NoChange {
		log.Info("nginx tunables re-applied from server settings", "main_conf_patched", res.MainConfPatched)
	}
}
