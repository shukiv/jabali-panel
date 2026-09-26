package reconciler

import (
	"context"
	"encoding/json"
	"time"
)

// WithDBGrantEscape enables the legacy MariaDB grant conversion pass.
func (r *Reconciler) WithDBGrantEscape() *Reconciler {
	r.dbGrantEscape = true
	return r
}

// reconcileDBGrantEscape asks the Agent to convert database-level MariaDB
// grants that name a database with an unescaped `_`. In `GRANT ... ON db.*`
// the underscore is a one-character wildcard, so such a grant also covers a
// sibling tenant's database whose name differs at that position.
// db_user.grant writes the escaped name now; this pass converts the grants
// written before it, which exist on every box.
//
// PhaseDBGrantEscape sends it on the first tick and then hourly. A failed
// conversion is retried on the next tick.
func (r *Reconciler) reconcileDBGrantEscape(ctx context.Context) {
	if r.agent == nil || !r.dbGrantEscape {
		return
	}
	params := map[string]any{}
	_, _ = r.project(ctx, PhaseDBGrantEscape, "all", fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		raw, err := r.agent.Call(callCtx, "db_user.escape_legacy_grants", params)
		if err != nil {
			r.log.Warn("db-grant-escape: legacy grants not all converted; will retry next tick", "error", err)
			return err
		}
		var resp struct {
			Converted []string `json:"converted"`
		}
		if json.Unmarshal(raw, &resp) == nil && len(resp.Converted) > 0 {
			r.log.Info("db-grant-escape: converted legacy database grants", "converted", resp.Converted)
		}
		return nil
	})
}
