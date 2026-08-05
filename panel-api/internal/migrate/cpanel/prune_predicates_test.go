package cpanel

import "testing"

// JAB-226 cleanup. The import filter and the `dns prune-service-records`
// cleanup must agree on what counts as a cPanel service hostname. Two copies of
// that list would drift, and the failure mode of drift here is a certificate
// that will not issue — so the cleanup calls these exported wrappers rather than
// keeping its own list.
func TestExportedPredicatesMatchInternalOnes(t *testing.T) {
	for _, name := range []string{"cpanel", "whm", "webdisk", "ftp", "cpcalendars", "cpcontacts"} {
		if IsCPanelServiceRecordName(name) != isCPanelServiceRecord(name) {
			t.Errorf("%s: exported and internal predicates disagree", name)
		}
		if !IsCPanelServiceRecordName(name) {
			t.Errorf("%s should be pruned", name)
		}
	}
	// Customer subdomains must survive the cleanup exactly as they survive import.
	for _, name := range []string{"www", "shop", "staging", "api", "myftp", "ftps", "whmcs"} {
		if IsCPanelServiceRecordName(name) {
			t.Errorf("%q would be deleted — that is a customer subdomain", name)
		}
	}
}

// The mail family is type-sensitive: cPanel writes these as A as often as CNAME,
// which is what the original filter missed.
func TestIsMailInfraRecordName_CoversAAndCNAME(t *testing.T) {
	for _, name := range []string{"webmail", "autoconfig", "autodiscover", "mail"} {
		for _, typ := range []string{"A", "AAAA", "CNAME", "a", "cname"} {
			if !IsMailInfraRecordName(name, typ) {
				t.Errorf("%s %s not matched — it would survive the cleanup and stay in the SAN", name, typ)
			}
		}
	}
}

// TXT/MX are content-matched during import and must NOT be swept by a
// name-only cleanup: an operator's own SPF or a real MX is not import junk.
func TestIsMailInfraRecordName_IgnoresContentMatchedTypes(t *testing.T) {
	for _, typ := range []string{"TXT", "MX", "SRV"} {
		if IsMailInfraRecordName("mail", typ) {
			t.Errorf("mail %s matched by name alone — the cleanup would delete operator records", typ)
		}
	}
	// And a plain customer record named "mail-archive" is not "mail".
	if IsMailInfraRecordName("mail-archive", "A") {
		t.Error("mail-archive matched — prefix matching would eat customer records")
	}
}
