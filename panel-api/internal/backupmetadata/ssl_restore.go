package backupmetadata

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: an account backup carries each domain's certificate and key (the
// agent reads them into MetadataSSLCert.CertPEM/KeyPEM). A restore whose
// certificate isn't on this server installs them when Deps.RestoreCertificates
// allows it and they pass the checks, and the domain serves them until Let's
// Encrypt takes over (the reconciler's hand-back of a restored certificate).
// Otherwise the domain starts over like a new one, and Let's Encrypt issues a
// certificate once its DNS points here.

// minRestoredCertLife is how long a certificate must still be valid to be
// worth installing.
const minRestoredCertLife = 24 * time.Hour

// restoredCertRoots verifies the chain of a certificate from an uploaded
// file; nil uses the system's roots. Tests set their own.
var restoredCertRoots *x509.CertPool

// setRestoredSSLMode gives row, a domain restored from dm, the backup's
// certificate mode, and returns a report line when it can't. A domain that
// used a shared certificate gets one of its own: the shared certificate is
// this server's, not the account's.
func setRestoredSSLMode(row *models.Domain, dm internalbackup.MetadataDomain) []string {
	row.SkipAutoSAN = dm.SkipAutoSAN
	switch mode := strings.TrimSpace(dm.SSLMode); {
	case mode == "":
	case mode == models.SSLModeShared:
		row.SSLMode = models.SSLModeLE
		return []string{"it used a shared certificate, which isn't part of the backup; it gets a Let's Encrypt certificate of its own"}
	case models.ValidSSLMode(mode):
		row.SSLMode = mode
	default:
		return []string{fmt.Sprintf("certificate mode %q is not one the panel knows; it gets a Let's Encrypt certificate", mode)}
	}
	return nil
}

// checkRestoredCert holds the certificate and key a backup carries for domain
// to what serving them needs: they pair, the certificate covers the domain,
// and it is valid now and for a while yet. A certificate from an uploaded
// file must also chain to a certificate authority this server trusts; one
// from this server's own backup may be the owner's own CA's, as the custom
// certificate page allows.
func checkRestoredCert(certPEM, keyPEM, domain string, verifyChain bool, now time.Time) (*x509.Certificate, error) {
	return checkCertificate(certPEM, keyPEM, domain, verifyChain, restoredCertRoots, now)
}

// checkCertificate is checkRestoredCert with the authorities a verified chain
// must end in; nil is the system's.
func checkCertificate(certPEM, keyPEM, domain string, verifyChain bool, roots *x509.CertPool, now time.Time) (*x509.Certificate, error) {
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("the certificate and key don't pair (%v)", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("the certificate can't be read (%v)", err)
	}
	if leaf.VerifyHostname(domain) != nil {
		return nil, fmt.Errorf("it doesn't cover %s", domain)
	}
	if now.Before(leaf.NotBefore) {
		return nil, fmt.Errorf("it isn't valid until %s", leaf.NotBefore.UTC().Format(time.DateOnly))
	}
	if leaf.NotAfter.Sub(now) < minRestoredCertLife {
		return nil, fmt.Errorf("it expires on %s", leaf.NotAfter.UTC().Format(time.DateOnly))
	}
	if verifyChain {
		intermediates := x509.NewCertPool()
		for _, der := range pair.Certificate[1:] {
			c, err := x509.ParseCertificate(der)
			if err != nil {
				return nil, fmt.Errorf("its chain can't be read (%v)", err)
			}
			intermediates.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{
			DNSName: domain, Intermediates: intermediates, Roots: roots, CurrentTime: now,
		}); err != nil {
			return nil, fmt.Errorf("it isn't signed by a certificate authority this server trusts (%v)", err)
		}
	}
	return leaf, nil
}

