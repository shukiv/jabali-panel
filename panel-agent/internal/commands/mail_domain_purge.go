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

	domainID, err := domainIDByName(ctx, p.Domain)
	if err != nil {
		return nil, err
	}
	if domainID == "" {
		// Domain never entered the registry → no accounts to purge.
		return mailDomainPurgeResult{Destroyed: 0}, nil
	}

	// All accounts under the domain. The SQL directory accepts a filter
	// on domainId (see accountIDByEmail's note on supported columns).
	args := map[string]any{
		"filter": map[string]any{"domainId": domainID},
		"limit":  50000,
	}
	var result jmapQueryResult
	if err := jmapCall(ctx, "x:Account/query", args, &result); err != nil {
		return nil, err
	}

	destroyed := 0
	for _, id := range result.IDs {
		if err := accountDestroy(ctx, id); err != nil {
			// Best-effort: log-equivalent via the returned error would
			// abort the whole purge; instead skip the one that failed
			// and keep destroying the rest. The caller treats a partial
			// purge as non-fatal (orphan is recoverable, blocked delete
			// is worse).
			continue
		}
		destroyed++
	}
	return mailDomainPurgeResult{Destroyed: destroyed}, nil
}

func init() {
	Default.Register("mail.domain.purge_accounts", mailDomainPurgeHandler)
}
