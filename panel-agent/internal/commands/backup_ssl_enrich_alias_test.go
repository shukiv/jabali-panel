package commands

import (
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: a certificate that also covers one of the domain's verified web
// domain aliases (GH #1625) is the account's own, so the backup carries it. A
// pending alias isn't the account's yet, and an alias admits only its own
// name: a wildcard or a name under it stays out.
func TestEnrichSSLCertificates_VerifiedAliasNamesAreTheAccounts(t *testing.T) {
	root := enrichLE(t)
	for _, tc := range []struct {
		name    string
		sans    []string
		status  string
		carried bool
	}{
		{"a verified alias", []string{"alice.org", "shop.example.net"}, "verified", true},
		{"a pending alias", []string{"alice.org", "shop.example.net"}, "pending", false},
		{"a wildcard of an alias", []string{"alice.org", "*.shop.example.net"}, "verified", false},
		{"a name under an alias", []string{"alice.org", "x.shop.example.net"}, "verified", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert, key := genTestCertKey(t, tc.sans, "alice.org")
			cp, kp := writeLineage(t, root, "alice.org", cert, key)
			t.Cleanup(func() { removeLineage(t, root) })
			d := enrichDomain("alice.org", "issued", cp, kp)
			d.Aliases = []backup.MetadataDomainAlias{{Hostname: "shop.example.net", OwnershipStatus: tc.status}}
			meta := &backup.AccountMetadata{Domains: []backup.MetadataDomain{d}}

			enrichSSLCertificates(meta)

			if got := meta.Domains[0].SSLCertificate.CertPEM != ""; got != tc.carried {
				t.Fatalf("carried %v, want %v", got, tc.carried)
			}
		})
	}
}

func removeLineage(t *testing.T, root string) {
	t.Helper()
	for _, dir := range []string{"live", "archive"} {
		if err := os.RemoveAll(filepath.Join(root, dir)); err != nil {
			t.Fatal(err)
		}
	}
}
