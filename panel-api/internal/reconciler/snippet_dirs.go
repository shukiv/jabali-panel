package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// pruneSnippetDirs asks the agent to remove the per-domain snippet dirs
// (/etc/nginx/jabali/<name>/) of names with no domain row. Domain deletes made
// before domain.delete removed that dir left them behind, and a domain created
// later with one of those names would include them. The agent keeps every
// listed name, and every name that still has a vhost.
//
// Only a complete list may decide what goes, so a partial page skips it. It is
// tried once per panel process: the leftovers do not grow (domain.delete now
// removes the dir), and a restart, which every update makes, tries again.
func (r *Reconciler) pruneSnippetDirs(ctx context.Context, rows []models.Domain, total int64) {
	if len(rows) == 0 || int64(len(rows)) != total || !r.snippetDirsPruned.CompareAndSwap(false, true) {
		return
	}
	keep := make([]string, 0, len(rows))
	for i := range rows {
		keep = append(keep, rows[i].Name)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := r.agent.Call(cctx, "nginx.snippet_dirs.prune", map[string]any{"keep": keep})
	if err != nil {
		var ae *agent.AgentError
		if errors.As(err, &ae) && ae.Code == agent.CodeUnknownCommand {
			return // an agent from before the verb (mid-update)
		}
		r.log.Warn("reconcile: snippet dir prune failed", "err", err)
		return
	}
	var res struct {
		Removed []string `json:"removed"`
		Failed  []string `json:"failed"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		r.log.Warn("reconcile: snippet dir prune: bad agent response", "err", err)
		return
	}
	if len(res.Removed) > 0 || len(res.Failed) > 0 {
		r.log.Info("reconcile: removed the snippet dirs of deleted domains",
			"removed", strings.Join(res.Removed, ", "), "failed", strings.Join(res.Failed, ", "))
	}
}
