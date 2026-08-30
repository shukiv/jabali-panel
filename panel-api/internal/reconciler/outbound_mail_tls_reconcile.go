package reconciler

import (
	"context"
	"encoding/json"
)

// reconcileOutboundMailTLS dispatches the JAB-391 DANE-disable ensure to the
// agent once per process, on a mail-enabled box, until it succeeds.
//
// Background: Stalwart's shipped outbound TLS strategies default to
// dane="optional", which hard-defers mail to DNSSEC-signed destinations
// (gmail.com, Google Workspace, …) because jabali runs a non-validating
// resolver chain that strips the RRSIGs DANE needs — Stalwart reads the signed
// zone as "Bogus". The agent verb mail.outbound_tls.ensure patches the shipped
// strategies to dane="disable" (see its doc comment for the full rationale and
// why this lives in a verb, not the apply-plan).
//
// Gating: MailEnabled (a no-mail box has no outbound to fix) and
// once-per-process-until-success. A transient Stalwart-unreachable reply
// ("skipped") is NOT success — it retries next tick; a real failure logs and
// retries. Only a genuine converge (or a no-drift no-op) flips the latch. The
// verb is idempotent, so re-running after a spurious extra tick is harmless.
func (r *Reconciler) reconcileOutboundMailTLS(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil || r.outboundTLSConverged {
		return
	}
	settings, err := r.serverSettings.Get(ctx)
	if err != nil {
		r.log.Debug("outbound-mail-TLS reconcile skipped: server_settings unavailable", "error", err)
		return
	}
	if !settings.MailEnabled {
		return
	}

	raw, err := r.agent.Call(ctx, "mail.outbound_tls.ensure", map[string]any{})
	if err != nil {
		r.log.Warn("outbound-mail-TLS DANE ensure failed (JAB-391), will retry next tick", "error", err)
		return
	}

	// A "skipped" reply means Stalwart was not reachable this tick (not yet up
	// on a freshly-provisioned box) — don't latch, so the next tick retries.
	var resp struct {
		Changed []string `json:"changed"`
		Skipped bool     `json:"skipped"`
	}
	_ = json.Unmarshal(raw, &resp)
	if resp.Skipped {
		r.log.Debug("outbound-mail-TLS ensure skipped: Stalwart not reachable yet, retrying next tick")
		return
	}

	r.outboundTLSConverged = true
	if len(resp.Changed) > 0 {
		r.log.Info("outbound-mail-TLS DANE disabled on shipped strategies (JAB-391)", "strategies", resp.Changed)
	}
}
