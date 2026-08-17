package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/pdns"
)

// dnsDNSSECEnableParams is the request shape for dns.dnssec_enable.
// Secures the zone, enables NSEC3 with PowerDNS defaults, rectifies, and
// returns the current key set. Idempotent if the zone is already signed —
// ADR-0057.
type dnsDNSSECEnableParams struct {
	DomainName string `json:"domain_name"`
}

type dnsDNSSECKeyOut struct {
	KeyTag    int    `json:"key_tag"`
	KeyType   string `json:"key_type"`
	Algorithm uint8  `json:"algorithm"`
	PublicKey string `json:"public_key"`
	Active    bool   `json:"active"`
}

type dnsDNSSECEnableResponse struct {
	Ok   bool              `json:"ok"`
	Keys []dnsDNSSECKeyOut `json:"keys"`
}

func dnsDNSSECEnableHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p dnsDNSSECEnableParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.DomainName == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "domain_name required"}
	}
	keys, err := pdns.SecureZone(ctx, p.DomainName)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	// Flush pdns Auth cache so the next query for any name in this
	// zone reads fresh-DNSSEC-signed records from the backend. Without
	// this, queries continue to return un-signed answers from cache
	// until cache-ttl evicts (same class of bug as PRs #86/#87).
	_ = execCommandContext(ctx, "pdns_control", "purge", p.DomainName+"$").Run()
	// Also wipe pdns-recursor cache — its forward-cached answer
	// will outlast the Auth purge otherwise (incident 2026-05-21:
	// dig still returned old CNAME after panel-edit even after pdns
	// Auth purge; recursor held the cached forward response).
	_ = execCommandContext(ctx, "rec_control", "wipe-cache", p.DomainName+"$").Run()
	out := make([]dnsDNSSECKeyOut, 0, len(keys))
	for _, k := range keys {
		out = append(out, dnsDNSSECKeyOut{
			KeyTag:    k.KeyTag,
			KeyType:   k.KeyType,
			Algorithm: k.Algorithm,
			PublicKey: k.PublicKey,
			Active:    k.Active,
		})
	}
	return dnsDNSSECEnableResponse{Ok: true, Keys: out}, nil
}

func init() {
	Default.Register("dns.dnssec_enable", dnsDNSSECEnableHandler)
}
