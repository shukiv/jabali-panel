package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// stalwartMarkers are what install_stalwart leaves on a box with the mail
// module: the extracted binary directory. Not the systemd unit: every
// `jabali update` installed it, mail module or not, so on a box without mail
// it kept a deleted domain's teardown failing forever. A var so tests can
// point it at a temp dir.
var stalwartMarkers = []string{"/opt/stalwart"}

// mailServerAbsent reports whether this box has no mail server at all: the
// admin token does not exist AND no Stalwart marker exists. Only then is there
// nothing to purge. A missing token on a box that has Stalwart is a broken
// install, not an absent one, so it stays an error and the caller retries.
// Any stat error other than "does not exist" counts as present (fail closed).
func mailServerAbsent() bool {
	if _, err := stalwartAdminTokenFunc(); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	for _, p := range stalwartMarkers {
		if _, err := os.Lstat(p); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}

// mailDomainPurgeParams is the input for mail.domain.purge_accounts.
type mailDomainPurgeParams struct {
	Domain string `json:"domain"`
	// RemoveDomain, set when the domain itself is being deleted, also
	// destroys the domain's DKIM signatures and then the Stalwart domain.
	// Unset (a mail-only purge) keeps both so mail can be re-enabled.
	RemoveDomain bool `json:"remove_domain,omitempty"`
}

type mailDomainPurgeResult struct {
	Destroyed      int    `json:"destroyed"`
	DestroyedLists int    `json:"destroyed_lists,omitempty"`
	DestroyedDkim  int    `json:"destroyed_dkim,omitempty"`
	DomainRemoved  bool   `json:"domain_removed,omitempty"`
	DomainError    string `json:"domain_error,omitempty"`
	// DKIMKeysRemoved counts the domain's key files removed with it.
	DKIMKeysRemoved int `json:"dkim_keys_removed,omitempty"`
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
//
// Accounts are destroyed in passes: Stalwart refuses to destroy a Group
// account while a member still links to it (objectIsLinked), so a group that
// comes before its members is retried once they are gone.
//
// With remove_domain (the domain itself is being deleted), the domain's DKIM
// signatures and then the Stalwart domain are destroyed too. Left behind, the
// domain kept the old owner's settings — catch-all address, DKIM key — for
// whoever adds the same name next. A domain that still cannot be destroyed is
// reported in the result, not returned as an error, so a leftover Stalwart
// object never blocks the panel's delete.
func mailDomainPurgeHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p mailDomainPurgeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.Domain == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "domain required"}
	}
	// GH #357: a box installed without the mail module has no Stalwart, so
	// there are no accounts to purge. Say so with a distinct answer the
	// panel's teardown accepts as done; before, the token read failed as an
	// internal error and a deleted domain's teardown retried forever.
	if mailServerAbsent() {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: agentwire.MsgMailServerNotInstalled}
	}

	targetID, err := domainIDByName(ctx, p.Domain)
	if err != nil {
		return nil, err
	}
	if targetID == "" {
		// Domain never entered the registry → no accounts to purge.
		res := mailDomainPurgeResult{Destroyed: 0}
		if p.RemoveDomain {
			res.DKIMKeysRemoved = removeDomainKeyFiles(p.Domain)
		}
		return res, nil
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
	// No account on the server leaves nothing to destroy here, but the lists
	// and, for a removed domain, its signatures, the domain and its keys still
	// go.
	var pending []string
	if len(q.IDs) > 0 {
		var got jmapGetResult
		if err := jmapCall(ctx, "x:Account/get", map[string]any{
			"ids":        q.IDs,
			"properties": []string{"domainId"},
		}, &got); err != nil {
			return nil, err
		}
		for _, raw := range got.List {
			var acct struct {
				ID       string `json:"id"`
				DomainID string `json:"domainId"`
			}
			if jErr := json.Unmarshal(raw, &acct); jErr != nil {
				continue
			}
			if acct.DomainID == targetID {
				pending = append(pending, acct.ID)
			}
		}
	}
	res := mailDomainPurgeResult{Destroyed: destroyAccountsInPasses(ctx, pending)}

	lists, err := purgeDomainMailingLists(ctx, targetID)
	if err != nil {
		return nil, err
	}
	res.DestroyedLists = lists

	if p.RemoveDomain {
		var sigsGone bool
		res.DestroyedDkim, sigsGone, res.DomainRemoved, res.DomainError = removeRegistryDomain(ctx, targetID)
		// The key files go once no signature signs with them. Left behind,
		// the next domain of the same name would be enabled with the old
		// owner's key (enable reuses a key file it finds).
		if sigsGone {
			res.DKIMKeysRemoved = removeDomainKeyFiles(p.Domain)
		}
	}
	return res, nil
}

// removeDomainKeyFiles removes a deleted domain's key files and returns how
// many it removed. A name that is not a plain domain name is skipped: it
// becomes part of a path. A file that cannot be removed is logged; the
// delete goes on.
func removeDomainKeyFiles(domain string) int {
	if !domainRegex.MatchString(domain) {
		slog.Warn("mail purge: not removing DKIM key files for an invalid domain name", "domain", domain)
		return 0
	}
	n, errs := removeDKIMKeyFiles(domain)
	for _, e := range errs {
		slog.Warn("mail purge: could not remove a DKIM key file", "domain", domain, "err", e)
	}
	return n
}

// destroyAccountsInPasses destroys each account, retrying the ones Stalwart
// refused for as long as a pass makes progress (a Group account becomes
// destroyable once its members are gone). Best-effort: an account that still
// cannot be destroyed is skipped — a partial purge is recoverable, a blocked
// delete is worse. Returns how many were destroyed.
func destroyAccountsInPasses(ctx context.Context, ids []string) int {
	destroyed := 0
	for len(ids) > 0 {
		var refused []string
		for _, id := range ids {
			if err := accountDestroy(ctx, id); err != nil {
				refused = append(refused, id)
				continue
			}
			destroyed++
		}
		if len(refused) == len(ids) {
			break // no progress this pass
		}
		ids = refused
	}
	return destroyed
}

// removeRegistryDomain destroys the domain's DKIM signatures, then the domain
// itself. It never returns an error: the outcome goes into the purge result.
// sigsGone reports that the domain has no DKIM signature left.
func removeRegistryDomain(ctx context.Context, domainID string) (dkimDestroyed int, sigsGone, removed bool, reason string) {
	sigs, err := dkimSignatureIDs(ctx, domainID, "")
	if err != nil {
		return 0, false, false, "list DKIM signatures: " + err.Error()
	}
	if len(sigs) > 0 {
		var result jmapSetResult
		if err := jmapCall(ctx, "x:DkimSignature/set", map[string]any{"destroy": sigs}, &result); err != nil {
			return 0, false, false, "destroy DKIM signatures: " + err.Error()
		}
		dkimDestroyed = len(result.Destroyed)
	}
	sigsGone = dkimDestroyed == len(sigs)
	if err := domainDestroy(ctx, domainID); err != nil {
		return dkimDestroyed, sigsGone, false, err.Error()
	}
	return dkimDestroyed, sigsGone, true, ""
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
	Default.Register("mail.domain.purge_accounts", flushesMailAuthCache(mailDomainPurgeHandler))
}
