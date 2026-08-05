package main

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The prune classifies "mail" as a cPanel service hostname because the IMPORTER
// skips it — jabali publishes its own mail A record when mail is enabled on the
// domain. For zones that were imported BEFORE that filter existed, jabali never
// published one, so the imported CNAME is the only thing making the name
// resolve.
//
// Measured on a live destination host: 69 of 71 migrated domains had their only
// mail.<zone> record as that CNAME, and every one of those zones had
// MX -> mail.<zone>. Pruning them would have made the MX target unresolvable,
// and external senders cannot deliver to a domain whose MX does not resolve.
//
// That is a mail outage traded for a certificate cleanup. These tests pin the
// refusal.

func rec(name, typ, content string) models.DNSRecord {
	return models.DNSRecord{Name: name, Type: typ, Content: content}
}

func TestWouldDanglingMX_ProductionShape(t *testing.T) {
	// Verbatim from the live box: MX -> mail.4lion.com, and the only
	// mail.4lion.com record is the imported CNAME to the apex.
	recs := []models.DNSRecord{
		rec("@", "MX", "mail.4lion.com"),
		rec("mail", "CNAME", "4lion.com."),
		rec("@", "A", "182.54.236.69"),
		rec("cpanel", "A", "1.2.3.4"),
		rec("webmail", "A", "1.2.3.4"),
	}
	mx, addressed := mailRoutingNames("4lion.com", recs)

	if !mx["mail"] {
		t.Fatalf("MX target not recognised: %v", mx)
	}
	if !wouldDanglingMX("mail", "CNAME", mx, addressed) {
		t.Error("mail CNAME is the MX target's only record — deleting it breaks inbound mail")
	}
	// The genuinely dead cPanel names stay deletable; refusing everything would
	// make the command useless and leave the certificate problem unfixed.
	for _, n := range []string{"cpanel", "webmail"} {
		if wouldDanglingMX(n, "A", mx, addressed) {
			t.Errorf("%s is not an MX target and must still be pruned", n)
		}
	}
}

func TestWouldDanglingMX_SafeOnceJabaliPublishesItsOwn(t *testing.T) {
	// Mail enabled in the panel: jabali publishes mail A -> the box. Now the
	// imported CNAME is genuinely redundant and can go.
	recs := []models.DNSRecord{
		rec("@", "MX", "mail.example.com."),
		rec("mail", "A", "203.0.113.10"),
		rec("mail", "CNAME", "example.com."),
	}
	mx, addressed := mailRoutingNames("example.com", recs)

	if !addressed["mail"] {
		t.Fatal("mail A record not seen")
	}
	if wouldDanglingMX("mail", "CNAME", mx, addressed) {
		t.Error("with a surviving mail A record the CNAME is redundant and prunable")
	}
	// The address record itself is never a service-hostname match, but if it
	// ever became one, deleting the MX target's address is the same outage.
	if !wouldDanglingMX("mail", "A", mx, addressed) {
		t.Error("deleting an MX target's address record must be refused")
	}
}

func TestMailRoutingNames_MXContentForms(t *testing.T) {
	// MX content varies: trailing dot or not, and some backends prepend the
	// priority into the content. Missing the target means the guard silently
	// stops guarding — the failure mode is deletion, so parsing must be liberal.
	cases := []struct {
		name    string
		content string
	}{
		{"bare", "mail.example.com"},
		{"trailing dot", "mail.example.com."},
		{"priority prefixed", "10 mail.example.com."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mx, _ := mailRoutingNames("example.com", []models.DNSRecord{rec("@", "MX", tc.content)})
			if !mx["mail"] {
				t.Fatalf("target not parsed from %q: %v", tc.content, mx)
			}
		})
	}
}

func TestMailRoutingNames_ForeignAndApexTargets(t *testing.T) {
	// An MX pointing outside the zone cannot be broken by deleting a record IN
	// the zone, so it must not make local names undeletable.
	mx, _ := mailRoutingNames("example.com", []models.DNSRecord{
		rec("@", "MX", "aspmx.l.google.com."),
		rec("webmail", "A", "1.2.3.4"),
	})
	if len(mx) != 0 {
		t.Errorf("foreign MX target should register no local label, got %v", mx)
	}
	if wouldDanglingMX("webmail", "A", mx, map[string]bool{"webmail": true}) {
		t.Error("Google-hosted mail must not block pruning local cPanel names")
	}

	// An apex MX target is recorded as "@" so an apex A is never pruned by a
	// future predicate change.
	mx2, _ := mailRoutingNames("example.com", []models.DNSRecord{rec("@", "MX", "example.com.")})
	if !mx2["@"] {
		t.Errorf("apex MX target not recorded: %v", mx2)
	}
}

// The newzaps shape, which is NOT the aramapp shape and nearly caused a second
// outage on the same day.
//
// There the mail record is an A, not a CNAME, and it is still the MX target:
//
//	hoff.co.il        MX  ->  mail.hoff.co.il
//	mail.hoff.co.il   A   ->  <the box>
//
// 26 domains on that host looked like this, including the server's OWN mail
// hostname. The pre-flight query that cleared the box asked for domains that had
// a mail record but NO address record — which by construction excludes exactly
// this set, so it returned zero and read as "safe".
//
// That is why the refusal belongs in the command and not in an operator's query:
// the command sees the records it is about to delete, and cannot ask the wrong
// question about them.
func TestWouldDanglingMX_AddressRecordIsTheMXTarget(t *testing.T) {
	recs := []models.DNSRecord{
		rec("@", "MX", "mail.hoff.co.il"),
		rec("mail", "A", "182.54.236.64"),
		rec("autoconfig", "CNAME", "hoff.co.il."),
		rec("autodiscover", "CNAME", "hoff.co.il."),
	}
	mx, addressed := mailRoutingNames("hoff.co.il", recs)

	if !wouldDanglingMX("mail", "A", mx, addressed) {
		t.Error("mail A is the MX target and its only provider — deleting it breaks inbound mail")
	}
	// The autodiscovery CNAMEs are not MX targets and stay prunable; refusing
	// them too would leave the certificate-SAN problem unfixed.
	for _, n := range []string{"autoconfig", "autodiscover"} {
		if wouldDanglingMX(n, "CNAME", mx, addressed) {
			t.Errorf("%s is not an MX target and must still be pruned", n)
		}
	}
}
