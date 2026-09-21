package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// forwarderApplyParams is the panel → agent request for forwarder.apply.
//
// Idempotent apply of a mailbox's forwarders. Caller sends the full desired
// state; agent writes x:UserAccount.aliases for type=alias entries and a
// single concatenated x:SieveUserScript per mailbox for type=external
// entries (Stalwart allows only one active sieve script per account —
// schema SieveScript.isActive).
type forwarderApplyParams struct {
	MailboxEmail string              `json:"mailbox_email"`
	Aliases      []forwarderAlias    `json:"aliases"`   // local parts within the mailbox's own domain
	Externals    []forwarderExternal `json:"externals"` // external forward targets
}

// forwarderExternal is one external forward target. KeepCopy controls
// whether a copy is also delivered to the mailbox (Sieve redirect :copy)
// — GH #237's "Keep a copy of forwarded email in this mailbox".
type forwarderExternal struct {
	Target   string `json:"target"`
	KeepCopy bool   `json:"keep_copy"`
}

type forwarderAlias struct {
	LocalPart string `json:"local_part"`
	// DomainID is the Stalwart x:Domain id the alias belongs to. Resolved
	// agent-side from the email's domain.
}

type forwarderApplyResponse struct {
	Ok bool `json:"ok"`
}

func forwarderApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p forwarderApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if _, err := requireEmail(p.MailboxEmail); err != nil {
		return nil, err
	}

	acctID, err := accountIDByEmail(ctx, p.MailboxEmail)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve account: %v", err)}
	}
	if acctID == "" {
		// The Stalwart Principal is created lazily — on first auth or first
		// inbound delivery (JIT provisioning, ADR-0045). A forwarder added to a
		// mailbox that has done neither yet finds no accountId, and without this
		// the whole apply used to fail CodeNotFound: the caller swallows it
		// best-effort and there is NO periodic backstop (the reconciler
		// forwarders phase is dormant), so the redirect Sieve is never applied and
		// mail is delivered locally forever (GH #1795). Mirror autoresponder.set:
		// proactively provision the Principal (idempotent — alreadyExists is
		// success, and a missing registry Domain is created too) and retry.
		if ensureErr := accountEnsureInRegistry(ctx, p.MailboxEmail); ensureErr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("mailbox not registered and ensure failed: %v", ensureErr)}
		}
		acctID, err = accountIDByEmail(ctx, p.MailboxEmail)
		if err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve account after ensure: %v", err)}
		}
		if acctID == "" {
			return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "mailbox not registered with mail server"}
		}
	}

	// Resolve the account's domain id.
	at := strings.LastIndex(p.MailboxEmail, "@")
	domainName := p.MailboxEmail[at+1:]
	domainID, err := domainIDByName(ctx, domainName)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve domain: %v", err)}
	}
	if domainID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "domain not in Stalwart registry"}
	}

	// 1. Replace account aliases.
	if err := applyAccountAliases(ctx, acctID, domainID, p.Aliases); err != nil {
		return nil, err
	}

	// 2. Replace the concatenated sieve script for external forwards.
	if err := applyExternalSieve(ctx, acctID, p.Externals); err != nil {
		return nil, err
	}

	return forwarderApplyResponse{Ok: true}, nil
}

func applyAccountAliases(_ context.Context, _, _ string, _ []forwarderAlias) error {
	// No-op by design. jabali serves alias delivery from its SQL directory
	// (queryEmailAliases reads the email_forwarders table directly), and
	// Stalwart auto-creates the sending Identity from the directory too, so
	// the principal's `aliases` property is unnecessary for both receive
	// and send-as (GH #199). Writing it is not just redundant but harmful:
	// once forwarder.apply actually runs (GH #237), a non-empty write sets
	// the property, but Stalwart rejects the empty-array patch needed to
	// clear it (invalidPatch) — so removed aliases would leave stale,
	// uncleanable ghost entries on the principal. Delivery is unaffected:
	// an alias whose email_forwarders row is gone returns "550 Mailbox does
	// not exist" regardless of the stale property (verified on the test
	// server). So we leave the principal property empty, as designed.
	return nil
}

