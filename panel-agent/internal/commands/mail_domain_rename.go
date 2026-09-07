package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mail.domain.rename carries a domain's mail across an in-place web-domain
// rename (GH #1579). Stalwart keys every account on (localpart, domainId) and
// stores its messages under that account's stable internal id, so renaming the
// registry Domain ENTITY in place — keeping its id — carries every account, its
// stored messages, and its (domainId-keyed) DKIM signature to the new address.
// The panel's DB trigger resyncs mailboxes.email_cached, so the SQL directory
// then authenticates + delivers at the new address. This is why a rename no
// longer has to refuse (and its old-name teardown purge) a domain that has mail.
//
// The verb is called twice by RenameDomain: once with dry_run=true BEFORE any
// file move, to catch a genuine conflict (the new name already carries mail)
// while nothing is committed; then for real AFTER the docroot move and BEFORE
// the DB rename, so the registry entity is named `new` before the trigger flips
// email_cached to `@new` (avoiding a window where the directory resolves an
// address whose domain the registry doesn't yet know).
type mailDomainRenameParams struct {
	Old    string `json:"old"`
	New    string `json:"new"`
	DryRun bool   `json:"dry_run"`
}

// mailDomainRenameResult reports the outcome. Status is one of:
//   - "renamed"          the registry Domain was renamed in place (real run).
//   - "already"          old is gone from the registry and new is present — the
//                        rename's Stalwart half is already done (idempotent
//                        retry); the caller proceeds to the DB rename.
//   - "not_in_registry"  neither name is in the registry — the domain has no
//                        Stalwart mail entity; the caller proceeds (the DB
//                        trigger alone carries any retained mailbox rows).
//   - "ok"               dry_run only: the rename would proceed (no conflict).
//   - "conflict"         both names carry mail (new has accounts) — the caller
//                        refuses; carrying mail into an occupied name is unsafe.
type mailDomainRenameResult struct {
	Status            string   `json:"status"`
	CatchallRewritten bool     `json:"catchall_rewritten"`
	Warnings          []string `json:"warnings,omitempty"`
}

// mailDomainNameRE is a defensive shape check on the names the verb sends to
// Stalwart. The panel already normalizes + validates, and JMAP filter values are
// JSON-encoded (no injection surface); this just rejects obvious garbage.
var mailDomainNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func mailDomainRenameHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p mailDomainRenameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	p.Old = strings.ToLower(strings.TrimSpace(p.Old))
	p.New = strings.ToLower(strings.TrimSpace(p.New))
	if p.Old == "" || p.New == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "old and new are required"}
	}
	if p.Old == p.New {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "old and new must differ"}
	}
	if !mailDomainNameRE.MatchString(p.Old) || !mailDomainNameRE.MatchString(p.New) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "old/new is not a valid domain name"}
	}

	oldID, err := domainIDByName(ctx, p.Old)
	if err != nil {
		return nil, err
	}
	newID, err := domainIDByName(ctx, p.New)
	if err != nil {
		return nil, err
	}

	// old already gone from the registry: the domain never had a Stalwart entity,
	// or a prior run already renamed it. If new is also present it must still be
	// checked for accounts — an EMPTY new is a harmless orphan (the panel's own
	// FindByName(new) gate guarantees no other panel domain owns `new`), but a
	// non-empty new holds another domain's mail. Proceeding would let the DB
	// trigger point this domain's retained mailboxes at `@new` and adopt those
	// accounts, so refuse (fail closed toward tenant isolation). The one case this
	// gives up is a mail-carrying rename whose DB flip failed AFTER a successful
	// carry (old renamed, new holds our accounts) — that retry now conflicts and
	// needs manual recovery; documented as a limitation.
	if oldID == "" {
		if newID == "" {
			return mailDomainRenameResult{Status: "not_in_registry"}, nil
		}
		n, cerr := countAccountsInDomain(ctx, newID)
		if cerr != nil {
			return nil, cerr
		}
		if n > 0 {
			return mailDomainRenameResult{Status: "conflict"}, nil
		}
		return mailDomainRenameResult{Status: "already"}, nil
	}

	// old is present. If new is ALSO present it is either an empty orphan left by
	// a prior domain delete (purge_accounts destroys accounts but not the Domain
	// entity — a common leftover) or a genuine, occupied name. Count accounts on
	// it with the reliable unfiltered enumeration (the `filter:{domainId}` form is
	// honoured on some Stalwart builds and silently returns 0 on others — trusting
	// it here could destroy a name that actually holds another tenant's mail).
	if newID != "" {
		n, cerr := countAccountsInDomain(ctx, newID)
		if cerr != nil {
			return nil, cerr
		}
		if n > 0 {
			return mailDomainRenameResult{Status: "conflict"}, nil
		}
	}

	if p.DryRun {
		return mailDomainRenameResult{Status: "ok"}, nil
	}

	// Real run. Clear an empty orphan on `new` first so the in-place rename does
	// not collide with a duplicate name.
	if newID != "" {
		if derr := domainDestroy(ctx, newID); derr != nil {
			return nil, derr
		}
	}

	// Rename the registry Domain entity in place — the load-bearing step. Keeping
	// its id is what carries the accounts, their message stores, and DKIM.
	if rerr := renameDomainEntity(ctx, oldID, p.New); rerr != nil {
		return nil, rerr
	}

	// Best-effort: rewrite a catch-all that pointed at an address on the old name.
	// It is a literal string on the Domain entity, so the in-place rename leaves
	// it stale. Safe to run here, BEFORE the caller commits the DB rename (while
	// the directory's email_cached is still @old): Stalwart validates the new
	// catchAllAddress against the x:Account it targets — which exists under the
	// just-renamed domain id — not against the SQL directory, so a not-yet-synced
	// directory does not reject it (box-verified). A failure here never fails the
	// rename (mail already carries); it comes back as a warning.
	res := mailDomainRenameResult{Status: "renamed"}
	rewritten, w := rewriteCatchAllOnRename(ctx, oldID, p.Old, p.New)
	res.CatchallRewritten = rewritten
	if w != "" {
		res.Warnings = append(res.Warnings, w)
	}
	return res, nil
}

