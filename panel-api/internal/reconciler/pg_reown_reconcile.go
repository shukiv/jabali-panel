package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// pgReownRequest is db.postgres.reown_superuser_objects' request: each of
// the panel's Postgres databases with the role its objects go to, or "" for
// the database's holder role.
type pgReownRequest struct {
	Databases map[string]string `json:"databases"`
}

// pgReownResponse is its response. Reowned counts, per database, the objects
// handed over; Left names the routines in an untrusted language that stay
// owned by a superuser; Failed says why nothing moved in a database.
type pgReownResponse struct {
	Reowned map[string]int      `json:"reowned"`
	Left    map[string][]string `json:"left"`
	Failed  map[string]string   `json:"failed"`
}

// WithPGReown wires the pass that hands superuser-owned objects in the
// panel's Postgres databases to each database's user. nil disables it.
func (r *Reconciler) WithPGReown(repo repository.PGDatabaseOwnerRepository) *Reconciler {
	r.pgReown = repo
	return r
}

// reconcilePGReown asks the Agent to hand what a superuser owns in each of
// the panel's Postgres databases to the database's user (GH #2004). An
// account restore from before GH #1993 loaded its dump as postgres, so the
// tables, sequences and routines it created stayed owned by postgres: the
// database's user can't TRUNCATE ... RESTART IDENTITY its own tables, and a
// routine a superuser owns runs with a superuser's rights. Restores now give
// the objects to a holder role the database's user takes over; this pass
// converts the databases restored before.
//
// It sends every database with its user (the Postgres user of the same
// account granted on it first), or "" for a database with none: the Agent
// gives that one's objects to the database's holder role, which the first
// user later granted takes over. A failed read skips the tick, so a database
// is never sent without its user.
//
// It runs only while the Postgres engine is enabled. PhasePGReown sends it
// on the first tick, whenever a database or its user changes, and daily; a
// failed run is retried on the next tick. An Agent from before the verb
// (mid-update) counts as done until the next of those.
func (r *Reconciler) reconcilePGReown(ctx context.Context) {
	if r.agent == nil || r.pgReown == nil || r.serverSettings == nil {
		return
	}
	srv, err := r.settingsGet(ctx)
	if err != nil || srv == nil || !srv.PostgresEnabled {
		return
	}
	owners, err := r.pgReown.ListPGDatabaseOwners(ctx)
	if err != nil {
		r.log.Warn("pg-reown: list databases failed; ownership left as is", "error", err)
		return
	}
	if len(owners) == 0 {
		return
	}
	params := pgReownRequest{Databases: owners}
	_, _ = r.project(ctx, PhasePGReown, "all", fingerprint(params), false, func() error {
		callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		raw, err := r.agent.Call(callCtx, "db.postgres.reown_superuser_objects", params)
		if err != nil {
			var ae *agent.AgentError
			if errors.As(err, &ae) && ae.Code == agent.CodeUnknownCommand {
				return nil
			}
			r.log.Warn("pg-reown: hand-over failed; will retry next tick", "error", err)
			return err
		}
		var resp pgReownResponse
		if json.Unmarshal(raw, &resp) != nil {
			return nil
		}
		if len(resp.Reowned) > 0 {
			r.log.Info("pg-reown: handed superuser-owned objects to the database's user", "databases", resp.Reowned)
		}
		if len(resp.Left) > 0 {
			r.log.Warn("pg-reown: routines in an untrusted language stay owned by a superuser; an admin has to review them", "routines", resp.Left)
		}
		if len(resp.Failed) > 0 {
			r.log.Warn("pg-reown: databases left as they were", "databases", resp.Failed)
		}
		return nil
	})
}
