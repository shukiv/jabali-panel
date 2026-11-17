// restore_ssl.go — M35.8 P3: install the certificates the source account
// served, so a migrated site keeps a valid certificate until Let's Encrypt
// takes over.
//
// cPanel's pkgacct stores each installed certificate as ONE file,
// apache_tls/<vhost>: key + certificate + chain. <vhost> is the site's
// servername, which for an addon domain is its backing subdomain (SiteDomain
// maps it to the addon). The directory layout
// apache_tls/<domain>/{combined | certificates + key}, which the DirectAdmin
// converter writes, is read too, and so is HestiaCP's flat
// hestia/ssl/<domain>.{crt,key,ca}. The legacy full-backup layout omits the
// cpmove wrapper; every root is tried.
//
// Every certificate passes backupmetadata.InstallSourceCertificate, the checks
// a restored backup's certificate passes (GH #1993): a domain of the account
// being imported, a key that pairs, a name it covers, validity left, a chain
// to an authority this server trusts. Its row is then recorded as a restored
// certificate, which the reconciler serves until Let's Encrypt can replace it
// (handBackRestoredCert). Files are read without following symlinks, regular
// files only, size-capped. Key material never reaches the report.
//
// Idempotent: re-running on a destination that already serves the same cert
// overwrites it with the identical bytes (atomic rename in the agent).

package cpanel

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupmetadata"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type SSLResult struct {
	Installed int      // per-domain certs installed and recorded
	Skipped   []string // human-readable reasons (path missing, checks failed, …)
}

// SSLDomainFinder looks a domain up by name.
type SSLDomainFinder interface {
	FindByName(ctx context.Context, name string) (*models.Domain, error)
}

// SSLCertStore keeps the domains' certificate rows.
type SSLCertStore interface {
	FindByDomainID(ctx context.Context, domainID string) (*models.SSLCertificate, error)
	Create(ctx context.Context, cert *models.SSLCertificate) error
	UpdateInstalled(ctx context.Context, cert *models.SSLCertificate) error
}

// SSLImport is what ImportSSL installs and records certificates with.
type SSLImport struct {
	Agent   agent.AgentInterface
	Domains SSLDomainFinder
	Certs   SSLCertStore
	// UserID is the account the import restores into: only its own domains
	// get a certificate.
	UserID string
	// Preserve is the operator's opt-in to the source's certificates and
	// private keys (JAB-54, --preserve-source-state).
	Preserve bool
	// Roots are the authorities a certificate must chain to; nil is this
	// server's.
	Roots *x509.CertPool
	Now   func() time.Time
}

// sslEntryMaxBytes caps one certificate file read from the archive.
const sslEntryMaxBytes = 256 << 10

const sslReissue = "; Let's Encrypt issues a new certificate once the domain points here"

func ImportSSL(ctx context.Context, in SSLImport, parsed *ParsedTarball) (*SSLResult, error) {
	if parsed == nil {
		return nil, errors.New("ImportSSL: parsed nil")
	}
	res := &SSLResult{}
	if in.Agent == nil {
		res.Skipped = append(res.Skipped, "ssl_skip:agent_unwired")
		return res, nil
	}

	// JAB-54: don't auto-install the source's custom certificate + PRIVATE KEY
	// unless the operator opts in — a leaked/compromised source key must not be
	// silently trusted on the new host. Skipping is safe: the reconciler's
	// auto-LE path issues a fresh cert for every migrated (domain, vhost), so
	// the domain degrades to a clean Let's Encrypt cert, not to no TLS.
	if !in.Preserve {
		res.Skipped = append(res.Skipped, "ssl_quarantined:source certs not installed; reconciler auto-LE issues fresh certs (opt in with --preserve-source-state)")
		return res, nil
	}
	if in.Domains == nil || in.Certs == nil {
		res.Skipped = append(res.Skipped, "ssl_skip:repositories_unwired")
		return res, nil
	}
	if in.Now == nil {
		in.Now = time.Now
	}

	apacheTLS := apacheTLSRoot(parsed)
	if apacheTLS == "" {
		// GH #327: HestiaCP has no cpanel apache_tls/ dir.
		if !scanHestiaSSL(ctx, in, parsed, res) {
			res.Skipped = append(res.Skipped, "ssl: no installed certificates in the archive"+sslReissue)
		}
		return res, nil
	}

	entries, err := os.ReadDir(apacheTLS)
	if err != nil {
		res.Skipped = append(res.Skipped, fmt.Sprintf("ssl_read_apache_tls:%v", err))
		return res, nil
	}
	// Its warnings are reported with the site files (restore_home_split).
	addons, _ := AddonDomainsBySubdomain(parsed)
	for _, e := range entries {
		name := e.Name()
		certPEM, keyPEM, why := readTLSEntry(apacheTLS, name)
		if why != "" {
			res.Skipped = append(res.Skipped, fmt.Sprintf("ssl_skip:%s: %s%s", name, why, sslReissue))
			continue
		}
		installSourceCert(ctx, in, name, SiteDomain(addons, name), certPEM, keyPEM, res)
	}
	return res, nil
}

