package reconciler

import (
	"context"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Preview URLs (temp URLs). A domain with temp_url_enabled=1 gets an
// extra vhost server block at <slug>.preview.<hostname> (rendered by the
// agent from the preview_* params createDomainOnAgent sends). The shared
// infrastructure behind every preview host is maintained here:
//
//   - a wildcard A/AAAA record `*.preview.<hostname>` in the hostname's
//     own DNS zone (the pdns self-zone install.sh bootstraps), and
//   - one server-wide shared certificate row for `*.preview.<hostname>`
//     (source=acme) that reconcileAcmeSharedCerts issues via DNS-01.
//
// Both ensures are cheap per-tick no-ops once in place. Nothing is torn
// down when the last domain disables previews — the record and cert are
// harmless, renewals are cheap, and re-enable stays instant.

// previewManagedBy tags the wildcard DNS rows this subsystem owns.
const previewManagedBy = "preview"

// previewStateTTL bounds how often the per-domain loop re-reads settings
// + shared certs for preview parameters. One lookup per tick, not per
// domain.
const previewStateTTL = 45 * time.Second

type previewState struct {
	base     string // effective preview base ("" = preview unavailable)
	certPath string // shared wildcard pair; empty until issued
	keyPath  string
}

// previewStateCached returns the current preview parameters, cached for
// previewStateTTL. Never nil; hostname=="" means previews are off (no
// hostname configured or repos unwired).
func (r *Reconciler) previewStateCached(ctx context.Context) previewState {
	r.previewMu.Lock()
	defer r.previewMu.Unlock()
	if time.Since(r.previewAt) < previewStateTTL {
		return r.previewCache
	}

	st := previewState{}
	if r.serverSettings != nil && r.sharedCerts != nil {
		if srv, err := r.serverSettings.Get(ctx); err == nil && srv != nil {
			st.base = models.EffectivePreviewBase(srv)
		}
		if st.base != "" {
			wildcard := models.PreviewWildcardName(st.base)
			if certs, err := r.sharedCerts.ListAll(ctx); err == nil {
				for i := range certs {
					if certs[i].Name != wildcard || certs[i].Source != models.SharedCertSourceACME {
						continue
					}
					if certs[i].CertPath != nil && certs[i].KeyPath != nil {
						st.certPath = *certs[i].CertPath
						st.keyPath = *certs[i].KeyPath
					}
					break
				}
			}
		}
	}
	r.previewCache = st
	r.previewAt = time.Now()
	return st
}

// reconcilePreviewInfra ensures the wildcard DNS record and the shared
// ACME cert row exist once any enabled domain has previews on. Runs once
// per ReconcileAll pass, before the domain loop, so a freshly-enabled
// preview resolves within one tick.
func (r *Reconciler) reconcilePreviewInfra(ctx context.Context, domains map[string]*models.Domain) {
	if r.sharedCerts == nil || r.dnsZones == nil || r.dnsRecords == nil || r.serverSettings == nil {
		return
	}
	enabled := false
	for _, d := range domains {
		if d.TempURLEnabled {
			enabled = true
			break
		}
	}
	if !enabled {
		return
	}
	srv, err := r.serverSettings.Get(ctx)
	if err != nil || srv == nil {
		return
	}
	base := models.EffectivePreviewBase(srv)
	if base == "" {
		r.log.Warn("preview: domains have temp URLs enabled but neither preview_base nor a hostname is set")
		return
	}
	wildcard := models.PreviewWildcardName(base)
	// DNS + DNS-01 cert only make sense when a locally-hosted zone
	// contains the base. A foreign base (sslip.io magic DNS, or a custom
	// domain whose DNS lives elsewhere — GH #836) already resolves
	// externally; previews then serve HTTP-only and the fallback vhost
	// still applies.
	zone := r.findZoneForPreviewBase(ctx, base)

	// 1. Shared wildcard cert row (issued by reconcileAcmeSharedCerts).
	// Skipped for foreign bases: DNS-01 places the challenge TXT through
	// the local pdns, which cannot answer for a zone it does not host.
	if zone != nil {
		if certs, cerr := r.sharedCerts.ListAll(ctx); cerr == nil {
			found := false
			for i := range certs {
				if certs[i].Name == wildcard {
					found = true
					break
				}
			}
			if !found {
				sansJSON := `["` + wildcard + `"]`
				now := time.Now().UTC()
				row := &models.SharedCertificate{
					ID:        ids.NewULID(),
					Name:      wildcard,
					UserID:    nil, // server-wide
					SANs:      &sansJSON,
					Source:    models.SharedCertSourceACME,
					CreatedAt: now,
					UpdatedAt: now,
				}
				if cerr := r.sharedCerts.Create(ctx, row); cerr != nil {
					r.log.Error("preview: create wildcard cert row failed", "err", cerr)
				} else {
					r.log.Info("preview: requested wildcard certificate", "name", wildcard)
				}
			}
		}
	}

	// 2. Wildcard A/AAAA records in the base's zone (local zones only).
	if zone == nil {
		r.log.Info("preview: base has no locally-hosted zone — DNS + cert are managed externally, previews serve via the external resolution", "base", base)
	} else if rows, lerr := r.dnsRecords.ListByZoneID(ctx, zone.ID); lerr != nil {
		r.log.Error("preview: list base zone records failed", "err", lerr)
	} else {
		r.ensurePreviewWildcardRecords(ctx, zone, wildcard, srv, rows)
	}

	// 3. Catch-all vhost — applies for EVERY base, foreign ones
	// included: unknown slugs must land on the branded 404, not the
	// default-vhost drop. Content-hash gated agent-side; re-sent each
	// tick so a cert issued later upgrades it to TLS.
	if r.agent != nil {
		st := r.previewStateCached(ctx)
		fbParams := map[string]any{"base": base}
		// The fallback must join the IP-specific listen pools (M24
		// per-domain binds) or nginx never considers it for connections
		// to the public IP — see the agent-side comment.
		if srv.PublicIPv4 != "" {
			fbParams["listen_ipv4"] = srv.PublicIPv4
		}
		if srv.PublicIPv6 != "" {
			fbParams["listen_ipv6"] = srv.PublicIPv6
		}
		if st.certPath != "" {
			fbParams["ssl_cert_path"] = st.certPath
			fbParams["ssl_key_path"] = st.keyPath
		}
		fbCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if _, err := r.agent.Call(fbCtx, "preview.fallback_apply", fbParams); err != nil {
			r.log.Warn("preview: fallback vhost apply failed", "err", err)
		}
		cancel()
	}
}

// ensurePreviewWildcardRecords converges the wildcard A/AAAA rows for
// base inside its locally-hosted zone. The zone's owning domain pushes
// it to pdns every tick, so a row insert here is live on the next push.
func (r *Reconciler) ensurePreviewWildcardRecords(ctx context.Context, zone *models.DNSZone, wildcard string, srv *models.ServerSettings, rows []models.DNSRecord) {
	have := map[string]bool{}
	for i := range rows {
		if rows[i].Name == wildcard {
			have[rows[i].Type] = true
		}
	}
	mk := func(typ, content string) {
		managedBy := previewManagedBy
		now := time.Now().UTC()
		rec := &models.DNSRecord{
			ID:        ids.NewULID(),
			ZoneID:    zone.ID,
			Name:      wildcard,
			Type:      typ,
			Content:   content,
			TTL:       models.EffectiveDNSTTL(srv),
			Managed:   true,
			ManagedBy: &managedBy,
			IsEnabled: true,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := r.dnsRecords.Create(ctx, rec); err != nil {
			r.log.Error("preview: create wildcard record failed", "type", typ, "err", err)
		} else {
			r.log.Info("preview: wildcard DNS record added", "name", wildcard, "type", typ, "content", content)
		}
	}
	if srv.PublicIPv4 != "" && !have["A"] {
		mk("A", srv.PublicIPv4)
	}
	if srv.PublicIPv6 != "" && !have["AAAA"] {
		mk("AAAA", srv.PublicIPv6)
	}
}

// findZoneForPreviewBase walks the base's parent chain until a local
// dns_zones row matches ("preview.example.com" tries itself, then
// "example.com", …). nil = no locally-hosted zone serves the base.
func (r *Reconciler) findZoneForPreviewBase(ctx context.Context, base string) *models.DNSZone {
	labels := strings.Split(strings.TrimSuffix(base, "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		if z, err := r.dnsZones.FindByName(ctx, candidate); err == nil && z != nil {
			return z
		}
	}
	return nil
}

// previewParams returns the agent payload additions for one domain, or
// nil when previews are off for it.
func (r *Reconciler) previewParams(ctx context.Context, domain *models.Domain) map[string]string {
	if !domain.TempURLEnabled {
		return nil
	}
	st := r.previewStateCached(ctx)
	if st.base == "" {
		return nil
	}
	out := map[string]string{
		"preview_host": models.PreviewHost(domain.Name, st.base),
	}
	if st.certPath != "" {
		out["preview_cert_path"] = st.certPath
		out["preview_key_path"] = st.keyPath
	}
	return out
}
