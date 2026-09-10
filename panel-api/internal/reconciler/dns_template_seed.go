package reconciler

import (
	"context"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// WithDNSTemplates wires the custom-DNS-template repo so the fresh-zone
// bootstrap can seed a template's records (GH #1627). Optional — without it,
// a domain created from a template simply gets no seeded records.
func (r *Reconciler) WithDNSTemplates(repo repository.DNSTemplateRepository) *Reconciler {
	r.dnsTemplates = repo
	return r
}

// dnsTemplateSeeds returns the DNS records to seed into a freshly-bootstrapped
// zone for a domain created from a custom DNS template (GH #1627). It reads the
// domain's mail_template_id ONCE here — the records are tenant-owned
// (Managed=false) and never re-asserted, mirroring the #1540 dns-only apex
// seeds. The single supported substitution token, {domain}, is replaced with
// the zone name in each record's Content only. Names stay verbatim: DNS record
// names are RELATIVE to the zone (@, mail, autoconfig...) and the zone writer
// re-qualifies them, so a {domain} token in a Name would double-qualify
// (shop.example.com.shop.example.com).
//
// Returns nil (seed nothing) when: the repo is unwired, the domain carries no
// template id, or the template can't be loaded (e.g. it was deleted after the
// domain was created — the id is not a foreign key, so a stale reference simply
// yields no records rather than an error).
func (r *Reconciler) dnsTemplateSeeds(ctx context.Context, domain *models.Domain, zoneID, zoneName string) []models.DNSRecord {
	if r.dnsTemplates == nil || domain == nil || domain.MailTemplateID == nil || *domain.MailTemplateID == "" {
		return nil
	}
	tmpl, err := r.dnsTemplates.FindByID(ctx, *domain.MailTemplateID)
	if err != nil || tmpl == nil {
		// Not-found is expected if the template was deleted post-create; any
		// other error is transient — either way, seed nothing this tick.
		r.log.Warn("dns template seed: template not loaded", "domain", domain.Name, "template_id", *domain.MailTemplateID, "err", err)
		return nil
	}
	now := time.Now().UTC()
	out := make([]models.DNSRecord, 0, len(tmpl.Records))
	for _, tr := range tmpl.Records {
		out = append(out, models.DNSRecord{
			ID:        ids.NewULID(),
			ZoneID:    zoneID,
			Name:      tr.Name,
			Type:      tr.Type,
			Content:   substituteDomainToken(tr.Content, zoneName),
			TTL:       tr.TTL,
			Priority:  tr.Priority,
			Managed:   false, // tenant-owned: seeded once, never re-asserted
			IsEnabled: true,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	return out
}

// substituteDomainToken replaces the {domain} token with the zone name. It is
// the only template token in this phase; richer tokens (server IP, mail host)
// are a later phase.
func substituteDomainToken(s, zoneName string) string {
	return strings.ReplaceAll(s, "{domain}", zoneName)
}
