package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mailboxSetPasswordParams is the request shape for mailbox.set_password.
//
// NB: this command does NOT carry the new password — the panel has
// already bcrypted it and written mailboxes.password_hash before calling
// us. Stalwart's SqlDirectory re-reads the hash on the next IMAP, POP3 or
// SMTP auth attempt, so there the new password is effective at once. Over
// HTTP (JMAP, webmail) Stalwart answers from its HTTP Authorization cache,
// so the OLD password kept working there (verified on 0.16.15); the verb is
// registered to flush that cache (mail_auth_cache.go).
//
// Plaintext never reaches the agent. That's the whole point of the
// post-review password model in ADR-0042 + plan §1 (two-column ->
// one-column bcrypt-only).
//
// Mid-session note: OAuth access tokens a client already holds are not
// revoked by a password change. Forced logout is a runbook escape-hatch via
// webadmin.
type mailboxSetPasswordParams struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

func mailboxSetPasswordHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p mailboxSetPasswordParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.ID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "id parameter required"}
	}
	if _, err := requireEmail(p.Email); err != nil {
		return nil, err
	}
	// Ensure the account is in Stalwart's JMAP registry (covers
	// mailboxes created pre-fix that have never authenticated).
	// Best-effort; DB row is authoritative (ADR-0045).
	_ = accountEnsureInRegistry(ctx, p.Email)
	return okBody{Ok: true}, nil
}

func init() {
	Default.Register("mailbox.set_password", flushesMailAuthCache(mailboxSetPasswordHandler))
}
