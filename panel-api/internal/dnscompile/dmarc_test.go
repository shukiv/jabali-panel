package dnscompile

import "testing"

func TestBuildDMARCString(t *testing.T) {
	const base = `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; rua=mailto:postmaster@example.com"`
	cases := map[string]struct {
		zone    string
		np      string
		testing bool
		want    string
	}{
		"default":          {"example.com", "", false, base},
		"np=reject":        {"example.com", "reject", false, `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; rua=mailto:postmaster@example.com; np=reject"`},
		"testing":          {"example.com", "", true, `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; rua=mailto:postmaster@example.com; t=y"`},
		"np+testing":       {"example.com", "quarantine", true, `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; rua=mailto:postmaster@example.com; np=quarantine; t=y"`},
		"bogus np omitted": {"example.com", "bogus", false, base},
		"zone normalised":  {" Example.COM. ", "", false, base},
		"idn zone":         {"xn--bcher-kva.example", "", false, `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; rua=mailto:postmaster@xn--bcher-kva.example"`},
	}
	for name, c := range cases {
		if got := BuildDMARCString(c.zone, c.np, c.testing); got != c.want {
			t.Errorf("%s: got %s want %s", name, got, c.want)
		}
	}
}

// A zone name that is not a plain DNS name must not reach the record: it could
// add tags (a second rua, p=none) or break the TXT quoting. The record is then
// rendered without rua.
func TestBuildDMARCString_UnusableZoneOmitsRUA(t *testing.T) {
	const noRUA = `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"`
	for _, zone := range []string{
		"",
		"localhost",
		"example.com; p=none",
		"example.com,mailto:x@evil.example",
		`example.com" "v=DMARC1; p=none`,
		"exa mple.com",
		"-bad.example.com",
		"under_score.example.com",
	} {
		if got := BuildDMARCString(zone, "", false); got != noRUA {
			t.Errorf("zone %q: got %s want %s", zone, got, noRUA)
		}
	}
}

func TestIsCanonicalDMARC(t *testing.T) {
	// Records rendered before rua was added must stay canonical so the
	// reconciler upgrades them (not treat them as an operator edit).
	for _, legacy := range []string{
		`"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"`,
		`"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; np=reject; t=y"`,
	} {
		if !IsCanonicalDMARC("example.com", legacy) {
			t.Errorf("pre-rua record must be canonical: %s", legacy)
		}
	}
	if !IsCanonicalDMARC("example.com", BuildDMARCString("example.com", "reject", true)) {
		t.Error("np+testing variant must be canonical")
	}
	// A hand-tuned record must NOT be canonical → the reconciler leaves it alone.
	if IsCanonicalDMARC("example.com", `"v=DMARC1; p=reject; rua=mailto:dmarc@example.com"`) {
		t.Error("operator-customised _dmarc must not be treated as canonical")
	}
	// Nor is jabali's record for another zone.
	if IsCanonicalDMARC("example.com", BuildDMARCString("other.example", "", false)) {
		t.Error("another zone's record must not be canonical for this zone")
	}
}

func TestValidDMARCNP(t *testing.T) {
	for _, v := range []string{"", "none", "quarantine", "reject"} {
		if !ValidDMARCNP(v) {
			t.Errorf("%q should be valid", v)
		}
	}
	for _, v := range []string{"bogus", "y", "p=reject", "REJECT"} {
		if ValidDMARCNP(v) {
			t.Errorf("%q should be invalid", v)
		}
	}
}