// installRestoredCert installs the backup's certificate for row, whose
// certificate (cert, still to be stored) isn't on this server, and points cert
// at it. It returns whether the certificate is installed and, when it isn't,
// the report line saying why; the caller then starts the certificate over. An
// installed certificate needs no line: a report line marks the restore as
// partial.
func installRestoredCert(ctx context.Context, d Deps, row *models.Domain, cert *models.SSLCertificate, mc *internalbackup.MetadataSSLCert, now time.Time) (bool, string) {
	const reissue = "; a new one will be issued"
	switch {
	case row.SSLMode == models.SSLModeSelf || row.SSLMode == models.SSLModeNone:
		return false, "its certificate isn't on this server" + reissue
	case cert.Status != models.SSLStatusIssued && cert.Status != models.SSLStatusRenewing && cert.Status != models.SSLStatusCustom:
		return false, "its certificate isn't on this server" + reissue
	case mc.CertPEM == "" || mc.KeyPEM == "":
		return false, "its certificate isn't on this server, and the backup doesn't carry it" + reissue
	case row.SSLMode != models.SSLModeCustom && !row.OwnershipState.Verified():
		// The reconciler serves a placeholder on a domain whose ownership
		// isn't proven, over any certificate a CA issued.
		return false, "the backup's certificate is not used: the domain's ownership isn't verified" + reissue
	case !d.RestoreCertificates:
		return false, "the backup's certificate was left out (\"Keep the backup's SSL certificates\" was not chosen)" + reissue
	case d.Agent == nil:
		return false, "the backup's certificate can't be installed: the agent is not wired" + reissue
	}
	leaf, err := checkRestoredCert(mc.CertPEM, mc.KeyPEM, row.Name, d.Untrusted, now)
	if err != nil {
		return false, "the backup's certificate is not used: " + err.Error() + reissue
	}
	if err := placeCertificate(ctx, d.Agent, row, cert, mc.CertPEM, mc.KeyPEM, leaf); err != nil {
		return false, fmt.Sprintf("installing the backup's certificate failed (%s)", err) + reissue
	}
	return true, ""
}

// InstallSourceCertificate installs certPEM and keyPEM, the certificate a
// migration's source server served for row, the way a restore installs a
// backup's: only on a domain whose ownership is proven, and only when they
// pair, cover row's name, stay valid for a while yet and chain to an
// authority in roots (nil: this server's). It points cert at the installed
// files in the state the reconciler hands to Let's Encrypt once the name
// points here (handBackRestoredCert), and returns whether it installed and,
// when it didn't, why. It never returns key material.
func InstallSourceCertificate(ctx context.Context, ag agent.AgentInterface, row *models.Domain, cert *models.SSLCertificate, certPEM, keyPEM string, roots *x509.CertPool, now time.Time) (bool, string) {
	switch {
	case row == nil || cert == nil:
		return false, "no domain row"
	case row.SSLMode == models.SSLModeSelf || row.SSLMode == models.SSLModeNone:
		return false, "the domain is set to serve no certificate authority's certificate"
	case row.SSLMode != models.SSLModeCustom && !row.OwnershipState.Verified():
		return false, "the domain's ownership isn't verified"
	case ag == nil:
		return false, "the agent is not wired"
	}
	leaf, err := checkCertificate(certPEM, keyPEM, row.Name, true, roots, now)
	if err != nil {
		return false, "the source's certificate is not used: " + err.Error()
	}
	if err := placeCertificate(ctx, ag, row, cert, certPEM, keyPEM, leaf); err != nil {
		return false, fmt.Sprintf("installing the source's certificate failed (%s)", err)
	}
	return true, ""
}

// placeCertificate has the agent install certPEM and keyPEM, checked against
// leaf, as row's certificate and points cert at the files.
func placeCertificate(ctx context.Context, ag agent.AgentInterface, row *models.Domain, cert *models.SSLCertificate, certPEM, keyPEM string, leaf *x509.Certificate) error {
	raw, err := ag.Call(ctx, "ssl.install_custom", map[string]any{
		"domain": row.Name, "cert_pem": certPEM, "key_pem": keyPEM,
	})
	if err != nil {
		return fmt.Errorf("%s", firstLineOf(err))
	}
	var res struct {
		CertPath string `json:"cert_path"`
		KeyPath  string `json:"key_path"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || !ownCertFiles(row.Name, &res.CertPath, &res.KeyPath) {
		return fmt.Errorf("the agent named no certificate files of this domain")
	}
	issued, expires := leaf.NotBefore.UTC(), leaf.NotAfter.UTC()
	cert.CertPath, cert.KeyPath = &res.CertPath, &res.KeyPath
	cert.IssuedAt, cert.ExpiresAt = &issued, &expires
	if row.SSLMode == models.SSLModeCustom {
		cert.Status, cert.IssueMethod = models.SSLStatusCustom, ""
		return nil
	}
	cert.Status, cert.IssueMethod = models.SSLStatusIssued, models.SSLIssueMethodRestored
	return nil
}

// firstLineOf is err's first line: an agent error can carry command output.
func firstLineOf(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
