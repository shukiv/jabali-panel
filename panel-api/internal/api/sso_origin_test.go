package api

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func originCtx(host, origin, referer string) *gin.Context {
	req := httptest.NewRequest("POST", "/admin/databases/sso/phpmyadmin", nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

// TestSameOriginStrict pins the JAB-304 policy shared by every browser DB-console
// SSO mint (tenant phpMyAdmin, tenant Adminer, privileged admin consoles). The
// privileged path previously used strings.Contains + accepted absent headers,
// which the substring/missing-header rows below now prove closed.
func TestSameOriginStrict(t *testing.T) {
	const host = "panel.example.com"
	cases := []struct {
		name    string
		host    string
		origin  string
		referer string
		want    bool
	}{
		{"origin exact match", host, "https://panel.example.com", "", true},
		{"origin with visible port still matches (hostname-only)", host, "https://panel.example.com:8443", "", true},
		{"request host carries a port too", "panel.example.com:443", "https://panel.example.com", "", true},

		// Deceptive substring hosts — the strings.Contains bypass, now rejected.
		{"suffix-appended attacker host rejected", host, "https://panel.example.com.attacker.tld", "", false},
		{"prefix look-alike host rejected", host, "https://evilpanel.example.com", "", false},
		{"foreign origin rejected", host, "https://attacker.tld", "", false},

		// Missing headers — the absent-header allowance, now rejected.
		{"missing origin AND referer rejected", host, "", "", false},

		// Referer fallback when Origin is absent.
		{"referer fallback exact match", host, "", "https://panel.example.com/db/index.php", true},
		{"referer fallback foreign rejected", host, "", "https://attacker.tld/x", false},

		// Unparseable origin is a non-match, never a pass.
		{"garbage origin rejected", host, "://", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sameOriginStrict(originCtx(tc.host, tc.origin, tc.referer))
			if got != tc.want {
				t.Fatalf("sameOriginStrict(host=%q, origin=%q, referer=%q) = %v, want %v",
					tc.host, tc.origin, tc.referer, got, tc.want)
			}
		})
	}
}
