package cpanel

import "testing"

// JAB-226. The per-domain Let's Encrypt certificate builds its SAN from what is
// in DNS, so every junk hostname imported from a cPanel zone becomes a name that
// must pass HTTP-01. Before cutover completes they still resolve to the SOURCE
// box and 404 the challenge — and one failed name fails the WHOLE certificate.
// The domain then sits on a self-signed origin, which behind Cloudflare Full
// (strict) is a hard 526.
//
// Observed on newhighsite: "Requesting a certificate for … and 3 more domains"
// -> autoconfig./autodiscover./mail. 404 -> "Some challenges have failed".

// The regression that let it happen: the mail-infra filter matched these names
// only as CNAME, and cPanel writes them as A at least as often.
func TestIsMailInfraRecord_DropsWebmailFamilyAsAAndCNAME(t *testing.T) {
	for _, name := range []string{"webmail", "autoconfig", "autodiscover"} {
		for _, typ := range []string{"CNAME", "A", "AAAA"} {
			if !isMailInfraRecord(name, typ, "1.2.3.4") {
				t.Errorf("%s %s not filtered — it lands in the cert SAN and 404s HTTP-01", name, typ)
			}
		}
	}
}

func TestIsCPanelServiceRecord(t *testing.T) {
	for _, name := range []string{"cpanel", "whm", "webdisk", "ftp", "cpcalendars", "cpcontacts"} {
		if !isCPanelServiceRecord(name) {
			t.Errorf("%s should be dropped — nothing on jabali serves it", name)
		}
		// Case must not matter; zone files are inconsistent.
		if !isCPanelServiceRecord(upper(name)) {
			t.Errorf("%s not matched case-insensitively", name)
		}
	}
}

// The filter must not eat a customer's real subdomain. "ftp" is the risky one —
// it is a plausible real hostname — but on a cPanel source it is cPanel's own
// service record, and jabali serves SFTP rather than a per-domain ftp host.
// Everything that is not on the list must survive.
func TestIsCPanelServiceRecord_LeavesRealSubdomainsAlone(t *testing.T) {
	for _, name := range []string{
		"www", "shop", "staging", "api", "app", "dev", "blog",
		"cpanel-backup", "myftp", "ftps", "whmcs", // near-misses, must NOT match
	} {
		if isCPanelServiceRecord(name) {
			t.Errorf("%q was dropped — that is a customer subdomain, not a cPanel service host", name)
		}
	}
}

// Apex must never be swallowed by the service filter.
func TestIsCPanelServiceRecord_IgnoresApex(t *testing.T) {
	for _, name := range []string{"", "@"} {
		if isCPanelServiceRecord(name) {
			t.Errorf("apex %q dropped", name)
		}
	}
}

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

// JAB-226 reverses an earlier deliberate choice to keep the source's `mail`
// record. That reason ("dropping it would leave the MX target unresolvable")
// does not hold: every imported MX is dropped unconditionally, and
// dnscompile/bootstrap.go generates both `@ MX mail.<zone>` and
// `mail A <this server>` when the domain uses jabali mail. The imported one is
// redundant at best, an orphan pointing at the OLD box at worst — and it lands
// in the certificate SAN either way.
func TestIsMailInfraRecord_DropsImportedMailRecord(t *testing.T) {
	for _, typ := range []string{"A", "AAAA", "CNAME"} {
		if !isMailInfraRecord("mail", typ, "1.2.3.4") {
			t.Errorf("mail %s survived import — it enters the cert SAN and 404s HTTP-01", typ)
		}
	}
	// Imported MX is dropped regardless, which is why keeping `mail` bought
	// nothing. Pin it so the justification above stays true.
	if !isMailInfraRecord("@", "MX", "mail.example.com") {
		t.Error("imported MX survived — the whole rationale for dropping `mail` rests on this")
	}
}

// Every service hostname a real cPanel zone actually emits, verbatim from
// /var/named/<domain>.db on a live source box. All of them are A records, which
// is exactly what the CNAME-only filter missed.
func TestRealCPanelZone_AllServiceHostnamesFiltered(t *testing.T) {
	realZone := []struct{ name, typ string }{
		{"mail", "CNAME"},
		{"ftp", "A"}, {"webdisk", "A"}, {"cpcalendars", "A"}, {"cpcontacts", "A"},
		{"webmail", "A"}, {"cpanel", "A"}, {"whm", "A"},
		{"autodiscover", "A"}, {"autoconfig", "A"},
	}
	for _, r := range realZone {
		if isMailInfraRecord(r.name, r.typ, "1.2.3.4") || isCPanelServiceRecord(r.name) {
			continue
		}
		t.Errorf("%s %s would be imported — 9 such names in one zone is what failed the cert", r.name, r.typ)
	}
}
