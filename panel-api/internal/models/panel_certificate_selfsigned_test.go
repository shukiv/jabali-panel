package models

import (
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/panelcert"
)

// GH #2166: the hostname row in self-signed mode follows the cert on disk.
func TestSelfSignedState(t *testing.T) {
	exp := time.Date(2026, 12, 25, 21, 45, 53, 0, time.UTC)
	other := exp.Add(-24 * time.Hour)
	retry := time.Now()
	upToDate := panelcert.Verdict{Action: panelcert.UpToDate}
	keepCA := panelcert.Verdict{Action: panelcert.KeepCA, NotAfter: exp}
	regen := panelcert.Verdict{Action: panelcert.Regenerate}

	for _, tc := range []struct {
		name        string
		row         PanelCertificate
		v           panelcert.Verdict
		wantStatus  string
		wantExpiry  bool
		wantChanged bool
	}{
		{name: "self-signed and clean", row: PanelCertificate{Status: PanelCertStatusSelfSigned}, v: upToDate, wantStatus: PanelCertStatusSelfSigned},
		{name: "a stale error", row: PanelCertificate{Status: PanelCertStatusSelfSigned, LastError: "not routable"}, v: upToDate, wantStatus: PanelCertStatusSelfSigned, wantChanged: true},
		{name: "a stale retry", row: PanelCertificate{Status: PanelCertStatusSelfSigned, NextRetryAt: &retry}, v: upToDate, wantStatus: PanelCertStatusSelfSigned, wantChanged: true},
		{name: "stale attempts", row: PanelCertificate{Status: PanelCertStatusSelfSigned, AttemptCount: 2}, v: upToDate, wantStatus: PanelCertStatusSelfSigned, wantChanged: true},
		{name: "a pending retry", row: PanelCertificate{Status: PanelCertStatusPendingACMERetry}, v: upToDate, wantStatus: PanelCertStatusSelfSigned, wantChanged: true},
		{name: "issued, but the cert is being regenerated", row: PanelCertificate{Status: PanelCertStatusIssued, ExpiresAt: &exp}, v: regen, wantStatus: PanelCertStatusSelfSigned, wantChanged: true},
		{name: "a kept CA cert already shown", row: PanelCertificate{Status: PanelCertStatusIssued, ExpiresAt: &exp}, v: keepCA, wantStatus: PanelCertStatusIssued, wantExpiry: true},
		{name: "a kept CA cert with another expiry", row: PanelCertificate{Status: PanelCertStatusIssued, ExpiresAt: &other}, v: keepCA, wantStatus: PanelCertStatusIssued, wantExpiry: true, wantChanged: true},
		{name: "a kept CA cert with no expiry", row: PanelCertificate{Status: PanelCertStatusIssued}, v: keepCA, wantStatus: PanelCertStatusIssued, wantExpiry: true, wantChanged: true},
		{name: "a kept CA cert shown as self-signed", row: PanelCertificate{Status: PanelCertStatusSelfSigned, ExpiresAt: &exp}, v: keepCA, wantStatus: PanelCertStatusIssued, wantExpiry: true, wantChanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, expiresAt, changed := SelfSignedState(&tc.row, tc.v)
			if status != tc.wantStatus || changed != tc.wantChanged {
				t.Errorf("got status %q changed %v, want %q changed %v", status, changed, tc.wantStatus, tc.wantChanged)
			}
			if tc.wantExpiry != (expiresAt != nil) || (expiresAt != nil && !expiresAt.Equal(exp)) {
				t.Errorf("expires_at = %v, want expiry %v", expiresAt, tc.wantExpiry)
			}
		})
	}
}
