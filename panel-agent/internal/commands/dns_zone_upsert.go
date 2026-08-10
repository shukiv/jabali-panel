package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/pdns"
)

type dnsRecordParam struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
	Disabled bool   `json:"disabled"`
}

type dnsZoneUpsertParams struct {
	// Zone is the apex name, no trailing dot. "example.com".
	Zone          string           `json:"zone"`
	Records       []dnsRecordParam `json:"records"`
	AllowAXFRFrom []string         `json:"allow_axfr_from,omitempty"`
	AlsoNotify    []string         `json:"also_notify,omitempty"`
}

type dnsZoneUpsertResponse struct {
	Zone    string `json:"zone"`
	ZoneID  int64  `json:"zone_id"`
	Records int    `json:"records"`
}

func dnsZoneUpsertHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dnsZoneUpsertParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	if p.Zone == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "zone required"}
	}

	// Validate all records before touching the database.
	recs := make([]pdns.Record, 0, len(p.Records))
	for i, r := range p.Records {
		if r.Name == "" {
			return nil, fmt.Errorf("record %d: name required", i)
		}
		if r.Type == "" {
			return nil, fmt.Errorf("record %d: type required", i)
		}
		if r.TTL <= 0 {
			r.TTL = 3600
		}
		recs = append(recs, pdns.Record{
			Name:     r.Name,
			Type:     r.Type,
			Content:  r.Content,
			TTL:      r.TTL,
			Priority: r.Priority,
			Disabled: r.Disabled,
		})
	}

	// Panel is expected to send the full desired record set; we don't
	// reject empty. An empty zone still has its SOA — if panel sent
	// nothing it's likely a bug, but that's panel's problem to catch.
	cl := pdns.Default()
	if cl == nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "powerdns backend not available (install.sh may not have run install_powerdns)",
		}
	}

	opts := pdns.UpsertZoneOptions{
		AllowAXFRFrom: p.AllowAXFRFrom,
		AlsoNotify:    p.AlsoNotify,
	}
	zoneID, err := cl.UpsertZoneWithMeta(p.Zone, recs, opts)
	if err != nil {
		return nil, err
	}

	// Signal PowerDNS:
	//   0. rediscover — refresh pdns-auth's cached LIST of known zones so a
	//      brand-new zone becomes servable IMMEDIATELY. jabali sets
	//      zone-cache-refresh-interval=10, so pdns caches which zones it hosts
	//      and won't answer for a just-inserted one until the next refresh —
	//      measured ~20s of NXDOMAIN from the authoritative server itself for a
	//      freshly-created (sub)domain (GH #896: dead page + certbot NXDOMAIN in
	//      that window; verified `pdns_control help` lists `rediscover: discover
	//      any new zones` on the installed pdns-auth). purge (below) only clears
	//      the RECORD/packet cache, not the zone-list cache — so rediscover must
	//      run first, else purge can't help a zone the server still thinks
	//      doesn't exist. Cheap no-op when nothing new was added.
	//   1. purge <zone>$ — invalidate the in-process query/packet cache
	//      for this zone (the '$' suffix targets the zone + all names
	//      under it). Without this, pdns serves the OLD answer from
	//      cache for the next cache-ttl seconds (default 20s) AND/OR
	//      the record TTL — operators see stale dig answers even after
	//      the SQL backend has the new value (incident 2026-05-21:
	//      user updated autoconfig CNAME via panel UI, dig at ns1 still
	//      returned old value 3h later because the cache entry was
	//      kept-alive by repeated queries and never evicted).
	//   2. notify <zone> — tells slave NSes to AXFR. Cheap no-op when
	//      there are no slaves.
	// All best-effort: if pdns_control isn't reachable, the SQL change is
	// still committed and a real pdns restart will pick up the change.
	_ = exec.CommandContext(ctx, "pdns_control", "rediscover").Run()
	_ = exec.CommandContext(ctx, "pdns_control", "purge", p.Zone+"$").Run()
	// Also wipe pdns-recursor cache — its forward-cached answer
	// will outlast the Auth purge otherwise (incident 2026-05-21:
	// dig still returned old CNAME after panel-edit even after pdns
	// Auth purge; recursor held the cached forward response).
	_ = exec.CommandContext(ctx, "rec_control", "wipe-cache", p.Zone+"$").Run()
	_ = exec.CommandContext(ctx, "pdns_control", "notify", p.Zone).Run()

	return dnsZoneUpsertResponse{Zone: p.Zone, ZoneID: zoneID, Records: len(recs)}, nil
}

func init() {
	Default.Register("dns.zone.upsert", dnsZoneUpsertHandler)
}
