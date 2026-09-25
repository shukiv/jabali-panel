package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mailDomainPurgeParams is the input for mail.domain.purge_accounts.
type mailDomainPurgeParams struct {
	Domain string `json:"domain"`
}

type mailDomainPurgeResult struct {
	Destroyed      int `json:"destroyed"`
	DestroyedLists int `json:"destroyed_lists,omitempty"`
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
	lists, err := purgeDomainMailingLists(ctx, targetID)
	if err != nil {
		return nil, err
	}
	return mailDomainPurgeResult{Destroyed: destroyed, DestroyedLists: lists}, nil
}

// purgeDomainMailingLists destroys every x:MailingList under the domain — the
// projection of a distribution mail group (GH #1818). A list left behind keeps
// its address live: mail to it is still accepted and fanned out, and a later
// mailbox at that address cannot be created. Same shape as the account purge:
// unfiltered query, domainId matched client-side, best-effort per list.
func purgeDomainMailingLists(ctx context.Context, domainID string) (int, error) {
	var q jmapQueryResult
	if err := jmapCall(ctx, "x:MailingList/query", map[string]any{}, &q); err != nil {
		return 0, err
	}
	if len(q.IDs) == 0 {
		return 0, nil
	}
	var got jmapGetResult
	if err := jmapCall(ctx, "x:MailingList/get", map[string]any{"ids": q.IDs}, &got); err != nil {
		return 0, err
	}
	destroyed := 0
	for _, raw := range got.List {
		var l struct {
			ID       string `json:"id"`
			DomainID string `json:"domainId"`
		}
		if jErr := json.Unmarshal(raw, &l); jErr != nil || l.DomainID != domainID {
			continue
		}
		if err := mailingListDestroy(ctx, l.ID); err != nil {
			continue
		}
		destroyed++
	}
	return destroyed, nil
}

func init() {
	Default.Register("mail.domain.purge_accounts", mailDomainPurgeHandler)
}
