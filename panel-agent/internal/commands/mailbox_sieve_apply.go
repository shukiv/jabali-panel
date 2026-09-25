package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mailbox.sieve.apply — the single agent command that converges a mailbox's
// server-side mail rules (external forwards + autoresponder) into ONE active
// standard SieveScript, the store Stalwart actually executes at delivery
// (GH #1795). It supersedes forwarder.apply's `x:SieveUserScript` write (a store
// Stalwart never runs) and autoresponder.set's native VacationResponse; the
// panel now sends the full desired state here so the composite respects
// Stalwart's one-active-script-per-account rule.
//
// The standard-SieveScript write pattern (blob upload → SieveScript/set with
// jmapCapSieve → onSuccessActivateScript) is the same one applyGroupInternalOnly
// (GH #348) already uses successfully; forwards and the autoresponder were the
// only rule paths that wrote to a store Stalwart does not run at delivery.
type mailboxSieveApplyParams struct {
	MailboxEmail  string                     `json:"mailbox_email"`
	Externals     []mailboxSieveExternal     `json:"externals"`
	Autoresponder *mailboxSieveAutoresponder `json:"autoresponder"`
}

type mailboxSieveExternal struct {
	Target   string `json:"target"`
	KeepCopy bool   `json:"keep_copy"`
}

type mailboxSieveAutoresponder struct {
	Enabled  bool    `json:"enabled"`
	FromDate *string `json:"from_date"`
	ToDate   *string `json:"to_date"`
	Subject  *string `json:"subject"`
	TextBody *string `json:"text_body"`
	HTMLBody *string `json:"html_body"`
}

type mailboxSieveApplyResponse struct {
	Ok bool `json:"ok"`
}

func mailboxSieveApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p mailboxSieveApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if _, err := requireEmail(p.MailboxEmail); err != nil {
		return nil, err
	}

	acctID, err := resolveOrEnsureAccount(ctx, p.MailboxEmail)
	if err != nil {
		return nil, err
	}

	// Serialise applies for the same account. Two concurrent applies (the
	// reconcile backfill sweep racing a tenant save) would both see no
	// jabali-managed script, both create one, and then each destroy the other's
	// as a "non-managed" duplicate — leaving the account with zero scripts and
	// both callers reporting success. The whole list→set→destroy sequence must be
	// atomic per account.
	unlock := lockSieveAccount(acctID)
	defer unlock()

	fwds := make([]managedForward, 0, len(p.Externals))
	for _, e := range p.Externals {
		fwds = append(fwds, managedForward{Target: e.Target, KeepCopy: e.KeepCopy})
	}
	var ar managedAutoresponder
	if p.Autoresponder != nil {
		ar = managedAutoresponder{
			Enabled:  p.Autoresponder.Enabled,
			FromDate: p.Autoresponder.FromDate,
			ToDate:   p.Autoresponder.ToDate,
			Subject:  p.Autoresponder.Subject,
			TextBody: p.Autoresponder.TextBody,
			HTMLBody: p.Autoresponder.HTMLBody,
		}
	}

	contents, err := buildManagedSieve(fwds, ar)
	if err != nil {
		// A malformed forward target fails closed — do not touch the ruleset.
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("build sieve: %v", err)}
	}

	// Remove the legacy x:SieveUserScript objects on every apply — they never
	// ran but must not linger once we own the account's real script.
	if err := destroyLegacyUserScripts(ctx, acctID); err != nil {
		return nil, err
	}

	if contents == "" {
		// No forwards and no autoresponder → destroy the scripts jabali itself
		// created (the composite, and any legacy native "vacation" script left by
		// the old autoresponder path) rather than activate an empty one. Scripts
		// jabali never created are left alone (keepID "").
		if err := destroyJabaliScripts(ctx, acctID, ""); err != nil {
			return nil, err
		}
		return mailboxSieveApplyResponse{Ok: true}, nil
	}

	blobID, err := uploadSieveBlob(ctx, acctID, contents)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("sieve blob upload: %v", err)}
	}
	if blobID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "sieve blob upload returned no blobId"}
	}
	managedID, err := setActiveManagedScript(ctx, acctID, blobID)
	if err != nil {
		return nil, err
	}
	// Clear the OTHER jabali-owned script — a stray native "vacation" left by the
	// old autoresponder path — so the composite is the only jabali script on the
	// account. Activating the composite already deactivated any other (incl.
	// tenant-owned) script via Stalwart's one-active rule; those are left in
	// place, only jabali's own legacy artifact is removed.
	if err := destroyJabaliScripts(ctx, acctID, managedID); err != nil {
		return nil, err
	}
	return mailboxSieveApplyResponse{Ok: true}, nil
}

