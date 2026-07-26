package dnscompile

import "testing"

func TestBuildDMARCString(t *testing.T) {
	const base = `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"`
	cases := map[string]struct {
		np      string
		testing bool
		want    string
	}{
		"default (legacy)": {"", false, base},
		"np=reject":        {"reject", false, `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; np=reject"`},
		"testing":          {"", true, `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; t=y"`},
		"np+testing":       {"quarantine", true, `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r; np=quarantine; t=y"`},
		"bogus np omitted": {"bogus", false, base},
	}
	for name, c := range cases {
		if got := BuildDMARCString(c.np, c.testing); got != c.want {
			t.Errorf("%s: got %s want %s", name, got, c.want)
		}
	}
}

func TestIsCanonicalDMARC(t *testing.T) {
	// The pre-#648 hardcoded record must be recognised as canonical so the
	// reconciler will re-render it (not treat it as an operator edit).
	if !IsCanonicalDMARC(`"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"`) {
		t.Error("legacy default must be canonical")
	}
	if !IsCanonicalDMARC(BuildDMARCString("reject", true)) {
		t.Error("np+testing variant must be canonical")
	}
	// A hand-tuned record must NOT be canonical → the reconciler leaves it alone.
	if IsCanonicalDMARC(`"v=DMARC1; p=reject; rua=mailto:dmarc@example.com"`) {
		t.Error("operator-customised _dmarc must not be treated as canonical")
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
