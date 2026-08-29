// Package reconciler — tenant database size sampler (GH #1242).
//
// Keeps databases.size_bytes fresh so the admin User List can show total
// storage = home quota + databases + mail. The panel-api DB user is scoped to
// its own schema and cannot read tenant sizes from information_schema, so this
// asks the root agent (db.usage.by_schema — one information_schema pass over
// every schema) and maps the sizes back to the tenant database rows by name.
//
// Best-effort: a failed pass is logged and retried next interval; a schema with
// no matching row is ignored, and a row whose schema is absent keeps its last
// known size.
package reconciler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	dbUsageInterval = 15 * time.Minute
	dbUsageCallTO   = 30 * time.Second
)

// StartDBUsageTicker runs the DB-size sampler until ctx is cancelled. Nil deps
// disable it.
func StartDBUsageTicker(ctx context.Context, ag agent.AgentInterface, databases repository.DatabaseRepository, log bwTickerLogger) {
	if ag == nil || databases == nil {
		return
	}
	log.Info("db-usage ticker starting", "interval", dbUsageInterval.String())

	// First pass shortly after boot so the User List populates without waiting.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(90 * time.Second):
			sampleDBUsage(ctx, ag, databases, log)
		}
	}()

	t := time.NewTicker(dbUsageInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sampleDBUsage(ctx, ag, databases, log)
		}
	}
}

func sampleDBUsage(ctx context.Context, ag agent.AgentInterface, databases repository.DatabaseRepository, log bwTickerLogger) {
	rows, err := databases.ListAllMariaDB(ctx)
	if err != nil {
		log.Warn("db-usage: list failed", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	sizes, ok := dbSchemaSizes(ctx, ag)
	if !ok {
		log.Warn("db-usage: schema sizes unavailable")
		return
	}
	now := time.Now().UTC()
	updated := 0
	for i := range rows {
		select {
		case <-ctx.Done():
			return
		default:
		}
		d := rows[i]
		size, present := sizes[strings.ToLower(d.Name)]
		if !present {
			continue // schema not in information_schema (dropped out of band) — keep last
		}
		if d.SizeBytes == size && d.SizeCheckedAt != nil {
			continue // unchanged → no write (idempotent)
		}
		if err := databases.UpdateSize(ctx, d.ID, size, now); err != nil {
			log.Warn("db-usage: writeback failed", "db", d.Name, "error", err)
			continue
		}
		updated++
	}
	log.Info("db-usage: pass complete", "updated", updated, "total", len(rows))
}

// dbSchemaSizes dispatches db.usage.by_schema and returns schema->bytes,
// lower-cased. ok=false on any agent/decode error.
func dbSchemaSizes(ctx context.Context, ag agent.AgentInterface) (map[string]uint64, bool) {
	callCtx, cancel := context.WithTimeout(ctx, dbUsageCallTO)
	defer cancel()
	raw, err := ag.Call(callCtx, "db.usage.by_schema", nil)
	if err != nil {
		return nil, false
	}
	var v struct {
		Schemas []struct {
			Schema string `json:"schema"`
			Bytes  int64  `json:"bytes"`
		} `json:"schemas"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	out := make(map[string]uint64, len(v.Schemas))
	for _, s := range v.Schemas {
		if s.Bytes < 0 {
			continue
		}
		out[strings.ToLower(s.Schema)] = uint64(s.Bytes)
	}
	return out, true
}
