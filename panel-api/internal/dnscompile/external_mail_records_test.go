package dnscompile

import (
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func ids() func() string {
	n := 0
	return func() string { n++; return "id" }
}

func recStr(rs []models.DNSRecord) string {
	var b strings.Builder
	for _, r := range rs {
		mb := ""
		if r.ManagedBy != nil {
			mb = *r.ManagedBy
		}
		b.WriteString(r.Name + " " + r.Type + " " + r.Content + " [" + mb + "]\n")
	}
	return b.String()
}

func TestBuildApexMailRecords_PerProvider(t *testing.T) {
	srv := &models.ServerSettings{PublicIPv4: "1.2.3.4"}
	now := time.Unix(0, 0)

	// m365 — dashed MX, M365 SPF, exactly one of each, all mail-apex scoped.
	m365 := BuildApexMailRecords(models.MailProviderM365, "example.com", srv, ids(), now)
	s := recStr(m365)
	for _, want := range []string{
		"@ MX example-com.mail.protection.outlook.com [mail-apex]",
		`@ TXT "v=spf1 include:spf.protection.outlook.com -all" [mail-apex]`,
		"_dmarc TXT", "[mail-apex]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("m365 apex missing %q in:\n%s", want, s)
		}
	}
	if n := strings.Count(s, " TXT \"v=spf1"); n != 1 {
		t.Errorf("m365 must have exactly one apex SPF, got %d:\n%s", n, s)
	}

	// google
	g := recStr(BuildApexMailRecords(models.MailProviderGoogle, "example.com", srv, ids(), now))
	if !strings.Contains(g, "@ MX smtp.google.com [mail-apex]") ||
		!strings.Contains(g, `@ TXT "v=spf1 include:_spf.google.com ~all" [mail-apex]`) {
		t.Errorf("google apex wrong:\n%s", g)
	}

	// jabali — restores the bootstrap-shape apex
	j := recStr(BuildApexMailRecords(models.MailProviderJabali, "example.com", srv, ids(), now))
	if !strings.Contains(j, "@ MX mail.example.com [mail-apex]") ||
		!strings.Contains(j, `@ TXT "v=spf1 mx ip4:1.2.3.4 ~all" [mail-apex]`) {
		t.Errorf("jabali apex wrong:\n%s", j)
	}

	// none — no apex mail records at all
	if r := BuildApexMailRecords(models.MailProviderNone, "example.com", srv, ids(), now); len(r) != 0 {
		t.Errorf("none must have no apex mail records, got %d", len(r))
	}
}

func TestBuildProviderMailRecords_NonApex(t *testing.T) {
	now := time.Unix(0, 0)
	// m365 without token -> just autodiscover; with token -> + selectors
	m := recStr(BuildProviderMailRecords(models.MailProviderM365, "example.com", "", "", nil, ids(), now))
	if !strings.Contains(m, "autodiscover CNAME autodiscover.outlook.com [mail-provider-m365]") {
		t.Errorf("m365 autodiscover missing:\n%s", m)
	}
	if strings.Contains(m, "selector1") {
		t.Errorf("m365 selectors should be absent without token:\n%s", m)
	}
	mt := recStr(BuildProviderMailRecords(models.MailProviderM365, "example.com", "contoso.onmicrosoft.com", "", nil, ids(), now))
	if !strings.Contains(mt, "selector1._domainkey CNAME selector1-example-com._domainkey.contoso.onmicrosoft.com [mail-provider-m365]") {
		t.Errorf("m365 selector1 wrong:\n%s", mt)
	}
	// google DKIM only when token present
	if r := BuildProviderMailRecords(models.MailProviderGoogle, "example.com", "", "", nil, ids(), now); len(r) != 0 {
		t.Errorf("google without DKIM token should be empty, got %d", len(r))
	}
	gt := recStr(BuildProviderMailRecords(models.MailProviderGoogle, "example.com", "", "v=DKIM1; k=rsa; p=ABC", nil, ids(), now))
	if !strings.Contains(gt, `google._domainkey TXT "v=DKIM1; k=rsa; p=ABC" [mail-provider-google]`) {
		t.Errorf("google DKIM wrong:\n%s", gt)
	}
}

func TestMailProviderTokens(t *testing.T) {
	if got, _ := NormaliseM365Onmicrosoft("Contoso"); got != "contoso.onmicrosoft.com" {
		t.Errorf("normalise bare label = %q", got)
	}
	if got, _ := NormaliseM365Onmicrosoft("contoso.onmicrosoft.com"); got != "contoso.onmicrosoft.com" {
		t.Errorf("normalise full = %q", got)
	}
	if _, err := NormaliseM365Onmicrosoft("bad tenant!"); err == nil {
		t.Error("expected reject for bad tenant")
	}
	if _, err := ValidateGoogleDKIM("v=DKIM1; p=" + strings.Repeat("A", 3000)); err == nil {
		t.Error("expected reject for over-long DKIM")
	}
	if _, err := ValidateGoogleDKIM(`has"quote`); err == nil {
		t.Error("expected reject for quote in DKIM")
	}
}