// apacheTLSRoot is the archive's apache_tls directory, or "" when it has
// none. A symlink in its place doesn't count.
func apacheTLSRoot(parsed *ParsedTarball) string {
	for _, r := range []string{
		filepath.Join(parsed.ExtractDir, "cpmove-"+parsed.SourceUser, "apache_tls"),
		filepath.Join(parsed.ExtractDir, "apache_tls"),
		filepath.Join(parsed.ExtractDir, "cp", parsed.SourceUser, "apache_tls"),
	} {
		if fi, err := os.Lstat(r); err == nil && fi.IsDir() {
			return r
		}
	}
	return ""
}

// readTLSEntry reads one apache_tls entry: pkgacct's single file, or a
// directory with `combined` or `certificates` + `key`. It returns the
// certificate blocks (leaf + chain) and the key block, or why it can't.
func readTLSEntry(root, name string) (string, string, string) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", "", "not a valid entry name"
	}
	fi, err := os.Lstat(filepath.Join(root, name))
	if err != nil {
		return "", "", "unreadable"
	}
	var certPEM, keyPEM string
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return "", "", "a symbolic link; not read"
	case fi.Mode().IsRegular():
		raw, err := readRegularBeneath(root, name, sslEntryMaxBytes)
		if err != nil {
			return "", "", "unreadable (" + err.Error() + ")"
		}
		certPEM, keyPEM = splitPEMByType(raw)
	case fi.IsDir():
		if raw, err := readRegularBeneath(root, name+"/combined", sslEntryMaxBytes); err == nil && len(raw) > 0 {
			certPEM, keyPEM = splitPEMByType(raw)
			break
		}
		certRaw, cerr := readRegularBeneath(root, name+"/certificates", sslEntryMaxBytes)
		keyRaw, kerr := readRegularBeneath(root, name+"/key", sslEntryMaxBytes)
		if cerr != nil || kerr != nil {
			return "", "", "no readable combined file, nor certificates and key"
		}
		certPEM, _ = splitPEMByType(certRaw)
		_, keyPEM = splitPEMByType(keyRaw)
	default:
		return "", "", "not a regular file"
	}
	if certPEM == "" || keyPEM == "" {
		return "", "", "missing certificate or key"
	}
	return certPEM, keyPEM, ""
}