// resolveOrEnsureAccount returns the Stalwart account id for the mailbox,
// provisioning the registry Principal (idempotently) if it is not there yet —
// mirrors forwarder.apply / autoresponder.set so a rule can be applied right
// after the mailbox was created (JIT-provisioning race, ADR-0045).
func resolveOrEnsureAccount(ctx context.Context, email string) (string, error) {
	acctID, err := accountIDByEmail(ctx, email)
	if err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve account: %v", err)}
	}
	if acctID != "" {
		return acctID, nil
	}
	if ensureErr := accountEnsureInRegistry(ctx, email); ensureErr != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("mailbox not registered and ensure failed: %v", ensureErr)}
	}
	acctID, err = accountIDByEmail(ctx, email)
	if err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("resolve account after ensure: %v", err)}
	}
	if acctID == "" {
		return "", &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: "mailbox not registered with mail server"}
	}
	return acctID, nil
}

// setActiveManagedScript creates-or-updates the jabali-managed SieveScript from
// the uploaded blob and activates it (onSuccessActivateScript). Update-by-id
// when it already exists; create otherwise. Activating it deactivates any other
// script on the account (Stalwart's one-active rule), which is what we want —
// jabali owns the active slot. SieveScript is RFC 9661, so every call carries
// jmapCapSieve (same as applyGroupInternalOnly).
func setActiveManagedScript(ctx context.Context, acctID, blobID string) (string, error) {
	return setActiveNamedScript(ctx, acctID, managedScriptName, blobID)
}

// setActiveNamedScript is setActiveManagedScript for any jabali-owned script
// name. The internal-only distribution list (GH #1818) keeps its combined
// reject + redirect script on the group account under its own name.
func setActiveNamedScript(ctx context.Context, acctID, name, blobID string) (string, error) {
	existingID, err := scriptIDByName(ctx, acctID, name)
	if err != nil {
		return "", err
	}
	if existingID != "" {
		args := map[string]any{
			"accountId":               acctID,
			"update":                  map[string]any{existingID: map[string]any{"blobId": blobID}},
			"onSuccessActivateScript": existingID,
		}
		var result jmapSetResult
		if err := jmapCallWith(ctx, jmapCapSieve, "SieveScript/set", args, &result); err != nil {
			return "", fmt.Errorf("sieve update: %w", err)
		}
		if reason, ok := result.NotUpdated[existingID]; ok {
			return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("sieve update refused: %s", string(reason))}
		}
		return existingID, nil
	}
	args := map[string]any{
		"accountId":               acctID,
		"create":                  map[string]any{"s1": map[string]any{"name": name, "blobId": blobID}},
		"onSuccessActivateScript": "#s1",
	}
	var result jmapSetResult
	if err := jmapCallWith(ctx, jmapCapSieve, "SieveScript/set", args, &result); err != nil {
		return "", fmt.Errorf("sieve create: %w", err)
	}
	if reason, ok := result.NotCreated["s1"]; ok {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("sieve create refused: %s", string(reason))}
	}
	// Resolve the freshly-created id (the create key "s1" maps to it in Created).
	if newID := createdID(result, "s1"); newID != "" {
		return newID, nil
	}
	// Fall back to a name lookup if the server did not echo the created id.
	newID, lookupErr := scriptIDByName(ctx, acctID, name)
	if lookupErr != nil {
		return "", lookupErr
	}
	return newID, nil
}

// createdID pulls the server-assigned id for a create key out of a
// SieveScript/set response, tolerating servers that omit the Created map.
func createdID(result jmapSetResult, key string) string {
	raw, ok := result.Created[key]
	if !ok {
		return ""
	}
	var row struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return ""
	}
	return row.ID
}

// sieveScriptRow is the subset of a standard SieveScript we read back.
type sieveScriptRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsActive bool   `json:"isActive"`
}

