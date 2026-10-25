package reconciler

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// On a free jabalihosted.com hostname the preview base is the hostname, a
// foreign zone. The hostname service publishes *.<label> and brokers the
// DNS-01 challenge, so the wildcard certificate row is requested anyway (it
// used to be skipped like any foreign base, leaving previews without TLS),
// and no local records are written.
func TestReconcilePreviewInfra_FreeHostnameRequestsCert(t *testing.T) {
	for _, tc := range []struct {
		name string
		zone *models.DNSZone
	}{
		{"no local zone", nil},
		// A DNS-module box has a zone row for its own hostname; nobody
		// queries it, so it must neither block the cert nor get records.
		{"stale local zone", &models.DNSZone{ID: "Z9", Name: "203-0-113-7.jabalihosted.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certs := &fakePreviewCertRepo{}
			recs := &fakePreviewRecordRepo{}
			r := previewReconciler(certs, &fakePreviewZoneRepo{zone: tc.zone}, recs)
			r.serverSettings = &fakeSettingsRepo{srv: &models.ServerSettings{
				Hostname: "203-0-113-7.jabalihosted.com", AdminEmail: "a@example.com", PublicIPv4: "203.0.113.7",
			}}
			ag := &fakePreviewAgent{}
			r.agent = ag

			r.reconcilePreviewInfra(context.Background(), enabledPreviewDomains())

			if len(certs.created) != 1 || certs.created[0].Name != "*.203-0-113-7.jabalihosted.com" {
				t.Fatalf("want one *.203-0-113-7.jabalihosted.com cert row, got %+v", certs.created)
			}
			c := certs.created[0]
			if c.Source != models.SharedCertSourceACME || c.UserID != nil {
				t.Errorf("cert row must be server-wide source=acme: %+v", c)
			}
			if got := c.ACMELineageName(); got != "wildcard.203-0-113-7.jabalihosted.com" {
				t.Errorf("lineage = %q — must not be the panel cert's 203-0-113-7.jabalihosted.com", got)
			}
			if len(recs.created) != 0 {
				t.Errorf("free hostname must not get local DNS records: %+v", recs.created)
			}
			if len(ag.calls) != 1 || ag.calls[0]["base"] != "203-0-113-7.jabalihosted.com" {
				t.Errorf("fallback vhost: %+v", ag.calls)
			}
		})
	}
}
