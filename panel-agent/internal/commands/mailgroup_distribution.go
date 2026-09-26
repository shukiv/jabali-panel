package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mailgroup_distribution.go — GH #1818 / #1834. Project a DISTRIBUTION mail
// group so mail sent to it reaches every member.
//
// The M51 projection (a Stalwart @type:Group account) is right for a resource
// group — members share its inbox, calendar and files through memberGroupIds —
// but wrong for a distribution list: Stalwart delivers to the Group account's
// own inbox and nobody sees it (the panel never links distribution members to
// the group). Expanding the SQL directory's queryRecipient does not help either:
// Stalwart treats it as an existence check, not an expansion list (verified on
// Stalwart 0.16.15). So a distribution group is projected as:
//
//   - internal_only=false → a native x:MailingList whose recipients are the
//     member addresses. Stalwart fans the message out itself.
//   - internal_only=true  → a Group account whose one active Sieve rejects a
//     sender outside the domain and otherwise redirects to each member. A
//     MailingList has no sender restriction, so the Sieve carries both rules.
//     Stalwart's SieveUserInterpreter allows 20 redirects per message, hence
//     maxInternalOnlyListMembers.
//   - no members          → nothing at the address, so senders get a 550 at RCPT
//     instead of mail that is accepted and silently dropped.
//
// A Group account left at the address by the old projection may hold mail the
// members never received. Before that account is removed (or reused for the
// internal-only shape), its mail is copied into every member's inbox and only
// then deleted from the group, so a failed copy never loses mail.

const (
	groupKindDistribution = "distribution"
	groupKindResource     = "resource"

	// maxInternalOnlyListMembers is Stalwart's default SieveUserInterpreter
	// maxRedirects. An internal-only list delivers with one Sieve redirect per
	// member, so members past this bound would silently get nothing. The panel
	// refuses a longer internal-only list; the agent enforces the same bound.
	maxInternalOnlyListMembers = 20

	// groupMailDrainPage is the Email/query page size used while copying a
	// group account's stored mail to the members.
	groupMailDrainPage = 50
	// groupMailDrainMaxPages bounds the drain loop so a mail server that keeps
	// returning the same messages fails the apply instead of spinning.
	groupMailDrainMaxPages = 10000

	jmapCapMail = "urn:ietf:params:jmap:mail"
)

// applyDistributionGroup converges the Stalwart side of one distribution group
// to the desired shape (see the file comment). Idempotent: every step checks
// the current state first, so the panel can re-send the same apply.
func applyDistributionGroup(ctx context.Context, email string, p mailGroupApplyParams) (any, error) {
	members, err := normalizeMemberEmails(p.MemberEmails)
	if err != nil {
		return nil, err
	}
	if p.InternalOnly && len(members) > maxInternalOnlyListMembers {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("an internal-only distribution list supports at most %d members (got %d)",
				maxInternalOnlyListMembers, len(members)),
		}
	}

	// Serialise applies for the same address: a panel save racing a reconcile
	// pass must not interleave the copy → destroy → create sequence.
	unlock := lockMailGroup(email)
	defer unlock()

	wantList := len(members) > 0 && !p.InternalOnly
	wantGroup := len(members) > 0 && p.InternalOnly
	description := groupDescription(p)

	// 1. A Group account at the address holds mail the members never saw.
	// Copy it to them first; then remove the account unless the internal-only
	// shape reuses it.
	gid, err := accountIDByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if gid != "" {
		if err := drainGroupMailToMembers(ctx, gid, members); err != nil {
			return nil, err
		}
		if !wantGroup {
			if err := unlinkGroupMembers(ctx, gid, members); err != nil {
				return nil, err
			}
			if err := accountDestroy(ctx, gid); err != nil {
				return nil, err
			}
		}
	}

	// 2. The native MailingList: create or replace its recipients, or remove it
	// when the address must not fan out through it.
	listID, err := mailingListIDByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if wantList {
		id, err := upsertMailingList(ctx, email, listID, members, description)
		if err != nil {
			return nil, err
		}
		return mailGroupApplyResult{Ok: true, GroupID: id}, nil
	}
	if listID != "" {
		if err := mailingListDestroy(ctx, listID); err != nil {
			return nil, err
		}
	}
	if !wantGroup {
		return mailGroupApplyResult{Ok: true}, nil
	}

	// 3. Internal-only: a Group account runs the combined reject + redirect
	// Sieve. The address is free now (any MailingList was removed above).
	gid, err = ensureGroupInRegistry(ctx, email, description)
	if err != nil {
		return nil, err
	}
	script := buildInternalOnlyListSieve(email[strings.LastIndex(email, "@")+1:], members)
	blobID, err := uploadSieveBlob(ctx, gid, script)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("sieve blob upload: %v", err)}
	}
	if blobID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "sieve blob upload returned no blobId"}
	}
	if _, err := setActiveNamedScript(ctx, gid, groupInternalOnlyScriptName, blobID); err != nil {
		return nil, err
	}
	return mailGroupApplyResult{Ok: true, GroupID: gid}, nil
}

