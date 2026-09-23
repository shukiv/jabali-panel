package models

import "testing"

// TestSSLModeProtectedRefusal locks the shared protected-domain TLS invariant
// (JAB-318 AC3): a switch to `none` is refused for the panel-primary hostname
// and for a mail-enabled domain, panel-primary takes precedence, and every other
// mode (or a non-protected domain) is allowed. The REST PATCH, REST SSL-disable,
// and CLI `domain set` doors all route through this leaf, so its behaviour is the
// single contract they share.
func TestSSLModeProtectedRefusal(t *testing.T) {
	panelPrimary := &Domain{IsPanelPrimary: true}
	mailOn := &Domain{EmailEnabled: true}
	both := &Domain{IsPanelPrimary: true, EmailEnabled: true}
	plain := &Domain{}

	cases := []struct {
		name        string
		d           *Domain
		mode        string
		wantCode    string
		wantDetail  string
		wantRefused bool
	}{
		{"panel-primary to none refused", panelPrimary, SSLModeNone, "ssl_none_panel_primary", "the panel hostname must keep TLS", true},
		{"mail-enabled to none refused", mailOn, SSLModeNone, "ssl_none_with_email", "disable mail before removing TLS", true},
		{"panel-primary precedence over mail", both, SSLModeNone, "ssl_none_panel_primary", "the panel hostname must keep TLS", true},
		{"plain domain to none allowed", plain, SSLModeNone, "", "", false},
		{"panel-primary to le allowed", panelPrimary, SSLModeLE, "", "", false},
		{"mail-enabled to self allowed", mailOn, SSLModeSelf, "", "", false},
		{"panel-primary to custom allowed here", panelPrimary, SSLModeCustom, "", "", false},
		{"nil domain allowed", nil, SSLModeNone, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, detail, refused := SSLModeProtectedRefusal(tc.d, tc.mode)
			if refused != tc.wantRefused || code != tc.wantCode || detail != tc.wantDetail {
				t.Fatalf("SSLModeProtectedRefusal(%v, %q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.d, tc.mode, code, detail, refused, tc.wantCode, tc.wantDetail, tc.wantRefused)
			}
		})
	}
}
