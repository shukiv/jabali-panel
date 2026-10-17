package services

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubResolver struct {
	addrs []string
	err   error
}

func (s stubResolver) LookupHost(_ context.Context, _ string) ([]string, error) {
	return s.addrs, s.err
}

func TestPanelCertRoutability_Check(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		hostname   string
		publicIPv4 string
		resolver   stubResolver
		wantRoute  bool
		wantReason string
	}{
		{
			name:       "empty hostname",
			hostname:   "",
			publicIPv4: "203.0.113.5",
			wantReason: "missing hostname",
		},
		{
			name:       "empty public ipv4",
			hostname:   "panel.example.com",
			publicIPv4: "",
			wantReason: "missing public_ipv4",
		},
		{
			name:       "localhost suffix",
			hostname:   "localhost",
			publicIPv4: "203.0.113.5",
			wantReason: "non-routable hostname suffix",
		},
		{
			name:       "dot local suffix",
			hostname:   "panel.local",
			publicIPv4: "203.0.113.5",
			wantReason: "non-routable hostname suffix",
		},
		{
			name:       "dot localdomain suffix",
			hostname:   "panel.localdomain",
			publicIPv4: "203.0.113.5",
			wantReason: "non-routable hostname suffix",
		},
		{
			name:       "dns lookup error surfaces as not routable",
			hostname:   "panel.example.com",
			publicIPv4: "203.0.113.5",
			resolver:   stubResolver{err: errors.New("nxdomain")},
			wantReason: "dns lookup failed",
		},
		{
			name:       "dns returns no records",
			hostname:   "panel.example.com",
			publicIPv4: "203.0.113.5",
			resolver:   stubResolver{addrs: nil},
			wantReason: "dns lookup returned no records",
		},
		{
			name:       "dns returns ipv6 only",
			hostname:   "panel.example.com",
			publicIPv4: "203.0.113.5",
			resolver:   stubResolver{addrs: []string{"2001:db8::1"}},
			wantReason: "dns lookup returned no IPv4 records",
		},
		{
			name:       "dns mismatch surfaces both got and want",
			hostname:   "panel.example.com",
			publicIPv4: "203.0.113.5",
			resolver:   stubResolver{addrs: []string{"198.51.100.99"}},
			wantReason: "dns points elsewhere (got 198.51.100.99, want 203.0.113.5)",
		},
		{
			name:       "dns match marks routable",
			hostname:   "panel.example.com",
			publicIPv4: "203.0.113.5",
			resolver:   stubResolver{addrs: []string{"203.0.113.5"}},
			wantRoute:  true,
		},
		{
			name:       "dns match among multiple records",
			hostname:   "panel.example.com",
			publicIPv4: "203.0.113.5",
			resolver:   stubResolver{addrs: []string{"198.51.100.99", "203.0.113.5"}},
			wantRoute:  true,
		},
		{
			name:       "uppercase hostname normalised",
			hostname:   "PANEL.EXAMPLE.COM",
			publicIPv4: "203.0.113.5",
			resolver:   stubResolver{addrs: []string{"203.0.113.5"}},
			wantRoute:  true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &PanelCertRoutability{Resolver: tc.resolver}
			got, err := p.Check(context.Background(), tc.hostname, tc.publicIPv4, false)
			if err != nil {
				t.Fatalf("Check returned unexpected error: %v", err)
			}
			if got.Routable != tc.wantRoute {
				t.Fatalf("Routable: got %v, want %v (reason=%q)", got.Routable, tc.wantRoute, got.Reason)
			}
			if !tc.wantRoute && !strings.Contains(got.Reason, tc.wantReason) {
				t.Fatalf("Reason: got %q, want substring %q", got.Reason, tc.wantReason)
			}
		})
	}
}

func TestNewPanelCertRoutability_WiresDefaultResolver(t *testing.T) {
	t.Parallel()
	p := NewPanelCertRoutability()
	if p.Resolver == nil {
		t.Fatalf("constructor must wire a default resolver")
	}
}

