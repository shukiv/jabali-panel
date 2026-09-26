package mailhostops

import (
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-390: a switchover needs a Let's Encrypt panel, an admin email for
// ACME, and a panel-primary mail vhost that is built — that vhost is the only
// one that answers the custom name, and Bulwark's JMAP URL moves to it. The
// setter refuses a request and the engine fails an attempt with the same
// reason, so the two cannot drift.

func readyInputs() (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
	return &models.ServerSettings{Hostname: "mx.example.com", AdminEmail: "admin@example.com", WebmailEnabled: true},
		&models.PanelCertificate{Kind: models.PanelCertKindHostname, UseLE: true},
		&models.Domain{Name: "mx.example.com", IsPanelPrimary: true, EmailEnabled: true, WebmailEnabled: true}
}

type readyMutation func(*models.ServerSettings, *models.PanelCertificate, *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain)

func TestReady(t *testing.T) {
	cases := []struct {
		name   string
		mutate readyMutation
		want   error
	}{
		{"ready", nil, nil},
		{"no settings", func(_ *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			return nil, c, d
		}, ErrPanelIdentityMissing},
		{"no hostname", func(s *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			s.Hostname = ""
			return s, c, d
		}, ErrPanelIdentityMissing},
		{"no admin email", func(s *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			s.AdminEmail = ""
			return s, c, d
		}, ErrPanelIdentityMissing},
		{"self-signed panel", func(s *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			c.UseLE = false
			return s, c, d
		}, ErrLetsEncryptOff},
		{"no hostname cert row", func(s *models.ServerSettings, _ *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			return s, nil, d
		}, ErrLetsEncryptOff},
		{"no panel-primary domain", func(s *models.ServerSettings, c *models.PanelCertificate, _ *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			return s, c, nil
		}, ErrPanelMailOff},
		{"domain not panel-primary", func(s *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			d.IsPanelPrimary = false
			return s, c, d
		}, ErrPanelMailOff},
		{"panel-primary is another name", func(s *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			d.Name = "old.example.com"
			return s, c, d
		}, ErrPanelMailOff},
		{"email off on panel-primary", func(s *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			d.EmailEnabled = false
			return s, c, d
		}, ErrPanelMailOff},
		{"webmail off on panel-primary", func(s *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			d.WebmailEnabled = false
			return s, c, d
		}, ErrPanelMailOff},
		{"webmail off server-wide", func(s *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			s.WebmailEnabled = false
			return s, c, d
		}, ErrPanelMailOff},
		{"panel-primary name case-insensitive", func(s *models.ServerSettings, c *models.PanelCertificate, d *models.Domain) (*models.ServerSettings, *models.PanelCertificate, *models.Domain) {
			d.Name = "MX.Example.COM"
			return s, c, d
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, c, d := readyInputs()
			if tc.mutate != nil {
				s, c, d = tc.mutate(s, c, d)
			}
			err := Ready(s, c, d)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("Ready = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Ready = %v, want %v", err, tc.want)
			}
		})
	}
}