// normalizeMemberEmails validates, lower-cases, de-duplicates and sorts the
// member addresses. requireEmail rejects quotes, backslashes and whitespace, so
// every address is safe to place inside a quoted Sieve string.
func normalizeMemberEmails(raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		e, err := requireEmail(r)
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("member %q: %v", r, err)}
		}
		if at := strings.LastIndex(e, "@"); at <= 0 || at == len(e)-1 {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("member %q: malformed address", r)}
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out, nil
}

// groupDescription is the label stored on the Stalwart object: the display
// name, falling back to the admin description.
func groupDescription(p mailGroupApplyParams) string {
	if n := strings.TrimSpace(p.DisplayName); n != "" {
		return n
	}
	return strings.TrimSpace(p.Description)
}

// buildInternalOnlyListSieve renders the one active script of an internal-only
// distribution list: reject a sender outside the domain (and stop, so no
// redirect runs), otherwise redirect to every member. redirect cancels the
// implicit keep, so nothing collects in the group's own inbox.
func buildInternalOnlyListSieve(domain string, members []string) string {
	var b strings.Builder
	b.WriteString("require [\"envelope\",\"reject\"];\n")
	fmt.Fprintf(&b, "if not envelope :domain :is \"from\" \"%s\" {\n", domain)
	b.WriteString("  reject \"550 5.7.1 This address only accepts mail from within the domain.\";\n")
	b.WriteString("  stop;\n")
	b.WriteString("}\n")
	for _, m := range members {
		fmt.Fprintf(&b, "redirect \"%s\";\n", m)
	}
	return b.String()
}

// --- stored-mail drain ---------------------------------------------------

type drainTarget struct {
	accountID string
	inboxID   string
}

// drainGroupMailToMembers copies every message stored in the group account
// into each member's inbox, then deletes it from the group. A message is
// deleted only after every member holds it (a copy the member already has is
// reported alreadyExists and counts as done), so a failure part-way leaves the
// remaining mail in the group for the next apply.
func drainGroupMailToMembers(ctx context.Context, groupAcctID string, members []string) error {
	ids, total, err := groupMailPage(ctx, groupAcctID)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	if len(members) == 0 {
		return &agentwire.AgentError{
			Code: agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("the group mailbox still holds %d message(s) and the list has no members to receive them; add a member and apply again",
				total),
		}
	}
	targets := make([]drainTarget, 0, len(members))
	for _, m := range members {
		acctID, err := resolveOrEnsureAccount(ctx, m)
		if err != nil {
			return err
		}
		inboxID, err := inboxMailboxID(ctx, acctID)
		if err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("find inbox of %s: %v", m, err)}
		}
		if inboxID == "" {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("member %s has no inbox yet", m)}
		}
		targets = append(targets, drainTarget{accountID: acctID, inboxID: inboxID})
	}
	for page := 0; len(ids) > 0; page++ {
		if page >= groupMailDrainMaxPages {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "group mail copy did not converge"}
		}
		for _, t := range targets {
			if err := copyEmailsTo(ctx, groupAcctID, t, ids); err != nil {
				return err
			}
		}
		if err := destroyEmails(ctx, groupAcctID, ids); err != nil {
			return err
		}
		if ids, _, err = groupMailPage(ctx, groupAcctID); err != nil {
			return err
		}
	}
	return nil
}

func groupMailPage(ctx context.Context, acctID string) ([]string, uint64, error) {
	args := map[string]any{"accountId": acctID, "limit": groupMailDrainPage, "calculateTotal": true}
	var res jmapQueryResult
	if err := jmapCallWith(ctx, jmapCapMail, "Email/query", args, &res); err != nil {
		return nil, 0, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("list group mail: %v", err)}
	}
	return res.IDs, res.Total, nil
}

func copyEmailsTo(ctx context.Context, fromAcctID string, t drainTarget, emailIDs []string) error {
	create := make(map[string]any, len(emailIDs))
	for i, id := range emailIDs {
		create["c"+strconv.Itoa(i)] = map[string]any{"id": id, "mailboxIds": map[string]bool{t.inboxID: true}}
	}
	args := map[string]any{"fromAccountId": fromAcctID, "accountId": t.accountID, "create": create}
	var res jmapSetResult
	if err := jmapCallWith(ctx, jmapCapMail, "Email/copy", args, &res); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("copy group mail: %v", err)}
	}
	for k, reason := range res.NotCreated {
		var r struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(reason, &r)
		if r.Type != "alreadyExists" {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("copy group mail %s refused: %s", k, string(reason))}
		}
	}
	return nil
}

