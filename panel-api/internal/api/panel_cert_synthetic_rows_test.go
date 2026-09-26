package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// listOnlyPanelCerts is a PanelCertificateRepository that serves ListAll
// from a fixed slice. Any other method panics through the nil embedded
// interface, so a test that reaches one fails loudly.
type listOnlyPanelCerts struct {
	repository.PanelCertificateRepository
	rows []*models.PanelCertificate
}

func (l listOnlyPanelCerts) ListAll(context.Context) ([]*models.PanelCertificate, error) {
	return l.rows, nil
}

func syntheticMailRow(t *testing.T, cfg SSLHandlerConfig) repository.SSLCertificateWithDomain {
	t.Helper()
	for _, row := range panelCertSyntheticRows(context.Background(), cfg) {
		if row.ID == "panel-cert:"+models.PanelCertKindMail {
			return row
		}
	}
	t.Fatal("no synthetic panel mail cert row")
	return repository.SSLCertificateWithDomain{}
}

// TestPanelCertSyntheticRows_MailRowWithoutHostnameUsesAppliedMailHostname
// (JAB-390): when the mail cert row carries no hostname, the SSL display
// label falls back to the applied mail hostname rather than a fresh
// mail.<primary> derivation.
func TestPanelCertSyntheticRows_MailRowWithoutHostnameUsesAppliedMailHostname(t *testing.T) {
	t.Parallel()

	applied := "mx.example.net"
	cfg := SSLHandlerConfig{
		Domains:        panelPrimaryRepo("panel.example.com"),
		PanelCerts:     listOnlyPanelCerts{rows: []*models.PanelCertificate{{Kind: models.PanelCertKindMail}}},
		ServerSettings: &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "panel.example.com", MailHostname: &applied}},
	}

	assert.Equal(t, "mx.example.net", syntheticMailRow(t, cfg).DomainName)
}

// TestPanelCertSyntheticRows_MailRowFallbackWithoutAppliedIsDerived: no
// applied mail hostname (no settings row, or no settings repo wired) keeps
// today's mail.<primary> label.
func TestPanelCertSyntheticRows_MailRowFallbackWithoutAppliedIsDerived(t *testing.T) {
	t.Parallel()

	for name, settings := range map[string]repository.ServerSettingsRepository{
		"no settings row":  &fakeSettingsRepo{},
		"no settings repo": nil,
		"read error":       errSettingsRepo{},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := SSLHandlerConfig{
				Domains:        panelPrimaryRepo("panel.example.com"),
				PanelCerts:     listOnlyPanelCerts{rows: []*models.PanelCertificate{{Kind: models.PanelCertKindMail}}},
				ServerSettings: settings,
			}
			assert.Equal(t, "mail.panel.example.com", syntheticMailRow(t, cfg).DomainName)
		})
	}
}

// TestPanelCertSyntheticRows_StoredMailHostnameWins: the hostname stored on
// the mail cert row is what the cert was pursued for, so it stays the label
// even when an applied mail hostname is set (JAB-389 pinning).
func TestPanelCertSyntheticRows_StoredMailHostnameWins(t *testing.T) {
	t.Parallel()

	applied := "mx.example.net"
	cfg := SSLHandlerConfig{
		Domains: panelPrimaryRepo("panel.example.com"),
		PanelCerts: listOnlyPanelCerts{rows: []*models.PanelCertificate{
			{Kind: models.PanelCertKindHostname, Hostname: "panel.example.com"},
			{Kind: models.PanelCertKindMail, Hostname: "mail.old.example.com"},
		}},
		ServerSettings: &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "panel.example.com", MailHostname: &applied}},
	}

	rows := panelCertSyntheticRows(context.Background(), cfg)
	require.Len(t, rows, 2)
	assert.Equal(t, "mail.old.example.com", syntheticMailRow(t, cfg).DomainName)
}
