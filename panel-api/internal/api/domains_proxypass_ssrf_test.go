package api

import "testing"

// JAB-65: proxy_pass target validation must block SSRF to internal services
// while still allowing legitimate external upstreams.
func TestValidateProxyPassTarget_SSRF(t *testing.T) {
	rejected := []string{
		"http://127.0.0.1:3306",                    // loopback v4
		"http://127.5.6.7",                         // loopback v4 (whole /8)
		"http://[::1]:6379",                        // loopback v6
		"http://localhost:8443",                    // localhost name
		"http://LocalHost",                         // case-insensitive
		"http://169.254.169.254/latest/meta",       // link-local (cloud metadata)
		"http://[fe80::1]",                         // link-local v6
		"http://10.0.0.5",                          // RFC1918
		"http://172.16.9.9",                        // RFC1918
		"http://192.168.1.1",                       // RFC1918
		"http://0.0.0.0",                           // unspecified
		"http://unix:/run/jabali-panel/api.sock:/", // nginx unix upstream
		"https://unix:/run/x.sock",
	}
	for _, tgt := range rejected {
		if err := validateProxyPassTarget(tgt); err == nil {
			t.Errorf("expected %q to be rejected, got nil", tgt)
		}
	}

	allowed := []string{
		"http://example.com",
		"https://api.stripe.com/v1",
		"http://93.184.216.34:8080",             // public literal IP
		"https://upstream.internal.example.com", // public hostname (not resolved)
	}
	for _, tgt := range allowed {
		if err := validateProxyPassTarget(tgt); err != nil {
			t.Errorf("expected %q to be allowed, got %v", tgt, err)
		}
	}
}
