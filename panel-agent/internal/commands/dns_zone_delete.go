package commands

import (
	"context"
	"encoding/json"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/pdns"
)

type dnsZoneDeleteParams struct {
	Zone string `json:"zone"`
}

type dnsZoneDeleteResponse struct {
	Zone    string `json:"zone"`
	Deleted bool   `json:"deleted"`
}

func dnsZoneDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dnsZoneDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	if p.Zone == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "zone required"}
	}
	cl := pdns.Default()
	if cl == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "powerdns backend not available"}
	}
	if err := cl.DeleteZone(p.Zone); err != nil {
		return nil, err
	}
	// purge <zone>$ — flush pdns Auth in-process query cache for this
	// zone + all names under it. Without this, NXDOMAIN-after-delete
	// returns the cached-PRE-delete answer for cache-ttl seconds (or
	// longer if the entry stays hot). Companion fix to dns.zone.upsert
	// (PR #86 incident: stale CNAME served 3h after edit).
	_ = execCommandContext(ctx, "pdns_control", "purge", p.Zone+"$").Run()
	// Also wipe pdns-recursor cache — its forward-cached answer
	// will outlast the Auth purge otherwise (incident 2026-05-21:
	// dig still returned old CNAME after panel-edit even after pdns
	// Auth purge; recursor held the cached forward response).
	_ = execCommandContext(ctx, "rec_control", "wipe-cache", p.Zone+"$").Run()
	// NOTIFY so any slaves drop their cached copy.
	_ = execCommandContext(ctx, "pdns_control", "notify", p.Zone).Run()
	return dnsZoneDeleteResponse{Zone: p.Zone, Deleted: true}, nil
}

func init() {
	Default.Register("dns.zone.delete", dnsZoneDeleteHandler)
}
