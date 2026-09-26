package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mailboxShareSetParams is the panel → agent request for mailbox.share_set.
//
// Converges JMAP Mailbox.shareWith for a given mailbox (root folder, id = "INBOX").
// Stalwart stores shareWith as map<PrincipalId, MailboxRights>; we write the
// whole map each call (idempotent replace).
type mailboxShareSetParams struct {
	OwnerEmail string            `json:"owner_email"`
	Shares     map[string]Rights `json:"shares"` // targetPrincipalId → rights
}

// Rights mirrors panel-api models.Rights (same JSON keys).
type Rights struct {
	MayRead        bool `json:"mayRead,omitempty"`
	MayAddItems    bool `json:"mayAddItems,omitempty"`
	MayRemoveItems bool `json:"mayRemoveItems,omitempty"`
	MayCreateChild bool `json:"mayCreateChild,omitempty"`
	MayRename      bool `json:"mayRename,omitempty"`
	MayDelete      bool `json:"mayDelete,omitempty"`
	MayAdmin       bool `json:"mayAdmin,omitempty"`
	MaySubmit      bool `json:"maySubmit,omitempty"`
}

// toStalwartACL maps the internal Rights (jabali's own JSON keys, shared with
// panel-api models.Rights + the UI) to the exact permission keys Stalwart's
// Mailbox/shareWith expects. Two differed and silently broke every share:
// jabali "mayRead" is Stalwart "mayReadItems", and jabali "mayAdmin" is
// Stalwart "mayShare" — Stalwart rejects the originals as invalidProperties.
// Only true permissions are emitted (a false permission is simply absent).
func (r Rights) toStalwartACL() map[string]bool {
	acl := map[string]bool{}
	if r.MayRead {
		acl["mayReadItems"] = true
	}
	if r.MayAddItems {
		acl["mayAddItems"] = true
	}
	if r.MayRemoveItems {
		acl["mayRemoveItems"] = true
	}
	if r.MayCreateChild {
		acl["mayCreateChild"] = true
	}
	if r.MayRename {
		acl["mayRename"] = true
	}
	if r.MayDelete {
		acl["mayDelete"] = true
	}
	if r.MayAdmin {
		acl["mayShare"] = true
	}
	if r.MaySubmit {
		acl["maySubmit"] = true
	}
	return acl
}

type mailboxShareSetResponse struct {
	Ok bool `json:"ok"`
}

func mailboxShareSetHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p mailboxShareSetParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if _, err := requireEmail(p.OwnerEmail); err != nil {
		return nil, err
	}

	acctID, err := accountIDByEmail(ctx, p.OwnerEmail)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve account: %v", err)}
	}
	if acctID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "owner mailbox not yet registered"}
	}

	// Resolve target principals by email → id.
	targetIDs := make(map[string]map[string]bool, len(p.Shares))
	for targetEmail, rights := range p.Shares {
		tid, err := accountIDByEmail(ctx, targetEmail)
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve target %s: %v", targetEmail, err)}
		}
		if tid == "" {
			continue // not yet registered; skip silently
		}
		targetIDs[tid] = rights.toStalwartACL()
	}

	// Find INBOX mailbox id under this account. If we can't, the account has
	// no mailbox yet — bail out; reconciler will retry.
	inboxID, err := inboxMailboxID(ctx, acctID)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve inbox: %v", err)}
	}
	if inboxID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "owner has no INBOX yet"}
	}

	args := map[string]any{
		"accountId": acctID,
		"update": map[string]any{
			inboxID: map[string]any{
				"shareWith": targetIDs,
			},
		},
	}
	var result jmapSetResult
	if err := jmapCallWith(ctx, jmapCapMail, "Mailbox/set", args, &result); err != nil {
		return nil, err
	}
	if reason, ok := result.NotUpdated[inboxID]; ok {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("Mailbox/set refused: %s", string(reason))}
	}
	return mailboxShareSetResponse{Ok: true}, nil
}

// inboxMailboxID resolves the owner account's INBOX Mailbox id via JMAP filter.
//
// Mailbox/query (like Mailbox/set above) is a mail-capability method: without
// urn:ietf:params:jmap:mail in "using", Stalwart answers unknownMethod, so the
// request carries jmapCapMail.
func inboxMailboxID(ctx context.Context, accountID string) (string, error) {
	args := map[string]any{
		"accountId": accountID,
		"filter":    map[string]any{"role": "inbox"},
		"limit":     1,
	}
	var result struct {
		IDs []string `json:"ids"`
	}
	if err := jmapCallWith(ctx, jmapCapMail, "Mailbox/query", args, &result); err != nil {
		return "", err
	}
	if len(result.IDs) == 0 {
		return "", nil
	}
	return result.IDs[0], nil
}

func init() {
	Default.Register("mailbox.share_set", mailboxShareSetHandler)
}
