package commands

// db.usage.by_schema — one information_schema pass returning every
// schema's on-disk footprint (JAB-243). The panel's DB-quota enforcement
// sweep needs authoritative per-database sizes; querying through the
// panel's own scoped connection would silently under-report (a schema
// the panel user can't see simply vanishes from information_schema), so
// the root-privileged agent answers instead — one query for the whole
// host, not one call per database.

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type dbUsageSchema struct {
	Schema string `json:"schema"`
	Bytes  int64  `json:"bytes"`
}

type dbUsageBySchemaResponse struct {
	Schemas []dbUsageSchema `json:"schemas"`
}

// dbUsageSystemSchemas are never tenant data — excluded from the reply.
var dbUsageSystemSchemas = map[string]bool{
	"information_schema": true,
	"performance_schema": true,
	"mysql":              true,
	"sys":                true,
}

func dbUsageBySchemaHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	// data_length+index_length lags a little behind reality (InnoDB
	// persistent stats), which is fine for quota enforcement — the
	// consumer applies hysteresis anyway.
	out, err := exec.CommandContext(ctx, "mysql", "-N", "-e",
		"SELECT table_schema, COALESCE(SUM(data_length+index_length),0) FROM information_schema.tables GROUP BY table_schema",
	).Output()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeUnavailable, Message: "information_schema query failed: " + err.Error()}
	}
	resp := dbUsageBySchemaResponse{Schemas: []dbUsageSchema{}}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || dbUsageSystemSchemas[fields[0]] {
			continue
		}
		n, perr := strconv.ParseInt(fields[1], 10, 64)
		if perr != nil {
			continue
		}
		resp.Schemas = append(resp.Schemas, dbUsageSchema{Schema: fields[0], Bytes: n})
	}
	return resp, nil
}

func init() {
	Default.Register("db.usage.by_schema", dbUsageBySchemaHandler)
}
