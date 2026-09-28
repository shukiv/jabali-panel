package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mailboxDeleteParams is the request shape for mailbox.delete.
//
// Unlike mailbox.create (which is a no-op in v0.16 — ADR-0045), delete
// requires an active Stalwart call: deleting the mailboxes row makes
// new auths fail, but any registry Account record Stalwart synced on a
// prior auth survives as a ghost until we explicitly destroy it.
//
// Flow:
//  1. Resolve the registry id via Account/query on the email.
//  2. If no registry record (never authed → nothing synced), ack ok.
//  3. Otherwise Account/set destroy the record.
//
// Mail data (RocksDB blobs for the deleted user's messages) is NOT
// touched here — Stalwart garbage-collects orphan storage on its own
// schedule. Immediate reclamation is an operator task via the webadmin.
type mailboxDeleteParams struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func mailboxDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p mailboxDeleteParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.ID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "id parameter required"}
	}
	email, err := requireEmail(p.Email)
	if err != nil {
		return nil, err
	}

	accountID, err := accountIDByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if accountID == "" {
		// Never synced into Stalwart's registry (no prior auth).
		// Nothing to destroy.
		return okBody{Ok: true}, nil
	}
	if err := accountDestroy(ctx, accountID); err != nil {
		return nil, err
	}
	return okBody{Ok: true}, nil
}

func init() {
	Default.Register("mailbox.delete", flushesMailAuthCache(mailboxDeleteHandler))
}
