package backupmetadata

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// counts tallies how many times each BATCH read was issued. JAB-374's core
// assertion: these stay constant no matter how many domains/mailboxes/etc. the
// account has (bounded by resource TYPE, not row count).
type counts struct {
	grants, certs, mbx, auto, shares, fwd, dnssec, zones, records, ports int
}

// --- fakes: each implements the batch read + Fatals its single-id sibling so a
// regression back to per-item reads fails loudly. ---

type fDatabases struct {
	repository.DatabaseRepository
	rows []models.Database
}

func (f *fDatabases) ListByUserID(context.Context, string, repository.ListOptions) ([]models.Database, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

type fDBUsers struct {
	repository.DatabaseUserRepository
	rows []models.DatabaseUser
}

func (f *fDBUsers) ListByUserID(context.Context, string, repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

type fGrants struct {
	repository.DatabaseUserGrantRepository
	t    *testing.T
	c    *counts
	rows []models.DatabaseUserGrant
}

func (f *fGrants) ListByDatabaseUserIDs(context.Context, []string) ([]models.DatabaseUserGrant, error) {
	f.c.grants++
	return f.rows, nil
}
func (f *fGrants) ListByDatabaseUserID(context.Context, string) ([]models.DatabaseUserGrant, error) {
	f.t.Fatal("Build must batch grants via ListByDatabaseUserIDs (JAB-374)")
	return nil, nil
}

type fDomains struct {
	repository.DomainRepository
	rows []models.Domain
}

func (f *fDomains) ListByUserID(context.Context, string, repository.ListOptions) ([]models.Domain, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

type fCerts struct {
	repository.SSLCertificateRepository
	t    *testing.T
	c    *counts
	rows []models.SSLCertificate
}

func (f *fCerts) FindByDomainIDs(context.Context, []string) ([]models.SSLCertificate, error) {
	f.c.certs++
	return f.rows, nil
}
func (f *fCerts) FindByDomainID(context.Context, string) (*models.SSLCertificate, error) {
	f.t.Fatal("Build must batch certs via FindByDomainIDs (JAB-374)")
	return nil, nil
}

type fMailboxes struct {
	repository.MailboxRepository
	t    *testing.T
	c    *counts
	rows []models.Mailbox
}

func (f *fMailboxes) ListByDomainIDs(context.Context, []string) ([]models.Mailbox, error) {
	f.c.mbx++
	return f.rows, nil
}
func (f *fMailboxes) ListByDomainID(context.Context, string, repository.ListOptions) ([]models.Mailbox, int64, error) {
	f.t.Fatal("Build must batch mailboxes via ListByDomainIDs (JAB-374)")
	return nil, 0, nil
}

type fAuto struct {
	repository.EmailAutoresponderRepository
	t    *testing.T
	c    *counts
	rows []models.EmailAutoresponder
}

func (f *fAuto) ListByMailboxIDs(context.Context, []string) ([]models.EmailAutoresponder, error) {
	f.c.auto++
	return f.rows, nil
}
func (f *fAuto) FindByMailboxID(context.Context, string) (*models.EmailAutoresponder, error) {
	f.t.Fatal("Build must batch autoresponders via ListByMailboxIDs (JAB-374)")
	return nil, nil
}

type fShares struct {
	repository.MailboxShareRepository
	t    *testing.T
	c    *counts
	rows []models.MailboxShare
}

func (f *fShares) ListByUserID(context.Context, string, repository.ListOptions) ([]models.MailboxShare, int64, error) {
	f.c.shares++
	return f.rows, int64(len(f.rows)), nil
}
func (f *fShares) FindByOwnerID(context.Context, string, repository.ListOptions) ([]models.MailboxShare, int64, error) {
	f.t.Fatal("Build must batch shares via ListByUserID (JAB-374)")
	return nil, 0, nil
}

type fFwd struct {
	repository.EmailForwarderRepository
	t    *testing.T
	c    *counts
	rows []models.EmailForwarder
}

func (f *fFwd) ListByDomainIDs(context.Context, []string) ([]models.EmailForwarder, error) {
	f.c.fwd++
	return f.rows, nil
}
func (f *fFwd) ListByDomainID(context.Context, string, repository.ListOptions) ([]models.EmailForwarder, int64, error) {
	f.t.Fatal("Build must batch forwarders via ListByDomainIDs (JAB-374)")
	return nil, 0, nil
}

type fDNSSEC struct {
	repository.DNSSECKeyRepository
	t    *testing.T
	c    *counts
	rows []models.DomainDNSSECKey
}

func (f *fDNSSEC) ListByDomainIDs(context.Context, []string) ([]models.DomainDNSSECKey, error) {
	f.c.dnssec++
	return f.rows, nil
}
func (f *fDNSSEC) ListByDomainID(context.Context, string) ([]models.DomainDNSSECKey, error) {
	f.t.Fatal("Build must batch dnssec keys via ListByDomainIDs (JAB-374)")
	return nil, nil
}

type fZones struct {
	repository.DNSZoneRepository
	t    *testing.T
	c    *counts
	rows []models.DNSZone
}

func (f *fZones) FindByDomainIDs(context.Context, []string) ([]models.DNSZone, error) {
	f.c.zones++
	return f.rows, nil
}
func (f *fZones) FindByDomainID(context.Context, string) (*models.DNSZone, error) {
	f.t.Fatal("Build must batch zones via FindByDomainIDs (JAB-374)")
	return nil, nil
}

type fRecords struct {
	repository.DNSRecordRepository
	t    *testing.T
	c    *counts
	rows []models.DNSRecord
}

func (f *fRecords) ListByZoneIDs(context.Context, []string) ([]models.DNSRecord, error) {
	f.c.records++
	return f.rows, nil
}
func (f *fRecords) ListByZoneID(context.Context, string) ([]models.DNSRecord, error) {
	f.t.Fatal("Build must batch records via ListByZoneIDs (JAB-374)")
	return nil, nil
}

type fDocker struct {
	repository.DockerAppRepository
	t     *testing.T
	c     *counts
	apps  []*models.DockerApp
	ports []*models.DockerAppPublishedPort
}

func (f *fDocker) ListByUserID(context.Context, string) ([]*models.DockerApp, error) {
	return f.apps, nil
}
func (f *fDocker) ListPortsForApps(context.Context, []string) ([]*models.DockerAppPublishedPort, error) {
	f.c.ports++
	return f.ports, nil
}
func (f *fDocker) ListPortsForApp(context.Context, string) ([]*models.DockerAppPublishedPort, error) {
	f.t.Fatal("Build must batch docker ports via ListPortsForApps (JAB-374)")
	return nil, nil
}

// fixtureDeps builds a Deps whose fakes carry `nDomains` domains (2 mailboxes
// each) plus fixed db/docker rows, so the same call graph runs at any scale.
func fixtureDeps(t *testing.T, c *counts, nDomains int) (Deps, *models.User) {
	user := &models.User{ID: "u1", Email: "u1@example.com"}

	var domains []models.Domain
	var certs []models.SSLCertificate
	var mbx []models.Mailbox
	var autos []models.EmailAutoresponder
	var shares []models.MailboxShare
	var fwds []models.EmailForwarder
	var keys []models.DomainDNSSECKey
	var zones []models.DNSZone
	var recs []models.DNSRecord
	for i := 0; i < nDomains; i++ {
		did := "d" + itoa(i)
		domains = append(domains, models.Domain{ID: did, Name: did + ".com"})
		certs = append(certs, models.SSLCertificate{ID: "c" + itoa(i), DomainID: did, Status: "issued"})
		m1, m2 := "m"+itoa(i)+"a", "m"+itoa(i)+"b"
		mbx = append(mbx,
			models.Mailbox{ID: m1, DomainID: did, LocalPart: "info", EmailCached: "info@" + did},
			models.Mailbox{ID: m2, DomainID: did, LocalPart: "sales", EmailCached: "sales@" + did})
		autos = append(autos, models.EmailAutoresponder{MailboxID: m1, Enabled: true})
		shares = append(shares, models.MailboxShare{ID: "sh" + itoa(i), OwnerMailboxID: m1, SharedWithMailboxID: m2})
		fwds = append(fwds, models.EmailForwarder{ID: "f" + itoa(i), DomainID: did, Type: "alias"})
		keys = append(keys, models.DomainDNSSECKey{DomainID: did, KeyTag: 1, KeyType: "ksk"})
		zid := "z" + itoa(i)
		zones = append(zones, models.DNSZone{ID: zid, DomainID: did})
		recs = append(recs,
			models.DNSRecord{ZoneID: zid, Managed: false, Name: "www", Type: "A", Content: "1.2.3.4"},
			models.DNSRecord{ZoneID: zid, Managed: true, Name: did, Type: "MX", Content: "mail." + did})
	}

	return Deps{
		Databases:      &fDatabases{rows: []models.Database{{ID: "db1", Name: "appdb"}}},
		DatabaseUsers:  &fDBUsers{rows: []models.DatabaseUser{{ID: "du1"}, {ID: "du2"}}},
		DatabaseGrants: &fGrants{t: t, c: c, rows: []models.DatabaseUserGrant{
			{ID: "g1", DatabaseUserID: "du1", DatabaseID: "db1"},
			{ID: "g2", DatabaseUserID: "du1", DatabaseID: "db1"},
			{ID: "g3", DatabaseUserID: "du2", DatabaseID: "db1"},
		}},
		Domains:        &fDomains{rows: domains},
		SSLCerts:       &fCerts{t: t, c: c, rows: certs},
		Mailboxes:      &fMailboxes{t: t, c: c, rows: mbx},
		Autoresponders: &fAuto{t: t, c: c, rows: autos},
		MailboxShares:  &fShares{t: t, c: c, rows: shares},
		Forwarders:     &fFwd{t: t, c: c, rows: fwds},
		DNSSECKeys:     &fDNSSEC{t: t, c: c, rows: keys},
		DNSZones:       &fZones{t: t, c: c, rows: zones},
		DNSRecords:     &fRecords{t: t, c: c, rows: recs},
		DockerApps: &fDocker{t: t, c: c, apps: []*models.DockerApp{{ID: "a1", Slug: "gitea"}},
			ports: []*models.DockerAppPublishedPort{{ID: "p1", AppID: "a1", PortName: "http"}}},
	}, user
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestBuild_BatchGroupingCorrect: rows land on the right parent, managed DNS
// records are excluded, grants group per db-user — and NONE of the single-id
// reads fire (the fakes Fatal if they do).
func TestBuild_BatchGroupingCorrect(t *testing.T) {
	var c counts
	deps, user := fixtureDeps(t, &c, 2)
	m := Build(context.Background(), user, deps)

	if len(m.Domains) != 2 {
		t.Fatalf("want 2 domains, got %d", len(m.Domains))
	}
	d0 := m.Domains[0]
	if d0.SSLCertificate == nil || d0.SSLCertificate.ID != "c0" {
		t.Errorf("domain0 cert not grouped: %+v", d0.SSLCertificate)
	}
	if len(d0.Mailboxes) != 2 {
		t.Fatalf("domain0 want 2 mailboxes, got %d", len(d0.Mailboxes))
	}
	if d0.Mailboxes[0].Autoresponder == nil {
		t.Error("mailbox m0a should carry its autoresponder")
	}
	if d0.Mailboxes[1].Autoresponder != nil {
		t.Error("mailbox m0b has no autoresponder — must be nil")
	}
	if len(d0.Mailboxes[0].SharedWith) != 1 {
		t.Errorf("mailbox m0a want 1 share, got %d", len(d0.Mailboxes[0].SharedWith))
	}
	if len(d0.Forwarders) != 1 || len(d0.DNSSECKeys) != 1 {
		t.Errorf("domain0 fwd/dnssec grouping wrong: fwd=%d dnssec=%d", len(d0.Forwarders), len(d0.DNSSECKeys))
	}
	if len(d0.DNSRecords) != 1 || d0.DNSRecords[0].Name != "www" {
		t.Errorf("domain0 must carry only the 1 non-managed record, got %d: %+v", len(d0.DNSRecords), d0.DNSRecords)
	}
	// Grants grouped per db-user.
	byUser := map[string]int{}
	for _, du := range m.DatabaseUsers {
		byUser[du.ID] = len(du.Grants)
	}
	if byUser["du1"] != 2 || byUser["du2"] != 1 {
		t.Errorf("grants not grouped per db-user: du1=%d du2=%d", byUser["du1"], byUser["du2"])
	}
	if len(m.DockerApps) != 1 || len(m.DockerApps[0].Ports) != 1 {
		t.Errorf("docker app ports not grouped: %+v", m.DockerApps)
	}
}

// TestBuild_QueryBudget_BoundedAndScaleInvariant: every batch read fires
// exactly once, the total is within an explicit budget, and it does NOT grow
// from 1 domain to 100 (JAB-374 AC: query count bounded by resource type).
func TestBuild_QueryBudget_BoundedAndScaleInvariant(t *testing.T) {
	const budget = 10 // grants,certs,mbx,auto,shares,fwd,dnssec,zones,records,ports

	var c1 counts
	deps1, u := fixtureDeps(t, &c1, 1)
	_ = Build(context.Background(), u, deps1)
	got1 := c1.grants + c1.certs + c1.mbx + c1.auto + c1.shares + c1.fwd + c1.dnssec + c1.zones + c1.records + c1.ports

	var c100 counts
	deps100, u2 := fixtureDeps(t, &c100, 100)
	_ = Build(context.Background(), u2, deps100)
	got100 := c100.grants + c100.certs + c100.mbx + c100.auto + c100.shares + c100.fwd + c100.dnssec + c100.zones + c100.records + c100.ports

	if got1 > budget {
		t.Fatalf("batch query count %d exceeds budget %d", got1, budget)
	}
	if got1 != got100 {
		t.Fatalf("query count grew with resource count: 1-domain=%d, 100-domain=%d (must be identical)", got1, got100)
	}
	for name, n := range map[string]int{
		"grants": c1.grants, "certs": c1.certs, "mbx": c1.mbx, "auto": c1.auto,
		"shares": c1.shares, "fwd": c1.fwd, "dnssec": c1.dnssec, "zones": c1.zones,
		"records": c1.records, "ports": c1.ports,
	} {
		if n != 1 {
			t.Errorf("batch read %q fired %d times, want exactly 1", name, n)
		}
	}
}

// TestBuild_NilAssociationRepo: a nil association repo skips its section, no
// panic — a missed nil-guard would be a 04:30 scheduler crash.
func TestBuild_NilAssociationRepo(t *testing.T) {
	var c counts
	deps, user := fixtureDeps(t, &c, 1)
	deps.Autoresponders = nil // caller didn't wire it
	deps.DNSSECKeys = nil
	m := Build(context.Background(), user, deps)
	if len(m.Domains) != 1 {
		t.Fatalf("want 1 domain, got %d", len(m.Domains))
	}
	if m.Domains[0].Mailboxes[0].Autoresponder != nil {
		t.Error("nil Autoresponders repo → section must be omitted, not populated")
	}
	if len(m.Domains[0].DNSSECKeys) != 0 {
		t.Error("nil DNSSECKeys repo → section must be omitted")
	}
	if c.auto != 0 || c.dnssec != 0 {
		t.Error("nil repos must not be queried")
	}
}

// TestBuild_MissingParentSectionsEmpty: a domain with none of its optional
// associations must come back with those sections absent/empty — the
// grouping-correctness case where an off-by-one in map construction hides.
func TestBuild_MissingParentSectionsEmpty(t *testing.T) {
	var c counts
	user := &models.User{ID: "u1"}
	deps := Deps{
		Domains: &fDomains{rows: []models.Domain{{ID: "d0", Name: "d0.com"}, {ID: "d1", Name: "d1.com"}}},
		// Only d0 has a cert / zone / dnssec key; only m0 (on d0) has an autoresponder.
		SSLCerts:  &fCerts{t: t, c: &c, rows: []models.SSLCertificate{{ID: "c0", DomainID: "d0"}}},
		Mailboxes: &fMailboxes{t: t, c: &c, rows: []models.Mailbox{
			{ID: "m0", DomainID: "d0", LocalPart: "a"},
			{ID: "m1", DomainID: "d1", LocalPart: "b"},
		}},
		Autoresponders: &fAuto{t: t, c: &c, rows: []models.EmailAutoresponder{{MailboxID: "m0", Enabled: true}}},
		Forwarders:     &fFwd{t: t, c: &c, rows: []models.EmailForwarder{{ID: "f0", DomainID: "d0", Type: "alias"}}},
		DNSSECKeys:     &fDNSSEC{t: t, c: &c, rows: []models.DomainDNSSECKey{{DomainID: "d0", KeyTag: 1}}},
		DNSZones:       &fZones{t: t, c: &c, rows: []models.DNSZone{{ID: "z0", DomainID: "d0"}}},
		DNSRecords:     &fRecords{t: t, c: &c, rows: []models.DNSRecord{{ZoneID: "z0", Name: "www", Type: "A"}}},
	}
	m := Build(context.Background(), user, deps)
	if len(m.Domains) != 2 {
		t.Fatalf("want 2 domains, got %d", len(m.Domains))
	}
	// d0 populated.
	if m.Domains[0].SSLCertificate == nil || len(m.Domains[0].DNSSECKeys) != 1 ||
		len(m.Domains[0].DNSRecords) != 1 || len(m.Domains[0].Forwarders) != 1 ||
		m.Domains[0].Mailboxes[0].Autoresponder == nil {
		t.Fatalf("d0 should be fully populated: %+v", m.Domains[0])
	}
	// d1 bare — every optional section absent.
	d1 := m.Domains[1]
	if d1.SSLCertificate != nil {
		t.Error("d1 has no cert → SSLCertificate must be nil")
	}
	if len(d1.DNSSECKeys) != 0 || len(d1.DNSRecords) != 0 || len(d1.Forwarders) != 0 {
		t.Errorf("d1 optional sections must be empty: dnssec=%d records=%d fwd=%d", len(d1.DNSSECKeys), len(d1.DNSRecords), len(d1.Forwarders))
	}
	if len(d1.Mailboxes) != 1 || d1.Mailboxes[0].Autoresponder != nil {
		t.Errorf("d1 mailbox must have no autoresponder: %+v", d1.Mailboxes)
	}
}
