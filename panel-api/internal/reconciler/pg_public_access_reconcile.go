package reconciler

import (
	"context"
	"encoding/json"
	"time"
)

// WithPGPublicAccess enables the Postgres PUBLIC access pass.
func (r *Reconciler) WithPGPublicAccess() *Reconciler {
	r.pgPublicAccess = true
	return r
}

// reconcilePGPublicAccess asks the Agent to take CONNECT and TEMPORARY away
// from PUBLIC on every Postgres database. Postgres grants both on each new
// database, so any tenant's role could connect to another tenant's database
// and read its catalog. db.postgres.create_db revokes them now; this pass
// converts the databases created before, and any created outside the panel.
//
// It runs only while the Postgres engine is enabled. PhasePGPublicAccess
// sends it on the first tick and then hourly; a failed run is retried on the
// next tick.
func (r *Reconciler) reconcilePGPublicAccess(ctx context.Context) {
	if r.agent == nil || !r.pgPublicAccess || r.serverSettings == nil {
		return
	}
	srv, err := r.settingsGet(ctx)
	if err != nil || srv == nil || !srv.PostgresEnabled {
		return
	}
	params := map[string]any{}
	_, _ = r.project(ctx, PhasePGPublicAccess, "all", fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		raw, err := r.agent.Call(callCtx, "db.postgres.revoke_public_access", params)
		if err != nil {
			r.log.Warn("pg-public-access: revoke failed; will retry next tick", "error", err)
			return err
		}
		var resp struct {
			Revoked []string `json:"revoked"`
		}
		if json.Unmarshal(raw, &resp) == nil && len(resp.Revoked) > 0 {
			r.log.Info("pg-public-access: revoked PUBLIC access", "databases", resp.Revoked)
		}
		return nil
	})
}
