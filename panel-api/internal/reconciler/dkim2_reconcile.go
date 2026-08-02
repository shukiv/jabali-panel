package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileDKIM2 converges DKIM2 chain-of-custody signing (GH #648) across
// every mail-enabled domain to server_settings.dkim2_signing_enabled: the
// agent creates or removes the Dkim2Ed25519Sha256 signature in Stalwart's
// registry (same key + selector as the classic DKIM signature, so the
// published DNS already verifies it; the agent call is idempotent).
//
// Gated by an in-memory applied-state map instead of a single hash: a mail
// domain enabled AFTER the toggle was applied still needs its own converge,
// which one global hash can't see. First tick after boot converges every
// domain once (cheap agent noops); after that only toggle flips and new
// mail domains trigger calls. A failed call leaves the domain out of the
// map, so the next tick retries it.
func (r *Reconciler) reconcileDKIM2(ctx context.Context) {
	if r.domains == nil || r.agent == nil || r.serverSettings == nil {
		return
	}
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	srv, err := r.serverSettings.Get(sctx)
	scancel()
	if err != nil || srv == nil {
		return
	}
	enabled := srv.DKIM2SigningEnabled

	domains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("dkim2: list domains failed", "error", err)
		return
	}

	r.dkim2Mu.Lock()
	if r.dkim2Applied == nil {
		r.dkim2Applied = make(map[string]bool)
	}
	r.dkim2Mu.Unlock()

	for i := range domains {
		d := &domains[i]
		if !d.EmailEnabled {
			// A domain whose email was disabled loses its Stalwart registry
			// entry (and with it the signature) via domain_email.disable —
			// drop it from the cache so a later re-enable reconverges.
			r.dkim2Mu.Lock()
			delete(r.dkim2Applied, d.Name)
			r.dkim2Mu.Unlock()
			continue
		}
		r.dkim2Mu.Lock()
		applied, seen := r.dkim2Applied[d.Name]
		r.dkim2Mu.Unlock()
		if seen && applied == enabled {
			continue
		}

		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := r.agent.Call(cctx, "domain_email.dkim2_set", map[string]any{
			"domain_name": d.Name,
			"enabled":     enabled,
		})
		cancel()
		if err != nil {
			r.log.Warn("dkim2: converge failed", "domain", d.Name, "enabled", enabled, "error", err)
			continue
		}
		r.dkim2Mu.Lock()
		r.dkim2Applied[d.Name] = enabled
		r.dkim2Mu.Unlock()
	}
}
