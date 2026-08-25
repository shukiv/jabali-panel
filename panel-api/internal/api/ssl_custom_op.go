// ssl_custom_op.go — the shared Domain Certificate Lifecycle operation for
// operator-supplied ("custom") certificates (JAB-345). The REST handler
// (installCustomSSL) and the `jabali ssl custom install` CLI both route through
// InstallCustomDomainCert so parse/hostname validation, the agent install, the
// authoritative TLS-mode + cert-row persistence, and convergence scheduling
// live in one place instead of two drifting copies.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// CustomCertDeps is the narrow collaborator set InstallCustomDomainCert needs,
// so both the gin handler and the cobra command wire it from their own globals.
type CustomCertDeps struct {
	Agent    agent.AgentInterface
	Domains  repository.DomainRepository
	SSLCerts repository.SSLCertificateRepository
}

// CustomCertResult is what a successful install reports back for rendering.
type CustomCertResult struct {
	CertPath  string
	KeyPath   string
	ExpiresAt time.Time
}

// Typed errors so each Adapter maps its own status/exit code without
// re-implementing the classification. Wrapped with %w; match with errors.Is.
var (
	// ErrCustomCertInvalid — cert PEM does not parse.
	ErrCustomCertInvalid = errors.New("custom cert: invalid certificate")
	// ErrCustomCertMismatch — cert does not cover the domain.
	ErrCustomCertMismatch = errors.New("custom cert: certificate does not cover the domain")
	// ErrCustomCertAgent — the agent rejected or failed the install (caller's
	// fault for a bad pair; host-side otherwise).
	ErrCustomCertAgent = errors.New("custom cert: agent install failed")
	// ErrCustomCertFinalize — the cert is installed and serving on the host, but
	// recording it in the panel failed. Forward-recovery: re-run the install.
	ErrCustomCertFinalize = errors.New("custom cert: installed on host but panel state not recorded")
	// ErrCustomCertDeps — misconfiguration (nil collaborator).
	ErrCustomCertDeps = errors.New("custom cert: dependencies not wired")
)

const (
	customCertFinalizeTimeout = 30 * time.Second
	customCertFinalizeRetries = 3
)

// InstallCustomDomainCert installs an operator-supplied cert+key for a domain
// and switches it to ssl_mode=custom (JAB-345). It is deliberately
// forward-recovery, never rollback:
//
// ssl.install_custom overwrites the domain's serving fullchain.pem/privkey.pem
// AND de-tracks its certbot renewal lineage (GH #738) BEFORE any DB write — so
// the moment the agent returns, the new files ARE the live serving cert and the
// prior cert/lineage is gone. Removing the freshly-installed files to "roll
// back" a later DB failure would take the domain OFFLINE (nginx reads that
// path), not restore anything. There is no compensating action; the only
// correct direction is to make the authoritative DB writes land.
//
// The realistic failure is the caller's request context dying mid-install
// (client disconnect), so the mode+row finalize runs on a FRESH bounded context
// with bounded retries (mirrors runAccountRestoreJob / GH #1044). If it still
// fails, the files are already live and the whole operation is idempotent — the
// returned ErrCustomCertFinalize names that recovery. Mode is written before the
// row: it is the protective write that stops the reconciler from treating the
// now-lineage-less domain as LE-managed and re-issuing over the custom files.
//
// scheduler is nullable: the REST adapter passes its reconciler for an immediate
// converge; the CLI passes nil (it is out-of-process from the in-process
// reconciler, which converges on its next tick regardless — the JAB-292 limit).
func InstallCustomDomainCert(ctx context.Context, d CustomCertDeps, domain *models.Domain, certPEM, keyPEM string, scheduler SSLScheduler) (*CustomCertResult, error) {
	if d.Agent == nil || d.Domains == nil || d.SSLCerts == nil || domain == nil {
		return nil, fmt.Errorf("%w: agent, domains, ssl-certs and domain are required", ErrCustomCertDeps)
	}

	// Parse + hostname validation (panel-side; the input is never logged).
	leaf, notAfter, err := parseLeafNotAfter(certPEM)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCustomCertInvalid, err)
	}
	if leaf.VerifyHostname(domain.Name) != nil {
		return nil, fmt.Errorf("%w: %s", ErrCustomCertMismatch, domain.Name)
	}

	// Agent install. The agent re-validates the pair and writes them 0600 under
	// /etc/letsencrypt/live/<domain>/. From here on the files are the live cert.
	raw, err := d.Agent.Call(ctx, "ssl.install_custom", map[string]any{
		"domain":   domain.Name,
		"cert_pem": certPEM,
		"key_pem":  keyPEM,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrCustomCertAgent, firstLineSSL(err.Error()))
	}
	var res struct {
		CertPath string `json:"cert_path"`
		KeyPath  string `json:"key_path"`
	}
	if uerr := json.Unmarshal(raw, &res); uerr != nil || res.CertPath == "" {
		return nil, fmt.Errorf("%w: agent returned no cert path", ErrCustomCertAgent)
	}

	if err := finalizeCustomCert(d, domain.ID, res.CertPath, res.KeyPath, notAfter); err != nil {
		return nil, err
	}

	if scheduler != nil {
		scheduler.Schedule(domain.ID)
	}
	return &CustomCertResult{CertPath: res.CertPath, KeyPath: res.KeyPath, ExpiresAt: notAfter}, nil
}

// finalizeCustomCert writes the authoritative state — ssl_mode=custom (the
// protective write, first) then the cert row — on a fresh bounded context with
// bounded retries, because the cert is already live on the host and the writes
// must land even if the caller's context is long dead. Both writes are
// idempotent, so retrying the whole sequence is safe.
func finalizeCustomCert(d CustomCertDeps, domainID, certPath, keyPath string, notAfter time.Time) error {
	var lastErr error
	for attempt := 0; attempt < customCertFinalizeRetries; attempt++ {
		fctx, cancel := context.WithTimeout(context.Background(), customCertFinalizeTimeout)
		lastErr = func() error {
			if err := d.Domains.UpdateSSLMode(fctx, domainID, models.SSLModeCustom); err != nil {
				return fmt.Errorf("switch ssl_mode: %w", err)
			}
			cert, _ := d.SSLCerts.FindByDomainID(fctx, domainID)
			if cert == nil {
				return d.SSLCerts.Create(fctx, &models.SSLCertificate{
					ID:        ids.NewULID(),
					DomainID:  domainID,
					Status:    models.SSLStatusCustom,
					CertPath:  &certPath,
					KeyPath:   &keyPath,
					ExpiresAt: &notAfter,
				})
			}
			return d.SSLCerts.UpdateCustom(fctx, cert.ID, certPath, keyPath, notAfter)
		}()
		cancel()
		if lastErr == nil {
			return nil
		}
	}
	// Deliberately does not echo cert/key material — only the DB error.
	return fmt.Errorf("%w: the certificate is installed and serving on the host, but recording it in the panel failed after %d attempts (%v); re-run the custom cert install — it is idempotent and will not disturb the live files",
		ErrCustomCertFinalize, customCertFinalizeRetries, lastErr)
}
