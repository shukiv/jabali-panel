package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// dkimEd25519SignatureType is the agent's name for the Ed25519 DKIM1
// signature in a domain_email.dkim_ensure response.
const dkimEd25519SignatureType = "Dkim1Ed25519Sha256"

// reconcileDKIMSignatures gives every domain whose mail this server hosts
// both DKIM signatures (GH #2475): the agent creates whichever of the
// Ed25519 and RSA signatures the domain's Stalwart entry lacks, and this pass
// records the RSA key on the domain row and publishes it at
// jabalirsa._domainkey. That is how a domain set up before #2475 gets its RSA
// key, and how a domain whose Stalwart entry was created without signatures
// gets signed at all; mail enable gives a new domain both directly.
//
// The call carries the keys the panel publishes. A signature that signs with
// another key while the key file holds the published one (a rotation from
// before Rotate DKIM updated Stalwart left domains like that, failing DKIM at
// every receiver) is given the published key by the agent. DNS already
// carries that key, so nothing changes here.
//
// A domain whose mail is not set up yet (no selector) is left to the enable
// convergence. Applied-state map as in reconcileDKIM2: every domain is
// converged once after boot (the agent call is a noop when both signatures
// exist), then only new mail domains and earlier failures are.
func (r *Reconciler) reconcileDKIMSignatures(ctx context.Context) {
	if r.domains == nil || r.agent == nil {
		return
	}
	domains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("dkim signatures: list domains failed", "error", err)
		return
	}

	r.dkimSigMu.Lock()
	if r.dkimSigApplied == nil {
		r.dkimSigApplied = make(map[string]bool)
	}
	r.dkimSigMu.Unlock()

	for i := range domains {
		d := &domains[i]
		if models.MailHostedElsewhere(d.EmailEnabled, d.MailProvider) || ownershipPending(d) ||
			!domainIsMailRoutable(d.Name) || d.DkimSelector == nil || *d.DkimSelector == "" {
			// Disabling mail drops the domain's Stalwart entry; forget it so
			// a later re-enable converges again.
			r.dkimSigMu.Lock()
			delete(r.dkimSigApplied, d.Name)
			r.dkimSigMu.Unlock()
			continue
		}
		r.dkimSigMu.Lock()
		done := r.dkimSigApplied[d.Name]
		r.dkimSigMu.Unlock()
		if done {
			continue
		}
		done, agentTooOld := r.ensureDomainDKIMSignatures(ctx, d)
		if agentTooOld {
			// An agent from before #2475 (mid-update): retry next tick.
			return
		}
		if done {
			r.dkimSigMu.Lock()
			r.dkimSigApplied[d.Name] = true
			r.dkimSigMu.Unlock()
		}
	}
}

