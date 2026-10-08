package commands

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// maxBackupCertFileBytes bounds each certificate file read into a backup: a
// certificate chain or key is a few kilobytes.
const maxBackupCertFileBytes = 256 << 10

// backupCertStatuses are the certificate states whose files are worth
// carrying: one a CA issued, or one the owner uploaded. A self-signed
// certificate is made again on restore.
var backupCertStatuses = map[string]bool{"issued": true, "renewing": true, "custom": true}

// enrichSSLCertificates fills CertPEM and KeyPEM on each domain's
// certificate from its own files under <sslLERoot>/live/<domain>/ (GH #1993),
// so a restore on another server can serve it until Let's Encrypt takes over.
// The panel can't read the private key; the agent can, as with the FTP
// shadow hashes. A certificate is left out when its files aren't the
// domain's own, when it doesn't pair with its key, and when it also covers a
// name outside the account's domains: an administrator's wider certificate,
// say a wildcard of the panel's own domain, must not reach the account's
// backup, which its owner can download. A verified web domain alias of the
// account (GH #1625) counts by its own name only: not a wildcard of it, nor a
// name under it. Best effort: a certificate that can't be read is left out,
// and the restore issues a new one.
func enrichSSLCertificates(meta *backup.AccountMetadata) {
	if meta == nil {
		return
	}
	var own []string
	aliases := map[string]bool{}
	for _, d := range meta.Domains {
		own = append(own, strings.ToLower(d.Name))
		for _, a := range d.Aliases {
			// A pending alias isn't the account's yet.
			if a.OwnershipStatus == "" || a.OwnershipStatus == "verified" {
				aliases[strings.TrimSuffix(strings.ToLower(a.Hostname), ".")] = true
			}
		}
	}
	for i := range meta.Domains {
		d := &meta.Domains[i]
		c := d.SSLCertificate
		if c == nil || !backupCertStatuses[c.Status] || c.CertPath == nil || c.KeyPath == nil {
			continue
		}
		if !sslInstallDomainRegex.MatchString(d.Name) {
			continue
		}
		live := filepath.Join(sslLERoot, "live", d.Name)
		if *c.CertPath != filepath.Join(live, "fullchain.pem") || *c.KeyPath != filepath.Join(live, "privkey.pem") {
			continue
		}
		certPEM, err := readBackupCertFile(*c.CertPath)
		if err != nil {
			continue
		}
		keyPEM, err := readBackupCertFile(*c.KeyPath)
		if err != nil {
			continue
		}
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			continue
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil || !namesWithin(leaf.DNSNames, own, aliases) {
			continue
		}
		c.CertPEM, c.KeyPEM = string(certPEM), string(keyPEM)
	}
}

// readBackupCertFile reads one of a domain's certificate files. certbot's
// live files are symlinks into its archive; the file they name must stay
// under sslLERoot.
func readBackupCertFile(path string) ([]byte, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(sslLERoot)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return nil, os.ErrPermission
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBackupCertFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxBackupCertFileBytes {
		return nil, os.ErrInvalid
	}
	return b, nil
}

// namesWithin reports whether every name is one of the domains or a name
// under one of them (a wildcard counts as the names under its base), or
// exactly one of the exact names.
func namesWithin(names, domains []string, exact map[string]bool) bool {
	if len(names) == 0 {
		return false
	}
	for _, n := range names {
		if exact[strings.ToLower(n)] {
			continue
		}
		n = strings.TrimPrefix(strings.ToLower(n), "*.")
		ok := false
		for _, d := range domains {
			if n == d || strings.HasSuffix(n, "."+d) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}
