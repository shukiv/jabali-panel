package reconciler

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// davSeed returns the four default m6 DAV SRV rows a jabali-mail zone starts
// with (BuildEmailRecords shape), all pointing at mail.example.com.
func davSeed() []*models.DNSRecord {
	m6 := dnscompile.EmailRecordsManagedBy
	mk := func(id, name, content string) *models.DNSRecord {
		return &models.DNSRecord{ID: id, ZoneID: "z1", Name: name, Type: "SRV", Content: content, Managed: true, ManagedBy: &m6}
	}
	return []*models.DNSRecord{
		mk("cals", "_caldavs._tcp", "1 443 mail.example.com"),
		mk("cards", "_carddavs._tcp", "1 443 mail.example.com"),
		mk("cal", "_caldav._tcp", "1 80 mail.example.com"),
		mk("card", "_carddav._tcp", "1 80 mail.example.com"),
	}
}

func davRec(repo *fakeDNSRecordRepo, name string) *models.DNSRecord {
	for _, r := range repo.records {
		if r.Name == name && r.Type == "SRV" {
			return r
		}
	}
	return nil
}

// GH #1462: an override repoints the SECURE SRV at the external host and drops
// the insecure :80 row; CardDAV without an override stays on mail.<domain>.
func TestReconcileDAVOverride_RepointsCalDAV(t *testing.T) {
	r, repo := newMailReconciler(davSeed()...)
	zone := &models.DNSZone{ID: "z1", Name: "example.com"}
	dom := &models.Domain{ID: "d1", EmailEnabled: true, MailProvider: "jabali", CalDAVHost: "nextcloud.example.com"}

	r.reconcileDAVOverrideRecords(context.Background(), zone, dom, nil)

	if got := davRec(repo, "_caldavs._tcp"); got == nil || got.Content != "1 443 nextcloud.example.com" {
		t.Errorf("_caldavs._tcp = %v, want '1 443 nextcloud.example.com'", got)
	}
	if davRec(repo, "_caldav._tcp") != nil {
		t.Error("insecure _caldav._tcp must be deleted when an override is set")
	}
	// CardDAV untouched — still the default, both rows present.
	if got := davRec(repo, "_carddavs._tcp"); got == nil || got.Content != "1 443 mail.example.com" {
		t.Errorf("_carddavs._tcp should stay default, got %v", got)
	}
	if davRec(repo, "_carddav._tcp") == nil {
		t.Error("_carddav._tcp default must remain when carddav has no override")
	}
}

func TestReconcileDAVOverride_HostPort(t *testing.T) {
	r, repo := newMailReconciler(davSeed()...)
	zone := &models.DNSZone{ID: "z1", Name: "example.com"}
	dom := &models.Domain{ID: "d1", EmailEnabled: true, CardDAVHost: "dav.example.com:8443"}

	r.reconcileDAVOverrideRecords(context.Background(), zone, dom, nil)

	if got := davRec(repo, "_carddavs._tcp"); got == nil || got.Content != "1 8443 dav.example.com" {
		t.Errorf("_carddavs._tcp = %v, want '1 8443 dav.example.com'", got)
	}
}

// Clearing a previously-set override restores the mail.<domain> defaults,
// including re-creating the insecure :80 row the override had removed.
func TestReconcileDAVOverride_ClearRestoresDefault(t *testing.T) {
	m6 := dnscompile.EmailRecordsManagedBy
	// State after a prior override: secure repointed, insecure gone.
	seed := []*models.DNSRecord{
		{ID: "cals", ZoneID: "z1", Name: "_caldavs._tcp", Type: "SRV", Content: "1 443 nextcloud.example.com", Managed: true, ManagedBy: &m6},
	}
	r, repo := newMailReconciler(seed...)
	zone := &models.DNSZone{ID: "z1", Name: "example.com"}
	dom := &models.Domain{ID: "d1", EmailEnabled: true, CalDAVHost: ""} // cleared

	r.reconcileDAVOverrideRecords(context.Background(), zone, dom, nil)

	if got := davRec(repo, "_caldavs._tcp"); got == nil || got.Content != "1 443 mail.example.com" {
		t.Errorf("_caldavs._tcp = %v, want restored default", got)
	}
	if got := davRec(repo, "_caldav._tcp"); got == nil || got.Content != "1 80 mail.example.com" {
		t.Errorf("insecure _caldav._tcp = %v, want re-created default", got)
	}
}

// A non-jabali (external) mail domain has no m6 DAV rows — the converger must
// not synthesise any even if an override host is set.
func TestReconcileDAVOverride_SkipsNonJabali(t *testing.T) {
	r, repo := newMailReconciler()
	zone := &models.DNSZone{ID: "z1", Name: "example.com"}
	dom := &models.Domain{ID: "d1", EmailEnabled: true, MailProvider: "m365", CalDAVHost: "nextcloud.example.com"}

	r.reconcileDAVOverrideRecords(context.Background(), zone, dom, nil)

	if len(repo.records) != 0 {
		t.Errorf("no DAV rows should be created for a non-jabali domain, got %d", len(repo.records))
	}
}
