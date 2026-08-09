package reconciler

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestRedirectHTTPSForCert covers GH #896/#887: a fresh LE/auto-mode domain on
// its self-signed bootstrap placeholder must NOT redirect to HTTPS (serve plain
// HTTP + ACME until a trusted cert lands), while every trusted-cert state — and
// the operator's deliberate self/custom/shared choices — keep redirecting.
func TestRedirectHTTPSForCert(t *testing.T) {
	const (
		selfSigned = "/etc/ssl/jabali-selfsigned/example.com/fullchain.pem"
		leCert     = "/etc/letsencrypt/live/example.com/fullchain.pem"
		sharedCert = "/etc/jabali/ssl/shared/01ABC/fullchain.pem"
		customCert = "/etc/nginx/ssl/operator.example.com.pem"
	)
	cases := []struct {
		name     string
		mode     string
		certPath string
		want     bool
	}{
		// The fix: fresh LE bootstrap placeholder → HTTP only.
		{"le mode self-signed bootstrap", models.SSLModeLE, selfSigned, false},
		{"legacy-empty mode self-signed bootstrap", "", selfSigned, false},
		// A pending_acme_retry that dropped to a self-signed fallback is the
		// same served-cert as the bootstrap: HTTP only (path is authoritative).
		{"le mode acme-retry self-signed fallback", models.SSLModeLE, selfSigned, false},

		// Trusted certs always redirect.
		{"le mode issued LE cert", models.SSLModeLE, leCert, true},
		{"legacy-empty mode issued LE cert", "", leCert, true},
		// Renewal-in-progress still has the valid LE cert on disk → keep redirecting.
		{"le mode renewing with valid LE cert on disk", models.SSLModeLE, leCert, true},

		// Operator-chosen modes keep serving HTTPS even from the self-signed dir.
		{"self mode self-signed is the chosen cert", models.SSLModeSelf, selfSigned, true},
		{"custom mode operator cert", models.SSLModeCustom, customCert, true},
		// A shared LE wildcard is trusted.
		{"shared mode wildcard cert", models.SSLModeShared, sharedCert, true},

		// No cert on disk: no :443, redirect is meaningless — :80 serves the docroot.
		{"le mode no cert", models.SSLModeLE, "", false},
		{"self mode no cert", models.SSLModeSelf, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redirectHTTPSForCert(tc.mode, tc.certPath); got != tc.want {
				t.Errorf("redirectHTTPSForCert(%q, %q) = %v, want %v", tc.mode, tc.certPath, got, tc.want)
			}
		})
	}
}