func destroyEmails(ctx context.Context, acctID string, emailIDs []string) error {
	var res jmapSetResult
	if err := jmapCallWith(ctx, jmapCapMail, "Email/set", map[string]any{"accountId": acctID, "destroy": emailIDs}, &res); err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove copied group mail: %v", err)}
	}
	for id, reason := range res.NotDestroyed {
		var r struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(reason, &r)
		if r.Type != "notFound" {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("remove copied group mail %s refused: %s", id, string(reason))}
		}
	}
	return nil
}

// unlinkGroupMembers clears the memberGroupIds edge from the given members so
// Stalwart will destroy the group (it refuses while members are linked).
func unlinkGroupMembers(ctx context.Context, gid string, members []string) error {
	update := map[string]any{}
	addMemberPatch(ctx, update, gid, members, false)
	if len(update) == 0 {
		return nil
	}
	var res jmapSetResult
	return jmapCall(ctx, "x:Account/set", map[string]any{"update": update}, &res)
}

// --- x:MailingList -------------------------------------------------------

// mailingListIDByEmail returns the id of the MailingList at email, or "" when
// none exists. x:MailingList/query only filters on `text` (a token match on
// the list name), so the candidates are fetched and matched on emailAddress.
func mailingListIDByEmail(ctx context.Context, email string) (string, error) {
	localPart := email[:strings.LastIndex(email, "@")]
	var q jmapQueryResult
	if err := jmapCall(ctx, "x:MailingList/query", map[string]any{"filter": map[string]any{"text": localPart}}, &q); err != nil {
		return "", err
	}
	if len(q.IDs) == 0 {
		return "", nil
	}
	var g jmapGetResult
	if err := jmapCall(ctx, "x:MailingList/get", map[string]any{"ids": q.IDs}, &g); err != nil {
		return "", err
	}
	for _, raw := range g.List {
		var row struct {
			ID           string `json:"id"`
			EmailAddress string `json:"emailAddress"`
		}
		if err := json.Unmarshal(raw, &row); err == nil && strings.EqualFold(row.EmailAddress, email) {
			return row.ID, nil
		}
	}
	return "", nil
}

// upsertMailingList replaces the recipients (and label) of the existing list,
// or creates it. Returns the list id.
func upsertMailingList(ctx context.Context, email, listID string, members []string, description string) (string, error) {
	recipients := make(map[string]bool, len(members))
	for _, m := range members {
		recipients[m] = true
	}
	var desc any
	if description != "" {
		desc = description
	}
	if listID != "" {
		args := map[string]any{"update": map[string]any{
			listID: map[string]any{"recipients": recipients, "description": desc},
		}}
		var res jmapSetResult
		if err := jmapCall(ctx, "x:MailingList/set", args, &res); err != nil {
			return "", err
		}
		if reason, ok := res.NotUpdated[listID]; ok {
			return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("stalwart x:MailingList/set update refused: %s", string(reason))}
		}
		return listID, nil
	}

	at := strings.LastIndex(email, "@")
	domainID, err := ensureRegistryDomain(ctx, email[at+1:])
	if err != nil {
		return "", err
	}
	create := map[string]any{"name": email[:at], "domainId": domainID, "recipients": recipients}
	if desc != nil {
		create["description"] = desc
	}
	var res jmapSetResult
	if err := jmapCall(ctx, "x:MailingList/set", map[string]any{"create": map[string]any{"#l": create}}, &res); err != nil {
		return "", err
	}
	if reason, ok := res.NotCreated["#l"]; ok {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("stalwart x:MailingList/set create refused: %s", string(reason))}
	}
	if raw, ok := res.Created["#l"]; ok {
		var created struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(raw, &created)
		return created.ID, nil
	}
	return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "x:MailingList/set: no created/notCreated entry"}
}

// mailingListDestroy removes a MailingList. An already-gone id is fine.
func mailingListDestroy(ctx context.Context, id string) error {
	var res jmapSetResult
	if err := jmapCall(ctx, "x:MailingList/set", map[string]any{"destroy": []string{id}}, &res); err != nil {
		return err
	}
	if reason, ok := res.NotDestroyed[id]; ok {
		var r struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(reason, &r)
		if r.Type != "notFound" {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("stalwart x:MailingList/set destroy %s refused: %s", id, string(reason))}
		}
	}
	return nil
}

// ensureRegistryDomain returns the registry id of domainName, creating the
// x:Domain when it is not registered yet.
func ensureRegistryDomain(ctx context.Context, domainName string) (string, error) {
	domainID, err := domainIDByName(ctx, domainName)
	if err != nil {
		return "", err
	}
	if domainID != "" {
		return domainID, nil
	}
	return createDomain(ctx, domainName)
}

// mailGroupLocks serialises distribution-group applies per group address.
// Keyed by address; the mutex is created lazily and kept for process lifetime
// (one per group that was ever applied).
var mailGroupLocks sync.Map // map[string]*sync.Mutex

func lockMailGroup(email string) func() {
	m, _ := mailGroupLocks.LoadOrStore(email, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
