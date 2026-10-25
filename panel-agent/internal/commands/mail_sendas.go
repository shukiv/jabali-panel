package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mail_sendas.go — GH #347 send-as delegation, Step 3 (Mechanism C, see
// project_jab347_spike_verdict). The panel is truth: panel-api computes the full
// set of (delegate, grantor) email pairs from mailbox_send_delegations and calls
// this command after any change. We materialise the pairs into Stalwart's
// MtaStageAuth.mustMatchSender EXPRESSION so the built-in sender check is skipped
// ONLY for a verified pair and enforced (true) for everyone else — no global
// mustMatchSender=false, so non-delegated mailboxes keep full anti-spoofing.
//
//	mail.sendas.reconcile  {pairs:[{delegate_email,grantor_email},...]}  ->  {pairs, applied}
//
// Full-replace + idempotent: the expression is rebuilt from the supplied set every
// call (never appended), so a missed event self-heals on the next reconcile.
// sql_query is NOT usable here (Stalwart 0.16.12 exposes no addable named SQL
// store — spike finding), which is why the pairs are materialised into the
// expression text rather than looked up at evaluation time.

type sendAsPair struct {
	DelegateEmail string `json:"delegate_email"`
	GrantorEmail  string `json:"grantor_email"`
}

type mailSendAsReconcileParams struct {
	Pairs []sendAsPair `json:"pairs"`
}

// safeEmail bounds what may appear inside a single-quoted expression string
// literal. The expression is CODE: an unescaped address would be an injection
// into Stalwart's sender check, so we reject anything outside a conservative
// address charset (no quotes, backslashes, whitespace, or control chars) rather
// than trying to escape it. Real mailbox email_cached values never contain these.
// Domains are stored in punycode (internal/mailaddr), so an internationalised
// TLD arrives as "xn--..." (GH #2267: one such pair used to fail the whole set).
var safeEmail = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.(?:[A-Za-z]{2,63}|xn--[A-Za-z0-9\-]{1,59})$`)

// buildMustMatchSenderExpr renders the mustMatchSender expression for a set of
// delegation pairs. Empty set -> the literal "true" (byte-identical to the stock
// value; NOT "!()", which is a parse error). Pairs are de-duplicated and sorted
// so the output is stable for a given set (no needless ReloadSettings churn).
// Returns an error if any address is unsafe — the caller must NOT apply a partial
// or injectable expression.
func buildMustMatchSenderExpr(pairs []sendAsPair) (string, error) {
	seen := make(map[string]struct{}, len(pairs))
	clauses := make([]string, 0, len(pairs))
	for _, p := range pairs {
		d := strings.TrimSpace(p.DelegateEmail)
		g := strings.TrimSpace(p.GrantorEmail)
		if d == "" || g == "" {
			continue
		}
		if !safeEmail.MatchString(d) {
			return "", fmt.Errorf("unsafe delegate address %q", d)
		}
		if !safeEmail.MatchString(g) {
			return "", fmt.Errorf("unsafe grantor address %q", g)
		}
		if d == g {
			continue // self-delegation is a no-op (own address already allowed)
		}
		key := d + "\x00" + g
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		clauses = append(clauses, fmt.Sprintf("(authenticated_as == '%s' && sender == '%s')", d, g))
	}
	if len(clauses) == 0 {
		return "true", nil
	}
	sort.Strings(clauses)
	return "!(" + strings.Join(clauses, " || ") + ")", nil
}

func mailSendAsReconcileHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p mailSendAsReconcileParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid params: " + err.Error()}
		}
	}
	expr, err := buildMustMatchSenderExpr(p.Pairs)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "build mustMatchSender: " + err.Error()}
	}

	// Patch MtaStageAuth.mustMatchSender. `stalwart-cli update` validates the
	// expression server-side and REJECTS a malformed one (proven in the spike),
	// so a bad expression never lands — the prior value stays in force. Only after
	// a successful update do we ReloadSettings to make it effective (spike: the
	// MtaStage config is not applied by InvalidateCaches, only ReloadSettings).
	patch := map[string]any{"mustMatchSender": map[string]any{"else": expr, "match": map[string]any{}}}
	body, mErr := json.Marshal(patch)
	if mErr != nil {
		return nil, csInternal("marshal mustMatchSender patch", mErr)
	}
	f, cErr := os.CreateTemp("", "jab-sendas-*.json")
	if cErr != nil {
		return nil, csInternal("create patch tempfile", cErr)
	}
	defer os.Remove(f.Name())
	if _, wErr := f.Write(body); wErr != nil {
		f.Close()
		return nil, csInternal("write patch tempfile", wErr)
	}
	f.Close()

	if _, err := runStalwartCLI(ctx, "update", "MtaStageAuth", "--file", f.Name()); err != nil {
		return nil, err // update rejected -> prior mustMatchSender untouched (safe)
	}
	if _, err := runStalwartCLI(ctx, "create", "Action/ReloadSettings"); err != nil {
		return nil, err
	}
	return map[string]any{"pairs": len(p.Pairs), "applied": true}, nil
}

func init() {
	Default.Register("mail.sendas.reconcile", mailSendAsReconcileHandler)
}