// countAccountsInDomain counts registry Accounts whose domainId matches, using
// the same reliable enumeration as mail.domain.purge_accounts (unfiltered query
// + get + client-side match) rather than a server-side domainId filter.
func countAccountsInDomain(ctx context.Context, domainID string) (int, error) {
	var q jmapQueryResult
	if err := jmapCall(ctx, "x:Account/query", map[string]any{"limit": 100000}, &q); err != nil {
		return 0, err
	}
	if len(q.IDs) == 0 {
		return 0, nil
	}
	var got jmapGetResult
	if err := jmapCall(ctx, "x:Account/get", map[string]any{
		"ids":        q.IDs,
		"properties": []string{"domainId"},
	}, &got); err != nil {
		return 0, err
	}
	n := 0
	for _, raw := range got.List {
		var acct struct {
			DomainID string `json:"domainId"`
		}
		if jErr := json.Unmarshal(raw, &acct); jErr != nil {
			continue
		}
		if acct.DomainID == domainID {
			n++
		}
	}
	return n, nil
}

// renameDomainEntity renames a registry Domain in place via x:Domain/set update,
// keeping its id. A refused or missing update is a hard error (fail-closed: the
// caller has not committed the DB rename yet).
func renameDomainEntity(ctx context.Context, domainID, newName string) error {
	args := map[string]any{
		"update": map[string]any{
			domainID: map[string]any{"name": newName},
		},
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:Domain/set", args, &result); err != nil {
		return err
	}
	if _, ok := result.Updated[domainID]; ok {
		return nil
	}
	if reason, ok := result.NotUpdated[domainID]; ok {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart x:Domain/set rename refused: %s", string(reason)),
		}
	}
	return &agentwire.AgentError{
		Code:    agentwire.CodeInternal,
		Message: "stalwart x:Domain/set rename: neither updated nor notUpdated contained the id",
	}
}

// rewriteCatchAllOnRename reads the domain's catchAllAddress and, if it is an
// address on the OLD name (localpart@old), rewrites it to localpart@new. Returns
// (rewritten, warning). Any read/patch failure yields a warning, never an error —
// the catch-all is opt-in and non-fatal.
func rewriteCatchAllOnRename(ctx context.Context, domainID, oldName, newName string) (bool, string) {
	var got jmapGetResult
	if err := jmapCall(ctx, "x:Domain/get", map[string]any{
		"ids":        []string{domainID},
		"properties": []string{"catchAllAddress"},
	}, &got); err != nil {
		return false, fmt.Sprintf("could not read the catch-all address to update it for the new name: %v", err)
	}
	if len(got.List) == 0 {
		return false, ""
	}
	var d struct {
		CatchAllAddress *string `json:"catchAllAddress"`
	}
	if err := json.Unmarshal(got.List[0], &d); err != nil {
		return false, ""
	}
	if d.CatchAllAddress == nil || *d.CatchAllAddress == "" {
		return false, ""
	}
	suffix := "@" + oldName
	if !strings.HasSuffix(*d.CatchAllAddress, suffix) {
		// Catch-all points somewhere else (e.g. an external address) — leave it.
		return false, ""
	}
	newTarget := strings.TrimSuffix(*d.CatchAllAddress, suffix) + "@" + newName
	if err := updateDomainCatchall(ctx, domainID, newTarget); err != nil {
		return false, fmt.Sprintf("mail carried to the new name, but the catch-all address still points at the old name (update it manually to %q): %v", newTarget, err)
	}
	return true, ""
}

func init() {
	Default.Register("mail.domain.rename", mailDomainRenameHandler)
}
