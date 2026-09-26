// Package mailhostops owns the JAB-390 shared panel mail hostname switchover
// rules that both the setter (API, CLI) and the reconciler's switchover
// engine apply, so a request is refused and an attempt is failed for the
// same reasons.
package mailhostops

import (
	"errors"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Readiness failures. Each message is shown to the admin as is.
var (
	ErrPanelIdentityMissing = errors.New("set the panel hostname and admin email before changing the mail hostname")
	ErrLetsEncryptOff       = errors.New("the panel uses a self-signed certificate; turn on Let's Encrypt for the panel before changing the mail hostname")
	ErrPanelMailOff         = errors.New("enable email and webmail on the panel hostname's domain before changing the mail hostname")
)

// Ready reports whether the panel can move its mail hostname. It needs:
//
//   - the panel hostname and admin email (ACME registration);
//   - Let's Encrypt on for the panel (hostCert is the hostname kind row) —
//     the new name gets a publicly trusted certificate, and a self-signed
//     switchover is not supported;
//   - the panel-primary domain for the panel hostname with email and
//     webmail on, and webmail on server-wide. Its mail vhost is the only one
//     that answers a custom mail hostname, and Bulwark's JMAP URL moves to
//     that name, so without the vhost every webmail login would fail.
func Ready(s *models.ServerSettings, hostCert *models.PanelCertificate, panelPrimary *models.Domain) error {
	if s == nil || s.Hostname == "" || s.AdminEmail == "" {
		return ErrPanelIdentityMissing
	}
	if hostCert == nil || !hostCert.UseLE {
		return ErrLetsEncryptOff
	}
	if panelPrimary == nil || !panelPrimary.IsPanelPrimary ||
		!strings.EqualFold(panelPrimary.Name, s.Hostname) ||
		!panelPrimary.EmailEnabled || !panelPrimary.WebmailEnabled || !s.WebmailEnabled {
		return ErrPanelMailOff
	}
	return nil
}