// TestPanelCertRoutability_RequirePublic exercises the mail-kind path:
// the local resolver matches publicIPv4, but routability now ALSO depends
// on what external public resolvers see (delegation). Stubs both layers.
func TestPanelCertRoutability_RequirePublic(t *testing.T) {
	t.Parallel()

	const want = "203.0.113.5"
	cases := []struct {
		name       string
		public     func(ctx context.Context, host string) ([]string, bool)
		wantRoute  bool
		wantReason string
	}{
		{
			name:      "public agrees -> routable",
			public:    func(_ context.Context, _ string) ([]string, bool) { return []string{want}, true },
			wantRoute: true,
		},
		{
			name:       "public NXDOMAIN (queried, empty) -> not routable",
			public:     func(_ context.Context, _ string) ([]string, bool) { return nil, true },
			wantRoute:  false,
			wantReason: "no public A record",
		},
		{
			name:       "public points elsewhere -> not routable",
			public:     func(_ context.Context, _ string) ([]string, bool) { return []string{"198.51.100.9"}, true },
			wantRoute:  false,
			wantReason: "public DNS -> 198.51.100.9",
		},
		{
			name:      "public unreachable (inconclusive) -> routable fallback",
			public:    func(_ context.Context, _ string) ([]string, bool) { return nil, false },
			wantRoute: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &PanelCertRoutability{
				Resolver:     stubResolver{addrs: []string{want}},
				PublicLookup: tc.public,
			}
			got, err := p.Check(context.Background(), "mail.panel.example.com", want, true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Routable != tc.wantRoute {
				t.Fatalf("Routable: got %v want %v (reason=%q)", got.Routable, tc.wantRoute, got.Reason)
			}
			if !tc.wantRoute && !strings.Contains(got.Reason, tc.wantReason) {
				t.Fatalf("Reason: got %q want substring %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// GH #2166. The local lookup answers from /etc/hosts (Debian's 127.0.1.1
// line), so the gate asks public DNS instead, and the reason says what public
// DNS said, not the loopback address.
func TestPanelCertRoutability_EtcHostsLoopback(t *testing.T) {
	t.Parallel()

	const want = "203.0.113.5"
	cases := []struct {
		name          string
		requirePublic bool
		public        func(ctx context.Context, host string) ([]string, bool)
		wantRoute     bool
		wantReason    []string
	}{
		{
			name:      "public DNS shows this server",
			public:    func(context.Context, string) ([]string, bool) { return []string{want}, true },
			wantRoute: true,
		},
		{
			name:          "public DNS shows this server, mail gate",
			requirePublic: true,
			public:        func(context.Context, string) ([]string, bool) { return []string{want}, true },
			wantRoute:     true,
		},
		{
			name:   "public DNS shows a CDN",
			public: func(context.Context, string) ([]string, bool) { return []string{"104.21.1.1", "172.67.1.1"}, true },
			wantReason: []string{
				"dns points elsewhere (got 104.21.1.1,172.67.1.1, want 203.0.113.5)",
				"proxied by a CDN such as Cloudflare",
			},
		},
		{
			name:       "no public record",
			public:     func(context.Context, string) ([]string, bool) { return nil, true },
			wantReason: []string{"no public A record", "/etc/hosts"},
		},
		{
			name:       "public DNS unreachable",
			public:     func(context.Context, string) ([]string, bool) { return nil, false },
			wantReason: []string{"could not be reached", "/etc/hosts"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &PanelCertRoutability{
				Resolver:     stubResolver{addrs: []string{"127.0.1.1"}},
				PublicLookup: tc.public,
			}
			got, err := p.Check(context.Background(), "panel.example.test", want, tc.requirePublic)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Routable != tc.wantRoute {
				t.Fatalf("Routable: got %v want %v (reason=%q)", got.Routable, tc.wantRoute, got.Reason)
			}
			for _, w := range tc.wantReason {
				if !strings.Contains(got.Reason, w) {
					t.Fatalf("Reason: got %q want substring %q", got.Reason, w)
				}
			}
			if got.Reason != "" && strings.Contains(got.Reason, "got 127.0.1.1") {
				t.Fatalf("Reason shows the /etc/hosts address as the DNS answer: %q", got.Reason)
			}
		})
	}
}

func TestHumanizePanelCertError(t *testing.T) {
	t.Parallel()
	name := "mail.panel.example.com"
	cases := []struct {
		raw        string
		wantHuman  bool
		wantSubstr string
	}{
		{raw: "acme: error: ... DNS problem: NXDOMAIN looking up A for mail.panel.example.com", wantHuman: true, wantSubstr: "not resolvable from public DNS"},
		{raw: "urn:ietf:params:acme:error:dns :: no valid A records found", wantHuman: true, wantSubstr: "delegate this domain's nameservers"},
		{raw: "too many certificates already issued (rate limit)", wantHuman: false},
		{raw: "Timeout during connect (likely firewall problem) port 80", wantHuman: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.raw[:min(24, len(tc.raw))], func(t *testing.T) {
			t.Parallel()
			got := HumanizePanelCertError(name, tc.raw)
			if tc.wantHuman {
				if got == tc.raw {
					t.Fatalf("expected humanized message, got raw: %q", got)
				}
				if !strings.Contains(got, tc.wantSubstr) {
					t.Fatalf("got %q, want substring %q", got, tc.wantSubstr)
				}
			} else if got != tc.raw {
				t.Fatalf("non-DNS error must pass through unchanged; got %q", got)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
