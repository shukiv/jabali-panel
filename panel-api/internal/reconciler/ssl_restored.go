package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dns01"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// handBackRestoredCert keeps serving a certificate a restore installed from a
// backup (GH #1993) until its renewal window (sslRenewalWindow), then has
// Let's Encrypt issue the domain's own once it can validate the name from
// here (letsEncryptCanValidate). Until then the restored certificate serves:
// a migration whose DNS still points at the old server, or a domain behind a
// proxy this server can't write DNS records for, keeps a working
// certificate. Issuing clears the restored files first, and a failed attempt
// leaves the domain on a self-signed placeholder, as a proxied domain without
// DNS-01 gets today. An expired certificate goes through that path whatever
// the DNS says. The new certificate is certbot's, renewed by its timer.
func (r *Reconciler) handBackRestoredCert(ctx context.Context, domain *models.Domain, cert *models.SSLCertificate) {
	if cert.ExpiresAt != nil && cert.ExpiresAt.After(time.Now().UTC()) {
		if cert.ExpiresAt.Sub(time.Now().UTC()) > sslRenewalWindow || !r.letsEncryptCanValidate(ctx, domain) {
			return
		}
	}
	r.tryACMEOrFallback(ctx, domain, cert)
}

// letsEncryptCanValidate reports whether the route tryACMEOrFallback takes
// for domain can pass: HTTP-01 for a name that resolves to this server,
// DNS-01 for one that resolves elsewhere or serves no website, when this
// server can write its record. A server that doesn't know its own address
// can't tell which route it takes, and a name that doesn't resolve has none.
func (r *Reconciler) letsEncryptCanValidate(ctx context.Context, domain *models.Domain) bool {
	if r.dnsPreflight == nil {
		return false
	}
	srv, err := r.settingsGet(ctx)
	if err != nil || srv == nil {
		return false
	}
	var ours []string
	if srv.PublicIPv4 != "" {
		ours = append(ours, srv.PublicIPv4)
	}
	if srv.PublicIPv6 != "" {
		ours = append(ours, srv.PublicIPv6)
	}
	if len(ours) == 0 {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	addrs, queried := r.dnsPreflight(lookupCtx, domain.Name)
	cancel()
	if !queried || len(addrs) == 0 {
		return false
	}
	if !domain.WebDisabled && intersects(addrs, ours) {
		return true
	}
	return r.dns01Route(ctx, srv, domain.Name).Provider != dns01.ProviderNone
}
