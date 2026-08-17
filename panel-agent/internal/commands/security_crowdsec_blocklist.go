package commands

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"strings"
	"time"
)

// runCscliRaw runs `cscli <args> -o raw`. Raw CSV is an order of
// magnitude cheaper for crowdsec to emit and for us to parse than
// `-o json` when a community/cscli-import blocklist has dropped
// 100k+ decisions into the table (incident: crowdsec pegged 100%
// CPU because blocklistsRefreshOnce pulled 100k JSON objects ×6
// origins every 5 min just to count them).
func runCscliRaw(ctx context.Context, args ...string) ([]byte, error) {
	full := append([]string{}, args...)
	full = append(full, "-o", "raw")
	cmd := execCommandContext(ctx, "cscli", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 512 {
			msg = msg[:512] + "…(truncated)"
		}
		if msg == "" {
			return nil, err
		}
		return nil, csInternal("cscli "+strings.Join(args, " "), err)
	}
	return stdout.Bytes(), nil
}

// aggregateBlocklistRaw folds one origin's `cscli decisions list
// --origin <o> -o raw` CSV into agg, keyed "<origin>/<scenario>".
// Columns: id,source,ip,reason,action,country,as,events_count,
// expiration,simulated,alert_id  (reason=scenario, expiration=a Go
// duration, not an absolute time → LatestEnd = now + max(duration)).
// Rows with an unparseable duration still count; only LatestEnd is
// skipped for them. Header + short/garbage rows are ignored.
func aggregateBlocklistRaw(origin string, raw []byte, now time.Time, agg map[string]*csBlocklistEntry) {
	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(rec) < 9 {
			continue
		}
		if rec[0] == "id" { // header
			continue
		}
		scenario := rec[3]
		if scenario == "" {
			continue
		}
		key := origin + "/" + scenario
		e, ok := agg[key]
		if !ok {
			e = &csBlocklistEntry{Name: key}
			agg[key] = e
		}
		e.Count++
		if d, derr := time.ParseDuration(rec[8]); derr == nil {
			end := now.Add(d).UTC().Format(time.RFC3339)
			if end > e.LatestEnd { // RFC3339 UTC sorts lexically
				e.LatestEnd = end
			}
		}
	}
}

// blocklistOrigins is the set queried per refresh. Bare `cscli
// decisions list` groups by alert and only reports the alert's
// top-level origin, so CAPI decisions inside a firehol-import alert
// would be missed — querying per-origin is required for a complete
// picture (see csBlocklistsListHandler doc).
var blocklistOrigins = []string{"CAPI", "lists", "cscli-import", "crowdsec", "console", "manual"}

// refreshBlocklistsSnapshot does one raw cscli pass per origin and
// returns the aggregated snapshot. Pulled out of blocklistsRefreshOnce
// so the heavy bit is a pure-ish function (cscli call + fold).
func refreshBlocklistsSnapshot(ctx context.Context) csBlocklistsResponse {
	resp := csBlocklistsResponse{Blocklists: []csBlocklistEntry{}}
	agg := map[string]*csBlocklistEntry{}
	now := time.Now()
	for _, origin := range blocklistOrigins {
		out, err := runCscliRaw(ctx, "decisions", "list", "--origin", origin, "--limit", "100000")
		if err != nil {
			continue // keep partial; transient cscli failure shouldn't blank the snapshot
		}
		aggregateBlocklistRaw(origin, out, now, agg)
	}
	for _, e := range agg {
		resp.Blocklists = append(resp.Blocklists, *e)
		resp.Total += e.Count
	}
	return resp
}
