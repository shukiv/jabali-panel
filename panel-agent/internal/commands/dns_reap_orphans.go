package commands

import (
	"context"
	"encoding/json"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/pdns"
)

// dns.reap-orphans — GH #1620 one-time remediation.
//
// #1629 (DeleteZone) tears a zone's child rows down before the parent `domains`
// row, so a zone delete no longer strands children. That is forward-only: a box
// that deleted a domain BEFORE #1629 still carries the orphaned child rows
// (records / domainmetadata / comments / cryptokeys with no parent `domains`
// row). They can keep being served — from the backend or its caches — until the
// rows are removed, and collide as "duplicate records" on re-add. This RPC
// sweeps them.
//
// Dry-run by default (apply=false): report per-table orphan counts and the
// affected record names, delete nothing. apply=true runs the transactional
// sweep and then purges the pdns Auth + recursor caches for each affected name.

type dnsReapOrphansParams struct {
	// Apply=false (the default) is a dry run: report counts, delete nothing. A
	// caller must opt in to deletion explicitly, mirroring the CLI's --apply.
	Apply bool `json:"apply"`
}

type dnsReapOrphansResponse struct {
	Applied bool           `json:"applied"`
	Counts  map[string]int `json:"counts"`  // orphan rows per table (pre-delete)
	Deleted map[string]int `json:"deleted"` // rows deleted per table (empty on a dry run)
	Names   []string       `json:"names"`   // distinct record names affected (and purged on apply)
}

// orphanSweeper is the pdns surface this handler needs. The package-var seam
// below lets tests substitute a fake without a live PowerDNS DB (mirrors
// php_fpm_reap.go's osUserExists stub), so the safety guards — the empty-domains
// refusal and "dry-run never deletes" — are testable.
type orphanSweeper interface {
	DomainsCount() (int, error)
	OrphanCounts() (map[string]int, error)
	OrphanRecordNames() ([]string, error)
	ReapOrphans() (map[string]int, error)
}

// reapOrphansClient returns the sweeper the handler uses, or nil when the pdns
// backend is unavailable. Overridable in tests. It must return an explicit nil
// interface (not a typed nil *pdns.Client) so the handler's nil check works.
var reapOrphansClient = func() orphanSweeper {
	if cl := pdns.Default(); cl != nil {
		return cl
	}
	return nil
}

func dnsReapOrphansHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dnsReapOrphansParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	cl := reapOrphansClient()
	if cl == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "powerdns backend not available"}
	}

	// Safety floor: an empty domains table would make the orphan predicate
	// (`domain_id NOT IN (SELECT id FROM domains)`) true for EVERY record, so a
	// misconfigured or mid-provision connection could wipe the whole backend.
	// Refuse outright — the DNS analogue of php.pool.reap-orphans refusing a nil
	// keep-list. A box with zero domains has nothing legitimate to reap anyway.
	domains, err := cl.DomainsCount()
	if err != nil {
		return nil, err
	}
	if domains == 0 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: "refusing to sweep: the pdns domains table is empty, which would make every record look orphaned — check the PowerDNS DB connection",
		}
	}

	counts, err := cl.OrphanCounts()
	if err != nil {
		return nil, err
	}
	// Collect affected names BEFORE the delete — afterwards the rows are gone.
	names, err := cl.OrphanRecordNames()
	if err != nil {
		return nil, err
	}

	if !p.Apply {
		return dnsReapOrphansResponse{Applied: false, Counts: counts, Deleted: map[string]int{}, Names: names}, nil
	}

	deleted, err := cl.ReapOrphans()
	if err != nil {
		return nil, err
	}
	// Purge caches for each affected name. The row is gone from SQL but the pdns
	// Auth packet cache and the recursor's forward cache would keep serving the
	// stale answer for cache-ttl otherwise (GH #1620 "keeps resolving"). Same
	// per-name purge dns.zone.delete does; best-effort. No NOTIFY — an orphan has
	// no zone, so there is nothing to notify slaves about.
	for _, n := range names {
		_ = execCommandContext(ctx, "pdns_control", "purge", n+"$").Run()
		_ = execCommandContext(ctx, "rec_control", "wipe-cache", n+"$").Run()
	}
	return dnsReapOrphansResponse{Applied: true, Counts: counts, Deleted: deleted, Names: names}, nil
}

func init() {
	Default.Register("dns.reap-orphans", dnsReapOrphansHandler)
}
