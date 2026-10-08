package backupmetadata

import (
	"context"
	"fmt"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a backup carries a domain's mail settings — its mail provider and
// the provider's DKIM tokens, its DMARC np and testing tags, its CalDAV and
// CardDAV hosts and its MTA-STS switch — and a restore brings them back.

// msSettings sets every mail setting on dm.
func msSettings(dm *internalbackup.MetadataDomain) {
	dm.MailProvider, dm.M365Onmicrosoft, dm.GoogleDKIM = models.MailProviderM365, odPtr("contoso.onmicrosoft.com"), odPtr("v=DKIM1; p=MIIB")
	dm.DmarcNP, dm.DmarcTesting = "reject", true
	dm.CalDAVHost, dm.CardDAVHost = "dav.example.net:8443", "card.example.net"
	dm.MTASTSEnabled = true
}

func msSettingsOf(d models.Domain) string {
	return fmt.Sprintf("provider=%s m365=%s google=%s np=%s testing=%v caldav=%s carddav=%s",
		d.MailProvider, odStr(d.M365Onmicrosoft), odStr(d.GoogleDKIM), d.DmarcNP, d.DmarcTesting, d.CalDAVHost, d.CardDAVHost)
}

const msWant = "provider=m365 m365=contoso.onmicrosoft.com google=v=DKIM1; p=MIIB np=reject testing=true caldav=dav.example.net:8443 carddav=card.example.net"

// msDomains records the MTA-STS switches Apply turns on.
type msDomains struct {
	*dcDomains
	mtasts []string
}

func (r *msDomains) UpdateMTASTSEnabled(_ context.Context, id string, on bool) (uint64, error) {
	r.mtasts = append(r.mtasts, fmt.Sprintf("%s=%v", id, on))
	return 1700000000, nil
}

// msApply restores meta, from this server's own backup, through a door that
// restores DNS records afterwards when restoresDNS.
func msApply(meta *internalbackup.AccountMetadata, restoresDNS bool) (*msDomains, ApplyResult) {
	dc, _, _, d := dcDeps()
	dom := &msDomains{dcDomains: dc}
	d.Domains = dom
	d.CheckDomain = func(context.Context, *models.Domain, string) ([]string, error) { return nil, nil }
	d.RestoresDNS = restoresDNS
	return dom, Apply(context.Background(), meta, d)
}

func TestBuild_CarriesDomainMailSettings(t *testing.T) {
	dom := models.Domain{ID: "d1", Name: "shop.org", DocRoot: "/home/alice/domains/shop.org/public_html"}
	dom.MailProvider, dom.M365Onmicrosoft, dom.GoogleDKIM = models.MailProviderM365, odPtr("contoso.onmicrosoft.com"), odPtr("v=DKIM1; p=MIIB")
	dom.DmarcNP, dom.DmarcTesting = "reject", true
	dom.CalDAVHost, dom.CardDAVHost = "dav.example.net:8443", "card.example.net"
	dom.MTASTSEnabled, dom.MTASTSId = true, 42
	m := Build(context.Background(), &models.User{ID: "u1"}, Deps{Domains: &fDomains{rows: []models.Domain{dom}}})
	if len(m.Domains) != 1 {
		t.Fatalf("backup has %d domains, want 1", len(m.Domains))
	}
	dm := m.Domains[0]
	got := msSettingsOf(models.Domain{MailProvider: dm.MailProvider, M365Onmicrosoft: dm.M365Onmicrosoft, GoogleDKIM: dm.GoogleDKIM,
		DmarcNP: dm.DmarcNP, DmarcTesting: dm.DmarcTesting, CalDAVHost: dm.CalDAVHost, CardDAVHost: dm.CardDAVHost})
	if got != msWant || !dm.MTASTSEnabled {
		t.Fatalf("backup:\n got  %s mta-sts=%v\n want %s mta-sts=true", got, dm.MTASTSEnabled, msWant)
	}
}

func TestApply_RestoredDomainTakesItsMailSettings(t *testing.T) {
	m := plpMeta(false)
	msSettings(&m.Domains[0])
	dom, r := msApply(m, true)
	if len(dom.created) != 1 {
		t.Fatalf("created %d (errors %v)", len(dom.created), r.Errors)
	}
	if got := msSettingsOf(dom.created[0]); got != msWant {
		t.Fatalf("restored:\n got  %s\n want %s", got, msWant)
	}
}

// A backup made before it carried the mail provider leaves the column to its
// default, as before.
func TestApply_RestoredDomainFromAnOlderBackupGetsTheDefaultMailProvider(t *testing.T) {
	dom, _ := msApply(plpMeta(false), true)
	if len(dom.created) != 1 || dom.created[0].MailProvider != "" {
		t.Fatalf("created %+v, want the provider left to the column's default", dom.created)
	}
}

// MTA-STS comes back on when the restore then publishes its DNS records.
func TestApply_RestoredDomainTurnsMTASTSOn(t *testing.T) {
	m := plpMeta(false)
	m.Domains[0].MTASTSEnabled = true
	dom, r := msApply(m, true)
	if len(dom.mtasts) != 1 || dom.mtasts[0] != "d-good=true" || hasError(r.Errors, "MTA-STS") {
		t.Fatalf("MTA-STS writes %v errors %v; want d-good turned on", dom.mtasts, r.Errors)
	}
}

// Without its DNS records a receiving server can't find the policy, so a
// restore that can't publish them leaves MTA-STS off and says why.
func TestApply_RestoredDomainLeavesMTASTSOffWithoutItsDNS(t *testing.T) {
	for _, tc := range []struct {
		name        string
		restoresDNS bool
		set         func(*internalbackup.MetadataDomain)
		want        string
	}{
		{"door restores no DNS", false, func(*internalbackup.MetadataDomain) {}, "MTA-STS not turned on: this restore doesn't restore DNS records"},
		{"DNS hosted elsewhere", true, func(dm *internalbackup.MetadataDomain) { dm.DNSDisabled = true }, "MTA-STS not turned on: DNS for this domain is hosted elsewhere"},
		{"ownership pending", true, func(dm *internalbackup.MetadataDomain) { dm.OwnershipStatus = models.OwnershipPending }, "MTA-STS not turned on: the domain's ownership isn't verified yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := plpMeta(false)
			m.Domains[0].MTASTSEnabled = true
			tc.set(&m.Domains[0])
			dom, r := msApply(m, tc.restoresDNS)
			if len(dom.created) != 1 || len(dom.mtasts) != 0 || !hasError(r.Errors, "domain d-good (good.org): "+tc.want) {
				t.Fatalf("created %d, MTA-STS writes %v, errors %v; want it left off with %q", len(dom.created), dom.mtasts, r.Errors, tc.want)
			}
		})
	}
}

