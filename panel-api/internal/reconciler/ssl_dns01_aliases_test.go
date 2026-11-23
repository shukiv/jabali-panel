package reconciler

// GH #2561 — a certificate issued over DNS-01 carries the domain's verified
// aliases: the TXT challenge does not care where an alias's A record points,
// so the HTTP-01 reachability filter (which drops a Cloudflare-proxied alias)
// does not apply. What does apply is whether the ACME hook can write the
// alias's _acme-challenge.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dns01"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

var cfNS = []string{"kip.ns.cloudflare.com", "lia.ns.cloudflare.com"}

// withZones answers NS lookups for the given zone apexes (Cloudflare) and
// nothing else, and wires the domain's verified aliases.
func withZones(r *Reconciler, domainID string, aliases []string, zones ...string) {
	r.dns01LookupNS = func(_ context.Context, name string) ([]string, bool) {
		for _, z := range zones {
			if name == z {
				return cfNS, true
			}
		}
		return nil, true
	}
	r.WithWebDomainAliases(&fakeAliasRepo{byDomain: map[string][]string{domainID: aliases}})
}

func dns01IssuedSANs(t *testing.T, ag *fakeAgent) []string {
	t.Helper()
	call, ok := findAgentCall(ag, "ssl.issue_dns01")
	if !ok {
		t.Fatal("no ssl.issue_dns01 call")
	}
	return call.params.(map[string]any)["sans"].([]string)
}

// The reporter's row: primary www.example.com behind Cloudflare, verified
// alias example.com, an issued DNS-01 certificate for [www.example.com,
// www.www.example.com]. The drift pass must see only the alias as missing
// (not the mail helpers a DNS-01 cert never carries) and top the cert up
// over DNS-01 to exactly [www.example.com, example.com].
func TestSANDrift_DNS01CertGetsItsProxiedAlias(t *testing.T) {
	r, ag, _, dom := dns01Fixture(t, cfNS, "zone123")
	dom.Name, dom.EmailEnabled = "www.example.com", true
	withZones(r, dom.ID, []string{"example.com"}, "example.com")
	cp := "/etc/letsencrypt/live/www.example.com/fullchain.pem"
	row := repository.SSLCertificateWithDomain{
		ID: "c1", DomainID: dom.ID, DomainName: dom.Name, Status: models.SSLStatusIssued,
		CertPath: &cp, IssueMethod: issueMethodDNS01, EmailEnabled: true, SSLMode: models.SSLModeLE,
	}
	onCert := []string{"www.example.com", "www.www.example.com"}

	missing := sslSANDriftMissing(row, onCert, r.aliasHostnames(context.Background(), dom.ID))
	if !reflect.DeepEqual(missing, []string{"example.com"}) {
		t.Fatalf("missing = %v, want [example.com]", missing)
	}
	r.expandCertSANsForDrift(context.Background(), row, onCert, missing)

	if agentCalled(ag, "ssl.issue") {
		t.Fatal("a DNS-01 certificate must not be topped up over HTTP-01")
	}
	if got := dns01IssuedSANs(t, ag); !reflect.DeepEqual(got, []string{"www.example.com", "example.com"}) {
		t.Errorf("sans = %v, want [www.example.com example.com]", got)
	}
}

// First issuance: an alias in the base name's own zone goes on; one in another
// (writable) zone waits for the drift pass, so a token that can see but not
// edit that zone cannot cost the domain the certificate it gets today; one
// whose zone no credential reaches stays off.
func TestDNS01Issue_AddsSameZoneAliases(t *testing.T) {
	r, ag, _, dom := dns01Fixture(t, cfNS, "zone123")
	withZones(r, dom.ID, []string{"example.com", "shop.example.net", "other.example.org"}, "example.com", "example.net")

	r.reconcileSSLForDomain(context.Background(), dom)

	want := []string{"sub.example.com", "www.sub.example.com", "example.com"}
	if got := dns01IssuedSANs(t, ag); !reflect.DeepEqual(got, want) {
		t.Errorf("sans = %v, want %v", got, want)
	}
}

