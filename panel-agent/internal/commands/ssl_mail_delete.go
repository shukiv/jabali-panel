package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
)

// ssl.mail.delete — GH #1387 mail-only domain delete.
//
// Removes the per-domain mail TLS lineage (mail.<domain>) that ssl.mail.issue
// created under /etc/letsencrypt/live/. Reuses cleanupCertbotLineage (GH #738):
// `certbot delete --cert-name`, with a renewal-conf-removal floor when certbot
// is absent or the lineage is half-broken. Removing the renewal conf is the
// important part — a dangling conf pointing at deleted live files makes
// `certbot renew` abort box-wide (the #738 failure), so mail-delete MUST take
// the lineage down cleanly rather than leaving an auto-renewing orphan.
//
// Best-effort + idempotent: a lineage that was never issued (no renewal conf)
// is a clean no-op, so re-running the mail-delete is safe.
//
// ORDER CONTRACT: the panel removes the per-domain mail vhost
// (webmail.vhost_remove) BEFORE calling this, so no nginx `ssl_certificate`
// directive still points at the lineage when its files disappear and the next
// `nginx -t` / reload stays green (the cert/vhost delete-parity scar, #754).
type sslMailDeleteParams struct {
	Domain string `json:"domain"`
	// LineagePath is the mail_certificates.lineage_path the panel stored at
	// issue time. certbot names a lineage after its primary domain
	// (mail.<domain>) but can suffix it on re-issue (mail.<domain>-0001), so
	// the stored dir's basename is the authoritative cert-name when present.
	LineagePath string `json:"lineage_path,omitempty"`
}

type sslMailDeleteResponse struct {
	Ok       bool   `json:"ok"`
	CertName string `json:"cert_name"`
}

func sslMailDeleteHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p sslMailDeleteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Domain == "" {
		return nil, fmt.Errorf("domain is required")
	}
	// Guard the exec arg + the renewal-conf path against traversal even though
	// the panel already validated the domain (defense in depth: cleanupCertbot-
	// Lineage builds a filepath from the cert-name and passes it to certbot).
	if err := validateDomainNameForShell(p.Domain); err != nil {
		return nil, fmt.Errorf("invalid domain: %w", err)
	}

	certName := "mail." + p.Domain
	if p.LineagePath != "" {
		if b := filepath.Base(p.LineagePath); b != "" && b != "." && b != "/" {
			certName = b
		}
	}

	cleanupCertbotLineage(ctx, sslLERoot, certName)
	return sslMailDeleteResponse{Ok: true, CertName: certName}, nil
}

func init() {
	Default.Register("ssl.mail.delete", sslMailDeleteHandler)
}