// installSourceCert installs certPEM and keyPEM, read from the archive entry
// entry, as domain's certificate and records the row; or reports why not.
func installSourceCert(ctx context.Context, in SSLImport, entry, domain, certPEM, keyPEM string, res *SSLResult) {
	label := domain
	if entry != domain {
		label = entry + " (" + domain + ")"
	}
	skip := func(why string) {
		res.Skipped = append(res.Skipped, fmt.Sprintf("ssl_skip:%s: %s%s", label, why, sslReissue))
	}
	name := domainops.NormalizeDomainName(domain)
	if err := domainops.ValidateDomainName(name); err != nil {
		skip("not a valid domain name")
		return
	}
	row, err := in.Domains.FindByName(ctx, name)
	switch {
	case errors.Is(err, repository.ErrNotFound) || (err == nil && row == nil):
		skip("no domain of that name on this server")
		return
	case err != nil:
		skip("looking the domain up failed")
		return
	case row.UserID != in.UserID:
		skip("not a domain of the account being imported")
		return
	}

	cert, err := in.Certs.FindByDomainID(ctx, row.ID)
	isNew := false
	switch {
	case errors.Is(err, repository.ErrNotFound) || (err == nil && cert == nil):
		now := in.Now().UTC()
		cert, isNew = &models.SSLCertificate{ID: ids.NewULID(), DomainID: row.ID, CreatedAt: now, UpdatedAt: now}, true
	case err != nil:
		skip("reading its certificate row failed")
		return
	}
	if ok, why := backupmetadata.InstallSourceCertificate(ctx, in.Agent, row, cert, certPEM, keyPEM, in.Roots, in.Now()); !ok {
		skip(why)
		return
	}
	cert.LastError, cert.NextRetryAt, cert.RetryCount = nil, nil, 0
	if isNew {
		err = in.Certs.Create(ctx, cert)
	} else {
		err = in.Certs.UpdateInstalled(ctx, cert)
	}
	if err != nil {
		res.Skipped = append(res.Skipped, fmt.Sprintf("ssl_skip:%s: installed, but recording it failed (%v); the panel may replace it", label, err))
		return
	}
	res.Installed++
}

// scanHestiaSSL installs HestiaCP's per-web-domain certificates. Hestia stores
// them FLAT under <extractDir>/hestia/ssl/<domain>.crt (leaf), .key (private
// key), .ca (chain), not cpanel's apache_tls/ (GH #327). A domain without a
// .crt and .key has no installed certificate; Let's Encrypt issues one. The
// mail cert (mail/<domain>/hestia/<domain>.pem) is a separate stage. It
// reports whether the archive has a hestia/ssl directory at all.
func scanHestiaSSL(ctx context.Context, in SSLImport, parsed *ParsedTarball, res *SSLResult) bool {
	sslDir := filepath.Join(parsed.ExtractDir, "hestia", "ssl")
	if fi, err := os.Lstat(sslDir); err != nil || !fi.IsDir() {
		return false
	}
	for _, domain := range parsed.DomainNames {
		crt, cerr := readRegularBeneath(sslDir, domain+".crt", sslEntryMaxBytes)
		key, kerr := readRegularBeneath(sslDir, domain+".key", sslEntryMaxBytes)
		if cerr != nil || kerr != nil || len(crt) == 0 || len(key) == 0 {
			continue
		}
		// Append the CA chain (if present) so nginx serves a full chain.
		certPEM := strings.TrimSpace(string(crt))
		if ca, err := readRegularBeneath(sslDir, domain+".ca", sslEntryMaxBytes); err == nil && len(ca) > 0 {
			certPEM = certPEM + "\n" + strings.TrimSpace(string(ca))
		}
		installSourceCert(ctx, in, domain, domain, certPEM, strings.TrimSpace(string(key)), res)
	}
	return true
}

// splitPEMByType walks every PEM block in `raw` + emits two
// strings: one concatenating CERTIFICATE blocks (leaf + chain) and
// one concatenating PRIVATE KEY blocks (RSA / EC / PKCS#8).
func splitPEMByType(raw []byte) (string, string) {
	var certs, keys strings.Builder
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case "CERTIFICATE":
			_ = pem.Encode(&certs, block)
		case "RSA PRIVATE KEY", "EC PRIVATE KEY", "PRIVATE KEY":
			_ = pem.Encode(&keys, block)
		}
	}
	return strings.TrimSpace(certs.String()), strings.TrimSpace(keys.String())
}
