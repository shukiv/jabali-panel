package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mail.outbound_tls.ensure converges the outbound TLS strategies Stalwart ships
// so DANE is DISABLED on the ones jabali's outbound delivery uses (JAB-391).
//
// Why: Stalwart's shipped outbound TLS strategies carry dane="optional", so a
// send to a DNSSEC-signed destination (gmail.com, Google Workspace, …) must
// DNSSEC-validate the MX lookup. But jabali runs a NON-validating resolver
// chain — pdns-recursor dnssec=off + systemd-resolved DNSSEC=no — so the stub
// strips RRSIGs. Stalwart's DANE validator then sees a signed zone with no
// valid signatures, classifies it "Bogus", and hard-defers the message —
// breaking outbound to every DNSSEC-signed domain (a large, growing slice of
// the internet). Even dane="optional" fails a Bogus result, because Bogus means
// suspected tampering, not merely "no TLSA".
//
// The operator's decision (JAB-391) is to ship with DANE off: outbound MITM
// protection continues via MTA-STS (which is what actually protects the
// Google/Microsoft paths — they publish MTA-STS policies, not TLSA records) and
// opportunistic STARTTLS, exactly what Gmail/Outlook and almost every MTA do.
// The path *back* to DANE is a DNSSEC-capable resolver, NOT re-enabling it
// against this chain.
//
// jabali owns exactly ONE knob here: the `dane` field on the two SHIPPED
// strategies ("default" + "invalid-tls"). It never writes name / startTls /
// mtaSts / allowInvalidCerts (operator tuning of those is never fought), and
// never touches an operator-created custom strategy (a deliberate dane=require
// there means they gave that route a validating resolver). RFC-8620 PatchObject
// semantics — verified live on the Stalwart 0.16 wire — apply only the named
// field, leaving the rest intact.
//
// This is deliberately NOT wired into install/stalwart/apply-plan.json.tmpl:
// the strategies carry Stalwart-generated ids a static template cannot address,
// so this ensure verb is the SINGLE mechanism for fresh installs AND retrofits.
// The ≤1-tick fresh-install window at dane="optional" holds no tenant mail. Do
// not "fix" the missing apply-plan entry — the template cannot express this.

// jabaliOutboundDANEStrategies are the SHIPPED Stalwart TLS strategy names whose
// `dane` knob jabali owns. Matched by name; a missing one is skipped, an
// operator-created one is never touched.
var jabaliOutboundDANEStrategies = map[string]bool{
	"default":     true,
	"invalid-tls": true,
}

type mtaTLSStrategyRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Dane string `json:"dane"`
}

type mailOutboundTLSResponse struct {
	Changed []string `json:"changed"`           // strategy names patched to dane=disable
	Skipped bool     `json:"skipped,omitempty"` // Stalwart unreachable → clean skip (retry later)
}

func mailOutboundTLSEnsureHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	var qr jmapQueryResult
	if err := jmapCall(ctx, "x:MtaTlsStrategy/query", map[string]any{}, &qr); err != nil {
		if isStalwartUnavailable(err) {
			return mailOutboundTLSResponse{Skipped: true}, nil
		}
		return nil, err
	}
	if len(qr.IDs) == 0 {
		return mailOutboundTLSResponse{}, nil
	}

	var gr jmapGetResult
	if err := jmapCall(ctx, "x:MtaTlsStrategy/get", map[string]any{"ids": qr.IDs}, &gr); err != nil {
		if isStalwartUnavailable(err) {
			return mailOutboundTLSResponse{Skipped: true}, nil
		}
		return nil, err
	}

	update := map[string]any{}
	var changed []string
	for _, raw := range gr.List {
		var row mtaTLSStrategyRow
		if json.Unmarshal(raw, &row) != nil {
			continue
		}
		if !jabaliOutboundDANEStrategies[row.Name] {
			continue // operator-created strategy — deliberate config, never touched
		}
		if row.Dane == "disable" {
			continue // already converged
		}
		update[row.ID] = map[string]any{"dane": "disable"}
		changed = append(changed, row.Name)
	}

	if len(update) == 0 {
		return mailOutboundTLSResponse{}, nil // no drift
	}

	var sr jmapSetResult
	if err := jmapCall(ctx, "x:MtaTlsStrategy/set", map[string]any{"update": update}, &sr); err != nil {
		return nil, err
	}
	for id, reason := range sr.NotUpdated {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart refused dane update for %s: %s", id, string(reason)),
		}
	}

	// Reload so the patched strategies take effect on the next delivery attempt.
	if err := reloadStalwartSettings(ctx); err != nil {
		return nil, err
	}
	return mailOutboundTLSResponse{Changed: changed}, nil
}

// isStalwartUnavailable reports whether err is the "stalwart JMAP unreachable"
// signal (Stalwart not running on this box) — a clean skip, not a failure.
func isStalwartUnavailable(err error) bool {
	var ae *agentwire.AgentError
	if errors.As(err, &ae) {
		return ae.Code == agentwire.CodeUnavailable
	}
	return false
}

func init() {
	Default.Register("mail.outbound_tls.ensure", mailOutboundTLSEnsureHandler)
}