// ensureDomainDKIMSignatures converges one domain; done=false means retry
// next tick. agentTooOld reports an agent without domain_email.dkim_ensure.
func (r *Reconciler) ensureDomainDKIMSignatures(ctx context.Context, d *models.Domain) (done, agentTooOld bool) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	raw, err := r.agent.Call(cctx, "domain_email.dkim_ensure", map[string]any{
		"domain_name":                  d.Name,
		"published_ed25519_public_key": stringOrEmpty(d.DkimPublicKey),
		"published_rsa_public_key":     stringOrEmpty(d.DkimRSAPublicKey),
	})
	cancel()
	if err != nil {
		var ae *agent.AgentError
		if errors.As(err, &ae) {
			switch ae.Code {
			case agent.CodeUnknownCommand:
				return false, true
			case agent.CodeNotFound:
				// Mail is set up in the panel but the mail server has no
				// entry for the domain; nothing here can sign for it.
				// Logged once.
				r.log.Warn("dkim signatures: the mail server has no entry for this mail domain", "domain", d.Name)
				return true, false
			}
		}
		r.log.Warn("dkim signatures: ensure failed", "domain", d.Name, "error", err)
		return false, false
	}
	var resp struct {
		Ed25519PublicKey string   `json:"ed25519_public_key"`
		RSAPublicKey     string   `json:"rsa_public_key"`
		Created          []string `json:"created"`
		Healed           []string `json:"healed"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		r.log.Warn("dkim signatures: bad agent response", "domain", d.Name, "error", err)
		return false, false
	}
	if len(resp.Healed) > 0 {
		r.log.Info("dkim signatures: the mail server signed with a key nothing published; it now signs with the published key",
			"domain", d.Name, "signatures", resp.Healed)
	}

	// The RSA key is recorded whenever it is known and differs. The Ed25519
	// key only when its signature was just created: an existing signature
	// keeps the key already published, whatever the key file says.
	var edKey, rsaKey *string
	if resp.RSAPublicKey != "" && resp.RSAPublicKey != stringOrEmpty(d.DkimRSAPublicKey) {
		rsaKey = &resp.RSAPublicKey
	}
	if slices.Contains(resp.Created, dkimEd25519SignatureType) &&
		resp.Ed25519PublicKey != "" && resp.Ed25519PublicKey != stringOrEmpty(d.DkimPublicKey) {
		edKey = &resp.Ed25519PublicKey
	}
	if edKey != nil || rsaKey != nil {
		if err := r.domains.UpdateDKIMPublicKeys(ctx, d.ID, edKey, rsaKey); err != nil {
			r.log.Warn("dkim signatures: store public keys failed", "domain", d.Name, "error", err)
			return false, false
		}
		if edKey != nil {
			d.DkimPublicKey = edKey
		}
		if rsaKey != nil {
			d.DkimRSAPublicKey = rsaKey
		}
	}

	// Publish. The RSA record is converged on every first pass (the row may
	// be missing even when the key is not new, e.g. on the panel's own
	// hostname, which has no other DNS backfill); the Ed25519 one only when
	// its key changed.
	want := map[string]string{}
	if k := stringOrEmpty(d.DkimRSAPublicKey); k != "" {
		want[dnscompile.EmailRecordsRSASelector+"._domainkey"] = dnscompile.QuoteTXT(k)
	}
	if edKey != nil {
		want[*d.DkimSelector+"._domainkey"] = dnscompile.QuoteTXT(*edKey)
	}
	return r.publishDKIMRecords(ctx, d, want), false
}

// publishDKIMRecords makes each name in want carry an m6-managed TXT record
// with that content: created when missing, its content replaced when it
// differs. A record the operator edited (no longer m6-managed) is left alone.
// A domain without a hosted zone has nothing to publish. False means retry.
func (r *Reconciler) publishDKIMRecords(ctx context.Context, d *models.Domain, want map[string]string) bool {
	if len(want) == 0 || r.dnsZones == nil || r.dnsRecords == nil {
		return true
	}
	zone, err := r.dnsZones.FindByDomainID(ctx, d.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return true
		}
		r.log.Warn("dkim signatures: DNS zone lookup failed", "domain", d.Name, "error", err)
		return false
	}
	existing, err := r.dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		r.log.Warn("dkim signatures: DNS record list failed", "domain", d.Name, "error", err)
		return false
	}
	var srv *models.ServerSettings
	if r.serverSettings != nil {
		srv, _ = r.settingsGet(ctx)
	}

	ok, changed := true, false
	for name, content := range want {
		if conflictingDNSRecord(existing, name, "TXT") {
			r.log.Debug("dkim signatures: DNS record edited by the operator; leaving it", "domain", d.Name, "name", name)
			continue
		}
		var cur *models.DNSRecord
		for i := range existing {
			if existing[i].Name == name && existing[i].Type == "TXT" {
				cur = &existing[i]
				break
			}
		}
		switch {
		case cur == nil:
			m6 := dnscompile.EmailRecordsManagedBy
			now := time.Now().UTC()
			rec := models.DNSRecord{
				ID: ids.NewULID(), ZoneID: zone.ID, Name: name, Type: "TXT", Content: content,
				TTL: models.EffectiveDNSTTL(srv), Managed: true, ManagedBy: &m6, IsEnabled: true,
				CreatedAt: now, UpdatedAt: now,
			}
			if err := r.dnsRecords.Create(ctx, &rec); err != nil {
				r.log.Warn("dkim signatures: DNS record create failed", "domain", d.Name, "name", name, "error", err)
				ok = false
				continue
			}
			changed = true
		case cur.Content != content:
			cur.Content = content
			cur.UpdatedAt = time.Now().UTC()
			if err := r.dnsRecords.Update(ctx, cur); err != nil {
				r.log.Warn("dkim signatures: DNS record update failed", "domain", d.Name, "name", name, "error", err)
				ok = false
				continue
			}
			changed = true
		}
	}
	if changed {
		// Push the zone on the next pass rather than at the periodic
		// re-dispatch.
		r.Schedule(d.ID)
	}
	return ok
}

func stringOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
