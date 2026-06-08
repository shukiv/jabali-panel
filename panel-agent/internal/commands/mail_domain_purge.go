package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.linux-hosting.co.il/shukivaknin/jabali2/agentwire"
)

// mailDomainPurgeParams is the input for mail.domain.purge_accounts.
type mailDomainPurgeParams struct {
	Domain string `json:"domain"`
}

type mailDomainPurgeResult struct {
	Destroyed int `json:"destroyed"`
}

// mailDomainPurgeHandler destroys EVERY Stalwart registry Account under a
// domain, by querying x:Account/query on the domain's registry id rather
// than per-email. This is the reliable user-delete cleanup path: the
// per-mailbox mailbox.delete only fires for mailboxes that still have a
// panel row, so an account whose row was already removed (a failed prior
// delete, or a migration that pushed mail to Stalwart without a matching
// row) survived as an orphan and blocked re-creating/re-migrating that
// address with primaryKeyViolation on email. Querying by domain catches
// those orphans too.
//
// A domain belongs to exactly one panel user, so purging all accounts
// under it during that user's delete is correct — there are no other
// tenants' accounts in the domain. Idempotent: if the domain isn't in
// the registry (nobody ever authed) it's a no-op.
func mailDomainPurgeHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p mailDomainPurgeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.Domain == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "domain required"}
	}

	targetID, err := domainIDByName(ctx, p.Domain)
	if err != nil {
		return nil, err
	}
	if targetID == "" {
		// Domain never entered the registry → no accounts to purge.
		return mailDomainPurgeResult{Destroyed: 0}, nil
	}

	// Enumerate ALL accounts (no filter) and match domainId CLIENT-SIDE.
	// Do NOT use `x:Account/query filter:{domainId}` — that filter is
	// honoured on some Stalwart builds but silently returns 0 on others
	// (cf. accountIDByEmail's "supported columns" caveat), which left the
	// orphan surviving every delete. The unfiltered query + x:Account/get
	// + domainIDByName are all proven-working primitives (the same ones
	// the per-email `jabali mailbox delete` path uses).
	var q jmapQueryResult
	if err := jmapCall(ctx, "x:Account/query", map[string]any{"limit": 100000}, &q); err != nil {
		return nil, err
	}
	if len(q.IDs) == 0 {
		return mailDomainPurgeResult{Destroyed: 0}, nil
	}

	var got jmapGetResult
	if err := jmapCall(ctx, "x:Account/get", map[string]any{
		"ids":        q.IDs,
		"properties": []string{"domainId"},
	}, &got); err != nil {
		return nil, err
	}

	destroyed := 0
	for _, raw := range got.List {
		var acct struct {
			ID       string `json:"id"`
			DomainID string `json:"domainId"`
		}
		if jErr := json.Unmarshal(raw, &acct); jErr != nil {
			continue
		}
		if acct.DomainID != targetID {
			continue
		}
		if err := accountDestroy(ctx, acct.ID); err != nil {
			// Best-effort: skip a failed destroy, keep going. Partial
			// purge is non-fatal (orphan recoverable; blocked delete worse).
			continue
		}
		destroyed++
	}
	return mailDomainPurgeResult{Destroyed: destroyed}, nil
}

func init() {
	Default.Register("mail.domain.purge_accounts", mailDomainPurgeHandler)
}
