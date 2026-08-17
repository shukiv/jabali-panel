package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/pdns"
)

// dnsDNSSECDisableParams is the request shape for dns.dnssec_disable.
type dnsDNSSECDisableParams struct {
	DomainName string `json:"domain_name"`
}

func dnsDNSSECDisableHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p dnsDNSSECDisableParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.DomainName == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "domain_name required"}
	}
	if err := pdns.DisableDNSSEC(ctx, p.DomainName); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	// Flush pdns Auth cache so the next query returns un-signed records
	// (signed answers cached pre-disable would otherwise persist for
	// cache-ttl seconds, same class of bug as PRs #86/#87).
	_ = execCommandContext(ctx, "pdns_control", "purge", p.DomainName+"$").Run()
	// Also wipe pdns-recursor cache — its forward-cached answer
	// will outlast the Auth purge otherwise (incident 2026-05-21:
	// dig still returned old CNAME after panel-edit even after pdns
	// Auth purge; recursor held the cached forward response).
	_ = execCommandContext(ctx, "rec_control", "wipe-cache", p.DomainName+"$").Run()
	return okBody{Ok: true}, nil
}

func init() {
	Default.Register("dns.dnssec_disable", dnsDNSSECDisableHandler)
}