// msOverwriteDomains records the MTA-STS switches the overwrite turns on.
type msOverwriteDomains struct{ *odDomains }

func (r msOverwriteDomains) UpdateMTASTSEnabled(_ context.Context, id string, on bool) (uint64, error) {
	d := r.rows[id]
	d.MTASTSEnabled = on
	r.rows[id] = d
	r.writes = append(r.writes, fmt.Sprintf("mta-sts %s=%v", id, on))
	return 1700000000, nil
}

func msOverwrite(t *testing.T, own func(*models.Domain), backup func(*internalbackup.MetadataDomain)) (*odFixture, ApplyResult) {
	t.Helper()
	f := odSetup("d-own", true, "")
	f.deps.Domains = msOverwriteDomains{f.domains}
	f.deps.RestoresDNS = true
	o := f.domains.rows["d-own"]
	o.MailProvider, o.M365Onmicrosoft = models.MailProviderJabali, odPtr("own.onmicrosoft.com")
	o.OwnershipStatus = models.OwnershipVerified
	own(&o)
	f.domains.rows["d-own"] = o
	m := odMeta("d-own")
	backup(&m.Domains[0])
	return f, Apply(context.Background(), m, f.deps)
}

// The overwrite gives an existing domain the backup's DMARC tags and DAV
// hosts, as its page sets them. Its mail provider and the provider's tokens
// stay: a switch republishes its mail DNS and certificate.
func TestApply_OverwriteDomainTakesTheBackupsMailSettings(t *testing.T) {
	f, r := msOverwrite(t, func(*models.Domain) {}, msSettings)
	want := "provider=jabali m365=own.onmicrosoft.com google=<nil> np=reject testing=true caldav=dav.example.net:8443 carddav=card.example.net"
	got := f.own("d-own")
	if s := msSettingsOf(got); s != want {
		t.Fatalf("domain:\n got  %s\n want %s\n(errors %v)", s, want, r.Errors)
	}
	// The same write takes the backup's on/off state: a second write from the
	// row as read would put it back.
	if got.IsEnabled {
		t.Fatalf("domain left enabled; want the backup's disabled state kept beside the mail settings")
	}
}

// A backup made before it carried the mail settings leaves the domain's own.
func TestApply_OverwriteFromAnOlderBackupKeepsTheDomainsMailSettings(t *testing.T) {
	f, r := msOverwrite(t, func(o *models.Domain) {
		o.DmarcNP, o.DmarcTesting, o.CalDAVHost, o.CardDAVHost = "quarantine", true, "dav.own.org", "card.own.org"
	}, func(*internalbackup.MetadataDomain) {})
	want := "provider=jabali m365=own.onmicrosoft.com google=<nil> np=quarantine testing=true caldav=dav.own.org carddav=card.own.org"
	if s := msSettingsOf(f.own("d-own")); s != want {
		t.Fatalf("domain:\n got  %s\n want %s\n(errors %v)", s, want, r.Errors)
	}
}

// The overwrite turns MTA-STS on as the backup has it, never off.
func TestApply_OverwriteDomainTurnsMTASTSOnNeverOff(t *testing.T) {
	f, r := msOverwrite(t, func(*models.Domain) {}, func(dm *internalbackup.MetadataDomain) { dm.MTASTSEnabled = true })
	if !f.own("d-own").MTASTSEnabled || !containsWrite(f.domains.writes, "mta-sts d-own=true") || len(f.scheduled) == 0 {
		t.Fatalf("MTA-STS %v writes %v scheduled %v errors %v; want it turned on and the domain scheduled", f.own("d-own").MTASTSEnabled, f.domains.writes, f.scheduled, r.Errors)
	}

	f, _ = msOverwrite(t, func(o *models.Domain) { o.MTASTSEnabled = true }, func(dm *internalbackup.MetadataDomain) { dm.MTASTSEnabled = false })
	if !f.own("d-own").MTASTSEnabled || containsWrite(f.domains.writes, "mta-sts") {
		t.Fatalf("MTA-STS %v writes %v; want it left on", f.own("d-own").MTASTSEnabled, f.domains.writes)
	}
}

func containsWrite(writes []string, prefix string) bool {
	for _, w := range writes {
		if len(w) >= len(prefix) && w[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
