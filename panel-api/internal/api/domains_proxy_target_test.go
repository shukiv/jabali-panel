package api

import "testing"

// TestValidateProxyPassTarget_InjectionHardening (GH #1624) pins that the
// admin-only proxy_pass target — rendered UNQUOTED into `proxy_pass <t>;` —
// cannot smuggle a second directive past the internal-address SSRF filter.
// url.Parse keeps the host valid while a `;`/space lives in the path, so the
// boundary rejects raw whitespace and `;`/`{`/`}` that a real target never
// carries (they would be percent-encoded).
func TestValidateProxyPassTarget_InjectionHardening(t *testing.T) {
	for _, tg := range []string{
		"http://ok.example/; proxy_pass http://169.254.169.254; #", // SSRF-filter bypass
		"http://ok.example/;return 204",                            // injected directive, no space
		"http://ok.example/a b",                                    // raw space
		"http://ok.example/{",                                      // block open
	} {
		if err := validateProxyPassTarget(tg); err == nil {
			t.Errorf("target %q must be rejected", tg)
		}
	}
	// Legitimate targets (non-internal host, no injection chars) still pass.
	for _, tg := range []string{
		"http://upstream.example:9000",
		"https://ok.example/api/v1",
		"http://198.51.100.20:8080", // TEST-NET-2: not loopback/private
	} {
		if err := validateProxyPassTarget(tg); err != nil {
			t.Errorf("target %q should be allowed, got %v", tg, err)
		}
	}
	// The internal-address filter still fires (no regression).
	if err := validateProxyPassTarget("http://127.0.0.1:9000"); err == nil {
		t.Errorf("loopback target must still be rejected")
	}
}