// buildExternalSieve renders the concatenated jabali-fwds Sieve script for a
// mailbox's external forwards. GH #237: `redirect :copy` keeps a copy in the
// mailbox, plain `redirect` does not. The "copy" extension is required only
// when at least one target keeps a copy.
func buildExternalSieve(externals []forwarderExternal) string {
	hasCopy := false
	for _, e := range externals {
		if e.KeepCopy {
			hasCopy = true
			break
		}
	}
	var body strings.Builder
	if hasCopy {
		body.WriteString(`require ["copy"];` + "\n")
	}
	for _, e := range externals {
		if e.Target == "" {
			continue
		}
		if e.KeepCopy {
			fmt.Fprintf(&body, "redirect :copy %q;\n", e.Target)
		} else {
			fmt.Fprintf(&body, "redirect %q;\n", e.Target)
		}
	}
	return body.String()
}

func applyExternalSieve(ctx context.Context, acctID string, externals []forwarderExternal) error {
	scriptName := "jabali-fwds"

	// Find the existing jabali-fwds script id. JMAP update/destroy key by
	// the object ID, NOT the name — keying by name silently no-ops, which
	// froze the script at its first-ever content (GH #237). Read the id
	// first so update/destroy target the real object. Note: the store is
	// x:SieveUserScript (a Stalwart extension), NOT the standard
	// SieveScript — they are different stores, so every call here must use
	// the x: method.
	existingID, err := existingFwdScriptID(ctx, acctID, scriptName)
	if err != nil {
		return err
	}

	if len(externals) == 0 {
		if existingID == "" {
			return nil // nothing to remove
		}
		args := map[string]any{
			"accountId": acctID,
			"destroy":   []string{existingID},
		}
		var result jmapSetResult
		if err := jmapCall(ctx, "x:SieveUserScript/set", args, &result); err != nil {
			return fmt.Errorf("sieve destroy: %w", err)
		}
		return nil
	}

	contents := buildExternalSieve(externals)

	if existingID != "" {
		// Update the existing script BY ID.
		args := map[string]any{
			"accountId": acctID,
			"update": map[string]any{
				existingID: map[string]any{
					"contents": contents,
					"isActive": true,
				},
			},
		}
		var result jmapSetResult
		if err := jmapCall(ctx, "x:SieveUserScript/set", args, &result); err != nil {
			return fmt.Errorf("sieve update: %w", err)
		}
		if reason, ok := result.NotUpdated[existingID]; ok {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("sieve update refused: %s", string(reason))}
		}
		return nil
	}

	// No existing script → create.
	args := map[string]any{
		"accountId": acctID,
		"create": map[string]any{
			scriptName: map[string]any{
				"name":     scriptName,
				"isActive": true,
				"contents": contents,
			},
		},
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:SieveUserScript/set", args, &result); err != nil {
		return err
	}
	if reason, ok := result.NotCreated[scriptName]; ok {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("sieve create refused: %s", string(reason))}
	}
	return nil
}

// existingFwdScriptID returns the object id of the account's jabali-fwds
// Sieve script, or "" if it doesn't exist yet.
func existingFwdScriptID(ctx context.Context, acctID, scriptName string) (string, error) {
	args := map[string]any{"accountId": acctID}
	var result jmapGetResult
	if err := jmapCall(ctx, "x:SieveUserScript/get", args, &result); err != nil {
		return "", fmt.Errorf("sieve get: %w", err)
	}
	for _, raw := range result.List {
		var s struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		if s.Name == scriptName {
			return s.ID, nil
		}
	}
	return "", nil
}

func init() {
	Default.Register("forwarder.apply", forwarderApplyHandler)
}
