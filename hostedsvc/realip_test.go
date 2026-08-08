package hostedsvc

import (
	"net/http"
	"testing"
)

func req(remote string, hdr map[string]string) *http.Request {
	r, _ := http.NewRequest("POST", "/v1/claim", nil)
	r.RemoteAddr = remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// The proxy-trust rule IS the label-integrity invariant (blueprint trap 2):
// a forged header from a direct external connection must never override the
// TCP source, or callers choose arbitrary labels.
func TestClientIP_TrustBoundary(t *testing.T) {
	// Loopback peer (nginx) → X-Real-IP honored.
	if ip := ClientIP(req("127.0.0.1:9999", map[string]string{"X-Real-IP": "203.0.113.9"})); ip.String() != "203.0.113.9" {
		t.Errorf("loopback proxy: got %v", ip)
	}
	// External peer with forged X-Real-IP → header ignored, TCP source wins.
	if ip := ClientIP(req("198.51.100.20:4242", map[string]string{"X-Real-IP": "203.0.113.9"})); ip.String() != "198.51.100.20" {
		t.Errorf("forged X-Real-IP: got %v", ip)
	}
	// X-Forwarded-For is never consulted, even from loopback.
	if ip := ClientIP(req("198.51.100.20:4242", map[string]string{"X-Forwarded-For": "203.0.113.9"})); ip.String() != "198.51.100.20" {
		t.Errorf("forged XFF: got %v", ip)
	}
	// No headers → TCP source.
	if ip := ClientIP(req("198.51.100.20:4242", nil)); ip.String() != "198.51.100.20" {
		t.Errorf("bare: got %v", ip)
	}
}