// A renewal keeps an other-zone alias the drift pass already put on the live
// DNS-01 certificate (it was validated then), instead of dropping it every
// renewal for the drift pass to add back.
func TestDNS01Renewal_KeepsAliasAlreadyOnTheCert(t *testing.T) {
	r, ag, sc, dom := dns01Fixture(t, cfNS, "zone123")
	withZones(r, dom.ID, []string{"shop.example.net"}, "example.com", "example.net")
	cp := writeNamedCert(t, "sub.example.com", "www.sub.example.com", "shop.example.net")
	exp := time.Now().Add(20 * 24 * time.Hour)
	sc.byDomain[dom.ID] = &models.SSLCertificate{ID: "c1", DomainID: dom.ID, Status: models.SSLStatusRenewing,
		CertPath: &cp, ExpiresAt: &exp, IssueMethod: issueMethodDNS01}

	r.reconcileSSLForDomain(context.Background(), dom)

	want := []string{"sub.example.com", "www.sub.example.com", "shop.example.net"}
	if got := dns01IssuedSANs(t, ag); !reflect.DeepEqual(got, want) {
		t.Errorf("sans = %v, want %v", got, want)
	}
}

// The security boundary is unchanged: a domain owner's own Cloudflare token
// is offered only for names under that owner's domains rows (GH #2032). An
// apex alias of a www. primary has no row of its own, so with only the
// owner's token it is not writable and stays off — the base name still
// issues.
func TestDNS01Issue_OwnerTokenDoesNotCoverAnUnownedAlias(t *testing.T) {
	r, ag, _, dom := dns01Fixture(t, cfNS, "")
	dom.Name = "www.example.com"
	withZones(r, dom.ID, []string{"example.com"}, "example.com")
	key := &ssokey.Key{}
	sealed, err := key.Seal([]byte("owner-token"))
	if err != nil {
		t.Fatal(err)
	}
	r.WithDNS01Key(key).WithCloudflareTokens(&fakeUserCFTokens{rows: map[string]*models.UserCloudflareToken{
		dom.UserID: {UserID: dom.UserID, TokenEnc: sealed, UpdatedAt: time.Now()},
	}})
	r.dns01ZoneFinder = nil
	r.dns01NewFinder = func(token string) dns01.ZoneFinder {
		if token == "owner-token" {
			return fixedZoneFinder{id: "zone-owner"}
		}
		return fixedZoneFinder{}
	}

	r.reconcileSSLForDomain(context.Background(), dom)

	if got := dns01IssuedSANs(t, ag); !reflect.DeepEqual(got, []string{"www.example.com"}) {
		t.Errorf("sans = %v, want [www.example.com]", got)
	}
}

func TestDNS01DesiredSANs(t *testing.T) {
	for _, c := range []struct {
		name    string
		aliases []string
		want    []string
	}{
		{"sub.example.com", nil, []string{"sub.example.com", "www.sub.example.com"}},
		{"www.example.com", []string{"example.com"}, []string{"www.example.com", "example.com"}},
		{"example.com", []string{"Shop.Example.net ", "www.example.com", "shop.example.net"}, []string{"example.com", "www.example.com", "shop.example.net"}},
	} {
		if got := dns01DesiredSANs(c.name, c.aliases); !reflect.DeepEqual(got, c.want) {
			t.Errorf("dns01DesiredSANs(%q, %v) = %v, want %v", c.name, c.aliases, got, c.want)
		}
	}
}

func TestSanHostnamesForDomain_NoWWWWWW(t *testing.T) {
	got := sanHostnamesForDomain(&models.Domain{Name: "www.example.com", CreateWWW: true, SkipAutoSAN: true}, []string{"example.com"})
	if !reflect.DeepEqual(got, []string{"example.com"}) {
		t.Errorf("sanHostnamesForDomain = %v, want [example.com]", got)
	}
	got = sanHostnamesForDomain(&models.Domain{Name: "example.com", CreateWWW: true, SkipAutoSAN: true}, nil)
	if !reflect.DeepEqual(got, []string{"www.example.com"}) {
		t.Errorf("sanHostnamesForDomain = %v, want [www.example.com]", got)
	}
}
