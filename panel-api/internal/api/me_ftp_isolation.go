package api

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// ftpIsolationAvailable reports whether isolated FTP accounts can be provisioned
// on this host — filesystem disk quota is enabled on the panel's quota mount, so
// the per-account setquota cap can be enforced. The panel-agent does the probe
// (root); the result is install-time-static, so a short TTL keeps
// /me/server-capabilities cheap while still recovering within minutes if an admin
// enables quota. GH #1171. On any uncertainty (no agent, empty mount, probe
// error with no prior answer) it returns false so the UI defaults "Isolated" off
// rather than defaulting on and failing at create.
var (
	ftpIsoMu     sync.Mutex
	ftpIsoCached bool
	ftpIsoKnown  bool
	ftpIsoAt     time.Time
)

const ftpIsoTTL = 5 * time.Minute

func (h *meExtHandler) ftpIsolationAvailable(ctx context.Context) bool {
	if h.cfg.Agent == nil || h.cfg.QuotaMount == "" {
		return false
	}
	ftpIsoMu.Lock()
	defer ftpIsoMu.Unlock()
	if ftpIsoKnown && time.Since(ftpIsoAt) < ftpIsoTTL {
		return ftpIsoCached
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(cctx, "ftpaccount.isolation_status", map[string]any{
		"quota_mount": h.cfg.QuotaMount,
	})
	if err != nil {
		if ftpIsoKnown {
			return ftpIsoCached // keep last known on a transient probe error
		}
		return false
	}
	var resp struct {
		Available bool `json:"available"`
	}
	_ = json.Unmarshal(raw, &resp)
	ftpIsoCached, ftpIsoKnown, ftpIsoAt = resp.Available, true, time.Now()
	return ftpIsoCached
}
