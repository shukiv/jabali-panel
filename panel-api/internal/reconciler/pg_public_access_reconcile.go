package reconciler

import (
	"context"
	"encoding/json"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// WithPGPublicAccess wires the Postgres PUBLIC access pass. nil disables it.
func (r *Reconciler) WithPGPublicAccess(repo repository.PGDatabaseGrantRepository) *Reconciler {
	r.pgPublicAccess = repo
	return r
}

// reconcilePGPublicAccess asks the Agent to take CONNECT and TEMPORARY away
// from PUBLIC on every Postgres database. Postgres grants both on each new
// database, so any tenant's role could connect to another tenant's database
// and read its catalog. db.postgres.create_db revokes them now; this pass
// converts the databases created before, and any created outside the panel.
//
// It sends the panel's grants with the call: the Agent first gives each
// granted role its own database grant, because a role on a database restored
// before the restore fix reached it through PUBLIC alone. A failed read skips
// the tick, so PUBLIC is never revoked without that list.
//
// It runs only while the Postgres engine is enabled. PhasePGPublicAccess
// sends it on the first tick, whenever the grants change, and hourly; a
// failed run is retried on the next tick.
func (r *Reconciler) reconcilePGPublicAccess(ctx context.Context) {
	if r.agent == nil || r.pgPublicAccess == nil || r.serverSettings == nil {
		return
	}
	srv, err := r.settingsGet(ctx)
	if err != nil || srv == nil || !srv.PostgresEnabled {
		return
	}
	grants, err := r.pgPublicAccess.ListPGDatabaseGrants(ctx)
	if err != nil {
		r.log.Warn("pg-public-access: list grants failed; PUBLIC left as is", "error", err)
		return
	}
	if grants == nil {
		// Sent as {}: the Agent refuses a missing list.
		grants = map[string][]string{}
	}
	params := map[string]any{"grants": grants}
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
