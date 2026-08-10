package dns01

import (
	"context"
	"errors"
	"testing"
)

// nsTable fakes LookupNSExternal: name → hosts. Names absent from the table
// answer definitively-empty (NODATA), the way any label below a zone apex
// does. unreachable=true simulates a full resolver outage.
type nsTable struct {
	answers     map[string][]string
	unreachable bool
}

func (t nsTable) lookup(_ context.Context, name string) ([]string, bool) {
	if t.unreachable {
		return nil, false
	}
	return t.answers[name], true
}

type fakeZones struct {
	ids map[string]string
	err error
}

func (f fakeZones) FindZoneID(_ context.Context, name string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.ids[name], nil
}

func TestAuthoritativeRoute(t *testing.T) {
	ourNS := []string{"ns1.panel.example", "ns2.panel.example"}
	cases := []struct {
		name     string
		domain   string
		ns       nsTable
		cf       ZoneFinder
		want     Provider
		wantZone string
		wantID   string
	}{
		{
			name:   "cloudflare zone covered by token",
			domain: "arama.co.il",
			ns:     nsTable{answers: map[string][]string{"arama.co.il": {"kip.ns.cloudflare.com", "uma.ns.cloudflare.com"}}},
			cf:     fakeZones{ids: map[string]string{"arama.co.il": "zone123"}},
			want:   ProviderCloudflare, wantZone: "arama.co.il", wantID: "zone123",
		},
		{
			name:   "subdomain of cloudflare-hosted parent anchors on the parent zone",
			domain: "pinat.pcc.co.il",
			ns:     nsTable{answers: map[string][]string{"pcc.co.il": {"kip.ns.cloudflare.com"}}},
			cf:     fakeZones{ids: map[string]string{"pcc.co.il": "zonepcc"}},
			want:   ProviderCloudflare, wantZone: "pcc.co.il", wantID: "zonepcc",
		},
		{
			name:   "cloudflare zone NOT covered by token",
			domain: "syflow.io",
			ns:     nsTable{answers: map[string][]string{"syflow.io": {"kip.ns.cloudflare.com"}}},
			cf:     fakeZones{ids: map[string]string{}},
			want:   ProviderNone, wantZone: "syflow.io",
		},
		{
			name:   "cloudflare zone but no token configured",
			domain: "arama.co.il",
			ns:     nsTable{answers: map[string][]string{"arama.co.il": {"kip.ns.cloudflare.com"}}},
			cf:     nil,
			want:   ProviderNone, wantZone: "arama.co.il",
		},
		{
			name:   "jabali-authoritative zone",
			domain: "shop.customer.com",
			ns:     nsTable{answers: map[string][]string{"customer.com": {"ns1.panel.example", "ns2.panel.example"}}},
			want:   ProviderPDNS, wantZone: "customer.com",
		},
		{
			name:   "mixed NS including ours still counts as pdns",
			domain: "customer.com",
			ns:     nsTable{answers: map[string][]string{"customer.com": {"ns.thirdparty.net", "NS1.PANEL.EXAMPLE."}}},
			want:   ProviderPDNS, wantZone: "customer.com",
		},
		{
			name:   "third-party NS is none",
			domain: "elsewhere.com",
			ns:     nsTable{answers: map[string][]string{"elsewhere.com": {"dns1.netvision.net.il"}}},
			want:   ProviderNone, wantZone: "elsewhere.com",
		},
		{
			name:   "no delegation anywhere",
			domain: "ghost.example",
			ns:     nsTable{answers: map[string][]string{}},
			want:   ProviderNone,
		},
		{
			name:   "resolver outage is inconclusive (fail open)",
			domain: "arama.co.il",
			ns:     nsTable{unreachable: true},
			want:   ProviderNone,
		},
		{
			name:   "cf zone lookup error surfaces as none",
			domain: "arama.co.il",
			ns:     nsTable{answers: map[string][]string{"arama.co.il": {"kip.ns.cloudflare.com"}}},
			cf:     fakeZones{err: errors.New("boom")},
			want:   ProviderNone, wantZone: "arama.co.il",
		},
	}
	for _, c := range cases {
		cfg := Config{LookupNS: c.ns.lookup, OurNSHosts: ourNS, CF: c.cf}
		got := AuthoritativeRoute(context.Background(), cfg, c.domain)
		if got.Provider != c.want || got.Zone != c.wantZone || got.CFZoneID != c.wantID {
			t.Errorf("%s: got %+v, want provider=%s zone=%s id=%s", c.name, got, c.want, c.wantZone, c.wantID)
		}
		if got.Provider == ProviderNone && got.Reason == "" {
			t.Errorf("%s: ProviderNone must carry a reason", c.name)
		}
	}
}