func listSieveScripts(ctx context.Context, acctID string) ([]sieveScriptRow, error) {
	var result jmapGetResult
	if err := jmapCallWith(ctx, jmapCapSieve, "SieveScript/get", map[string]any{"accountId": acctID}, &result); err != nil {
		return nil, fmt.Errorf("sieve get: %w", err)
	}
	out := make([]sieveScriptRow, 0, len(result.List))
	for _, raw := range result.List {
		var r sieveScriptRow
		if err := json.Unmarshal(raw, &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func managedScriptID(ctx context.Context, acctID string) (string, error) {
	return scriptIDByName(ctx, acctID, managedScriptName)
}

func scriptIDByName(ctx context.Context, acctID, name string) (string, error) {
	rows, err := listSieveScripts(ctx, acctID)
	if err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Name == name {
			return r.ID, nil
		}
	}
	return "", nil
}

func destroySieveScriptIDs(ctx context.Context, acctID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var result jmapSetResult
	if err := jmapCallWith(ctx, jmapCapSieve, "SieveScript/set", map[string]any{"accountId": acctID, "destroy": ids}, &result); err != nil {
		return fmt.Errorf("sieve destroy: %w", err)
	}
	// Surface a refused destroy instead of swallowing it. Stalwart refuses to
	// destroy the ACTIVE script ("scriptIsActive") — a real failure that would
	// silently leave the composite live after a tenant clears every rule. An
	// already-gone id ("notFound") is fine (idempotent).
	for id, reason := range result.NotDestroyed {
		var r struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(reason, &r)
		if r.Type != "notFound" {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("sieve destroy refused for %s: %s", id, string(reason))}
		}
	}
	return nil
}

// deactivateSieveScripts clears the active flag on the given scripts. Stalwart
// 0.16.x refuses to destroy an active script, and `onSuccessActivateScript:null`
// is a no-op there, so the only reliable deactivation is an explicit
// isActive:false update (verified against Stalwart 0.16.15).
func deactivateSieveScripts(ctx context.Context, acctID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	update := make(map[string]any, len(ids))
	for _, id := range ids {
		update[id] = map[string]any{"isActive": false}
	}
	var result jmapSetResult
	if err := jmapCallWith(ctx, jmapCapSieve, "SieveScript/set", map[string]any{"accountId": acctID, "update": update}, &result); err != nil {
		return fmt.Errorf("sieve deactivate: %w", err)
	}
	for id, reason := range result.NotUpdated {
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("sieve deactivate refused for %s: %s", id, string(reason))}
	}
	return nil
}

// jabaliOwnedScriptNames are the standard SieveScripts jabali itself creates on
// an account: the composite it owns now, and the legacy native "vacation" script
// Stalwart compiled from the pre-GH#1795 VacationResponse autoresponder path.
// The sweep destroys only these — a tenant-authored script (if the mail server
// ever exposes one) is never jabali's to delete. Stalwart's one-active rule
// already deactivates any other script when the composite is activated, so
// leaving it in place is safe.
var jabaliOwnedScriptNames = map[string]bool{
	managedScriptName: true,
	"vacation":        true,
}

// destroyJabaliScripts removes the jabali-owned standard SieveScripts on the
// account, except the one whose id is keepID (the composite just activated;
// pass "" to remove all jabali-owned scripts, i.e. the empty-desired-state
// case). Scripts jabali did not create are never touched.
func destroyJabaliScripts(ctx context.Context, acctID, keepID string) error {
	rows, err := listSieveScripts(ctx, acctID)
	if err != nil {
		return err
	}
	var ids, activeIDs []string
	for _, r := range rows {
		if r.ID == keepID {
			continue
		}
		if jabaliOwnedScriptNames[r.Name] {
			ids = append(ids, r.ID)
			if r.IsActive {
				activeIDs = append(activeIDs, r.ID)
			}
		}
	}
	// Stalwart refuses to destroy the active script, so deactivate first — but
	// ONLY the jabali-owned scripts we are about to destroy. A tenant's own
	// active script (never in this set) keeps its active state untouched. In the
	// empty-desired-state case this leaves the account with no active script, so
	// mail delivers straight to the inbox.
	if err := deactivateSieveScripts(ctx, acctID, activeIDs); err != nil {
		return err
	}
	return destroySieveScriptIDs(ctx, acctID, ids)
}

// destroyLegacyUserScripts removes any x:SieveUserScript objects on the account.
// These are the never-executed store jabali used before GH #1795; they carry no
// delivery effect but should not linger. x:SieveUserScript is a Stalwart
// extension served under the urn:stalwart:jmap capability, so it uses jmapCall.
func destroyLegacyUserScripts(ctx context.Context, acctID string) error {
	var result jmapGetResult
	if err := jmapCall(ctx, "x:SieveUserScript/get", map[string]any{"accountId": acctID}, &result); err != nil {
		return fmt.Errorf("legacy sieve get: %w", err)
	}
	if len(result.List) == 0 {
		return nil
	}
	ids := make([]string, 0, len(result.List))
	for _, raw := range result.List {
		var r struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &r); err == nil && r.ID != "" {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var setRes jmapSetResult
	if err := jmapCall(ctx, "x:SieveUserScript/set", map[string]any{"accountId": acctID, "destroy": ids}, &setRes); err != nil {
		return fmt.Errorf("legacy sieve destroy: %w", err)
	}
	return nil
}

// sieveAccountLocks serialises mailbox.sieve.apply per Stalwart account so the
// list→set→destroy sequence is atomic (see mailboxSieveApplyHandler). Keyed by
// account id; the mutex is created lazily and kept for process lifetime (a
// bounded set — one per mailbox that ever applies rules).
var sieveAccountLocks sync.Map // map[string]*sync.Mutex

func lockSieveAccount(acctID string) func() {
	m, _ := sieveAccountLocks.LoadOrStore(acctID, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func init() {
	Default.Register("mailbox.sieve.apply", mailboxSieveApplyHandler)
}
