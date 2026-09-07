package repository

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// computeSSLState is pure (no r.db) — GH #246 regression: a Self-mode
// self_signed cert must read "self_signed", NOT fall through to "pending"
// (which the UI renders as "Issuing…").
func TestComputeSSLState(t *testing.T) {
	r := &domainRepo{}
	sp := func(s string) *string { return &s }
	dom := func(mode string, enabled bool) *models.Domain {
		return &models.Domain{SSLMode: mode, SSLEnabled: enabled}
	}
	le := &models.SSLCertificate{Status: models.SSLStatusIssued, CertPath: sp("/etc/letsencrypt/live/x/fullchain.pem")}
	selfStatus := &models.SSLCertificate{Status: models.SSLStatusSelfSigned, CertPath: sp("/etc/ssl/jabali-selfsigned/x/fullchain.pem")}

	cases := []struct {
		name string
		d    *models.Domain
		c    *models.SSLCertificate
		want string
	}{
		{"none mode", dom(models.SSLModeNone, true), selfStatus, "off"},
		{"ssl disabled", dom(models.SSLModeLE, false), le, "off"},
		{"self mode self_signed cert", dom(models.SSLModeSelf, true), selfStatus, "self_signed"},
		{"revoked leftover", dom(models.SSLModeLE, true), &models.SSLCertificate{Status: models.SSLStatusRevoked}, "off"},
		{"LE issued", dom(models.SSLModeLE, true), le, "active_le"},
		{"failed", dom(models.SSLModeLE, true), &models.SSLCertificate{Status: models.SSLStatusFailed}, "failed"},
		{"pending acme retry", dom(models.SSLModeLE, true), &models.SSLCertificate{Status: models.SSLStatusPendingACMERetry}, "pending"},
		{"no cert", dom(models.SSLModeLE, true), nil, "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.ComputeSSLState(tc.d, tc.c); got != tc.want {
				t.Errorf("computeSSLState = %q, want %q", got, tc.want)
			}
		})
	}
}
