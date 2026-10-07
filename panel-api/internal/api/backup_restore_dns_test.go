package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: no restore brought back a domain's custom DNS records — Apply
// rebuilt the domain, the reconciler later made its zone with only the
// panel's own records, and the backup's records were dropped. The restore now
// adds them once the zone exists, through the same checks a record created in
// the panel goes through.

type rdDomains struct {
	repository.DomainRepository
	rows map[string]*models.Domain
}

func (r *rdDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if d, ok := r.rows[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

// rdZones answers FindByDomainID with ErrNotFound until the zone appears on
// the appearAt-th lookup of that domain (0 = there from the start).
type rdZones struct {
	repository.DNSZoneRepository
	zones    map[string]*models.DNSZone
	appearAt map[string]int
	lookups  map[string]int
}

func (r *rdZones) FindByDomainID(_ context.Context, domainID string) (*models.DNSZone, error) {
	r.lookups[domainID]++
	z, ok := r.zones[domainID]
	if !ok || r.lookups[domainID] < r.appearAt[domainID] {
		return nil, repository.ErrNotFound
	}
	return z, nil
}

type rdRecords struct {
	repository.DNSRecordRepository
	rows []models.DNSRecord
}

func (r *rdRecords) ListByZoneID(_ context.Context, zoneID string) ([]models.DNSRecord, error) {
	var out []models.DNSRecord
	for _, x := range r.rows {
		if x.ZoneID == zoneID {
			out = append(out, x)
		}
	}
	return out, nil
}
func (r *rdRecords) Create(_ context.Context, rec *models.DNSRecord) error {
	r.rows = append(r.rows, *rec)
	return nil
}

func (r *rdRecords) userRecords(zoneID string) []string {
	var out []string
	for _, x := range r.rows {
		if x.ZoneID == zoneID && !x.Managed {
			out = append(out, x.Name+" "+x.Type+" "+x.Content)
		}
	}
	return out
}

type rdSettings struct {
	repository.ServerSettingsRepository
	s *models.ServerSettings
}

func (r rdSettings) Get(context.Context) (*models.ServerSettings, error) { return r.s, nil }

type rdScheduler struct{ ids []string }

func (s *rdScheduler) Schedule(id string) { s.ids = append(s.ids, id) }

type rdFixture struct {
	domains *rdDomains
	zones   *rdZones
	records *rdRecords
	sched   *rdScheduler
	srv     *models.ServerSettings
}

// newRDFixture: alice (u1) owns example.com (d1), whose zone z1 holds this
// server's own managed records (apex A and www CNAME).
func newRDFixture() *rdFixture {
	f := &rdFixture{
		domains: &rdDomains{rows: map[string]*models.Domain{}},
		zones:   &rdZones{zones: map[string]*models.DNSZone{}, appearAt: map[string]int{}, lookups: map[string]int{}},
		records: &rdRecords{},
		sched:   &rdScheduler{},
		srv:     &models.ServerSettings{PublicIPv4: "203.0.113.10", DefaultDNSTTL: 300},
	}
	f.addDomain("d1", "example.com", "u1", "z1")
	return f
}

func (f *rdFixture) addDomain(id, name, owner, zoneID string) *models.Domain {
	d := &models.Domain{ID: id, Name: name, UserID: owner}
	d.OwnershipStatus = models.OwnershipVerified
	f.domains.rows[name] = d
	f.zones.zones[id] = &models.DNSZone{ID: zoneID, DomainID: id, Name: name, IsEnabled: true}
	f.records.rows = append(f.records.rows,
		models.DNSRecord{ID: zoneID + "-a", ZoneID: zoneID, Name: "@", Type: "A", Content: "203.0.113.10", Managed: true, IsEnabled: true},
		models.DNSRecord{ID: zoneID + "-www", ZoneID: zoneID, Name: "www", Type: "CNAME", Content: name + ".", Managed: true, IsEnabled: true})
	return d
}

func (f *rdFixture) run(t *testing.T, untrusted bool, recs ...internalbackup.MetadataDNSRecord) ([]string, []string) {
	t.Helper()
	meta := internalbackup.AccountMetadata{Domains: []internalbackup.MetadataDomain{{ID: "src-d1", Name: "example.com", DNSRecords: recs}}}
	raw, _ := json.Marshal(meta)
	return RestoreBundleDNS(context.Background(), RestoreDNSDeps{
		Domains: f.domains, Zones: f.zones, Records: f.records, Settings: rdSettings{s: f.srv}, Scheduler: f.sched, Untrusted: untrusted,
	}, raw, "u1")
}

func rec(name, typ, content string) internalbackup.MetadataDNSRecord {
	return internalbackup.MetadataDNSRecord{Name: name, Type: typ, Content: content, TTL: 3600, IsEnabled: true}
}

func fastRestoreDNSWait(t *testing.T, wait time.Duration) {
	t.Helper()
	prevWait, prevPoll := restoreDNSZoneWait, restoreDNSPoll
	restoreDNSZoneWait, restoreDNSPoll = wait, time.Millisecond
	t.Cleanup(func() { restoreDNSZoneWait, restoreDNSPoll = prevWait, prevPoll })
}

func TestRestoreBundleDNS_AddsTheBackupsRecordsOnceTheZoneExists(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	f := newRDFixture()
	f.zones.appearAt["d1"] = 3 // the reconciler makes the zone a little later
	applied, warnings := f.run(t, false,
		rec("shop", "CNAME", "shops.example.net."),
		rec("@", "TXT", "google-site-verification=abc"),
		rec("@", "MX", "aspmx.l.google.com."),
	)

	got := strings.Join(f.records.userRecords("z1"), "|")
	for _, want := range []string{"shop CNAME shops.example.net.", "@ TXT google-site-verification=abc", "@ MX aspmx.l.google.com."} {
		if !strings.Contains(got, want) {
			t.Errorf("records %s: missing %q", got, want)
		}
	}
	if len(warnings) != 0 || len(applied) != 1 || !strings.Contains(applied[0], "dns → example.com (3 records") {
		t.Fatalf("applied %v warnings %v", applied, warnings)
	}
	// Scheduled twice: so the reconciler makes the zone now, then publishes
	// the records added to it.
	if got := strings.Join(f.sched.ids, ","); got != "d1,d1" {
		t.Fatalf("scheduled %q, want d1 to create the zone and d1 again to publish the records", got)
	}
	// One more look after the zone first appears, so the zone's own records
	// are in before the conflict checks run.
	if f.zones.lookups["d1"] < 4 {
		t.Fatalf("zone looked up %d times, want a settle look after it appeared", f.zones.lookups["d1"])
	}
}

func TestRestoreBundleDNS_RecordsGoThroughTheCreateChecks(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	f := newRDFixture()
	f.records.rows = append(f.records.rows, models.DNSRecord{ID: "u-txt", ZoneID: "z1", Name: "@", Type: "TXT", Content: "v=keep", IsEnabled: true})
	applied, warnings := f.run(t, false,
		rec("@", "TXT", "v=keep"),             // already there: skipped quietly
		rec("bad name!", "A", "198.51.100.7"), // invalid
		rec("www", "A", "198.51.100.7"),       // conflicts with the zone's www CNAME
		rec("@", "A", "198.51.100.99"),        // this server publishes its own apex A
		rec("api", "A", "198.51.100.7"),       // fine
	)

	got := f.records.userRecords("z1")
	if strings.Join(got, "|") != "@ TXT v=keep|api A 198.51.100.7" {
		t.Fatalf("records %v, want only the kept TXT and api A", got)
	}
	w := strings.Join(warnings, "\n")
	for _, want := range []string{
		"dns example.com: record bad name! A not restored:",
		"dns example.com: record www A not restored: cannot add A at \"www\" because a CNAME already exists there",
		"dns example.com: record @ A not restored: this server publishes its own A record at @",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings %q: missing %q", w, want)
		}
	}
	if len(applied) != 1 || !strings.Contains(applied[0], "(1 records restored, 1 already there)") {
		t.Fatalf("applied %v", applied)
	}
	// The zone was there already: scheduled once, to publish the new record.
	if got := strings.Join(f.sched.ids, ","); got != "d1" {
		t.Fatalf("scheduled %q, want d1 once", got)
	}
}

// A disabled zone isn't published, so records added to it would never be
// served: the restore says so instead of adding them.
func TestRestoreBundleDNS_DisabledZoneIsReported(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	f := newRDFixture()
	f.zones.zones["d1"].IsEnabled = false
	applied, warnings := f.run(t, false, rec("api", "A", "198.51.100.7"))
	if got := f.records.userRecords("z1"); len(got) != 0 || len(applied) != 0 {
		t.Fatalf("records %v applied %v, want nothing added to a disabled zone", got, applied)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "dns example.com: 1 records not restored: This DNS zone is disabled") {
		t.Fatalf("warnings %v", warnings)
	}
}

// From an uploaded file the account's record-type policy applies (the file's
// author isn't the admin), and records pointing away from this server are
// listed for the admin to check. From this server's own backup, as an admin
// create, the policy doesn't apply.
func TestRestoreBundleDNS_UploadedFileFollowsTheAccountsRecordPolicy(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	f := newRDFixture()
	_, warnings := f.run(t, true, rec("sub", "NS", "ns1.elsewhere.net."), rec("blog", "A", "198.51.100.7"))
	if got := f.records.userRecords("z1"); strings.Join(got, "|") != "blog A 198.51.100.7" {
		t.Fatalf("uploaded: records %v, want the NS refused", got)
	}
	w := strings.Join(warnings, "\n")
	if !strings.Contains(w, "dns example.com: record sub NS not restored: accounts on this server can't create NS records") ||
		!strings.Contains(w, "dns example.com: check that these records still point where you want; they don't point at this server: blog A 198.51.100.7") {
		t.Fatalf("uploaded: warnings %q", w)
	}

	f = newRDFixture()
	_, warnings = f.run(t, false, rec("sub", "NS", "ns1.elsewhere.net."), rec("blog", "A", "198.51.100.7"))
	if got := f.records.userRecords("z1"); len(got) != 2 || len(warnings) != 0 {
		t.Fatalf("own backup: records %v warnings %v, want both kept without notes", got, warnings)
	}
}

func TestRestoreBundleDNS_SkipsWhatItCantRestoreWithoutWaiting(t *testing.T) {
	const wait = 300 * time.Millisecond
	fastRestoreDNSWait(t, wait)
	noWait := func(t *testing.T, f *rdFixture) []string {
		t.Helper()
		start := time.Now()
		_, warnings := f.run(t, false, rec("api", "A", "198.51.100.7"))
		if time.Since(start) >= wait || strings.Contains(strings.Join(warnings, "\n"), "wasn't created in time") {
			t.Fatalf("waited for a zone that will never come: warnings %v", warnings)
		}
		return warnings
	}

	// DNS hosted elsewhere: the reconciler never makes a zone for it.
	f := newRDFixture()
	f.domains.rows["example.com"].DNSDisabled = true
	delete(f.zones.zones, "d1")
	if w := noWait(t, f); !strings.Contains(strings.Join(w, "\n"), "dns example.com: 1 records not restored: DNS for this domain is hosted elsewhere") {
		t.Fatalf("DNS hosted elsewhere: warnings %v", w)
	}

	// Ownership not verified yet: no zone until it is.
	f = newRDFixture()
	f.domains.rows["example.com"].OwnershipStatus = models.OwnershipPending
	delete(f.zones.zones, "d1")
	if w := noWait(t, f); !strings.Contains(strings.Join(w, "\n"), "dns example.com: 1 records not restored: the domain's ownership isn't verified yet") {
		t.Fatalf("pending ownership: warnings %v", w)
	}

	// Another account's domain of the same name: nothing (Apply refused it).
	f = newRDFixture()
	f.domains.rows["example.com"].UserID = "u-bob"
	applied, warnings := f.run(t, false, rec("api", "A", "198.51.100.7"))
	if len(f.records.userRecords("z1")) != 0 || len(applied) != 0 || len(warnings) != 0 {
		t.Fatalf("another account's domain: records %v applied %v warnings %v", f.records.userRecords("z1"), applied, warnings)
	}
}

func TestRestoreBundleDNS_ReportsAZoneThatNeverAppears(t *testing.T) {
	fastRestoreDNSWait(t, 20*time.Millisecond)
	f := newRDFixture()
	delete(f.zones.zones, "d1")
	_, warnings := f.run(t, false, rec("api", "A", "198.51.100.7"))
	if !strings.Contains(strings.Join(warnings, "\n"), "dns example.com: 1 records not restored: its DNS zone wasn't created in time") {
		t.Fatalf("warnings %v", warnings)
	}
}

// rdUploadDomains serves both the upload restore's lookups (ListByUserID) and
// the DNS step's (FindByName); Apply's domain step finds the domain by id.
type rdUploadDomains struct {
	rdDomains
}

func (r *rdUploadDomains) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	var out []models.Domain
	for _, d := range r.rows {
		if d.UserID == userID {
			out = append(out, *d)
		}
	}
	return out, int64(len(out)), nil
}
func (r *rdUploadDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	for _, d := range r.rows {
		if d.ID == id {
			return d, nil
		}
	}
	return nil, repository.ErrNotFound
}

// The upload restore adds the archive's DNS records for the account's
// domains, with or without a mail pass.
func TestRunUploadRestore_RestoresTheDomainsDNSRecords(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	for name, stages := range map[string]string{"no mail": `[{"name":"home"}]`, "with mail": `[{"name":"home"},{"name":"mail"}]`} {
		t.Run(name, func(t *testing.T) {
			f := newRDFixture()
			f.domains.rows["example.com"].UserID = "T"
			f.domains.rows["example.com"].ID = "src-d1" // the account's own domain, same id as in the archive
			f.zones.zones["src-d1"] = f.zones.zones["d1"]
			h, a, _ := ucUploadPasses(t, func(int) string {
				return `{"upload_confinement_enforced":true,"stages":` + stages + `,"metadata":` +
					`{"user":{"id":"SRC","username":"alice"},"domains":[{"id":"src-d1","name":"example.com",` +
					`"dns_records":[{"name":"shop","type":"CNAME","content":"shops.example.net.","ttl":3600,"is_enabled":true},` +
					`{"name":"sub","type":"NS","content":"ns1.elsewhere.net.","ttl":3600,"is_enabled":true}]}]}}`
			})
			h.cfg.Domains = &rdUploadDomains{*f.domains}
			h.cfg.DNSZones, h.cfg.DNSRecords = f.zones, f.records
			h.cfg.ServerSettings = rdSettings{s: f.srv}
			h.cfg.Scheduler = f.sched
			h.runUploadRestore(a)

			// The uploaded file's records follow the account's record-type
			// policy: the NS record is refused.
			if got := strings.Join(f.records.userRecords("z1"), "|"); got != "shop CNAME shops.example.net." {
				t.Fatalf("records %q, want the archive's CNAME only", got)
			}
			o, err := readRestoreUploadOutcome(a.outcomePath)
			if err != nil || o.Status != "done" || !strings.Contains(strings.Join(o.Applied, "|"), "dns → example.com (1 records restored)") ||
				!strings.Contains(strings.Join(o.Warnings, "|"), "record sub NS not restored: accounts on this server can't create NS records") {
				t.Fatalf("outcome %+v err=%v", o, err)
			}
		})
	}
}

// An admin restore from a backup destination adds the domains' DNS records
// too, as an admin create (no record-type policy).
func TestRunAccountRestoreJob_RestoresTheDomainsDNSRecords(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	f := newRDFixture()
	jobs := newSealCapture()
	h := &backupHandler{cfg: BackupHandlerConfig{
		Jobs: jobs,
		Agent: restoreAgent{reply: json.RawMessage(`{"job_id":"job-1","stages":[{"name":"home","status":"ok"}],"metadata":` +
			`{"user":{"id":"u1","username":"alice"},"domains":[{"id":"d1","name":"example.com",` +
			`"dns_records":[{"name":"sub","type":"NS","content":"ns1.elsewhere.net.","ttl":3600,"is_enabled":true}]}]}}`)},
		Domains: &rdUploadDomains{*f.domains}, DNSZones: f.zones, DNSRecords: f.records,
		ServerSettings: rdSettings{s: f.srv}, Scheduler: f.sched,
	}}
	h.runAccountRestoreJob("job-1", &models.BackupDestination{ID: "d1", Kind: "local"}, map[string]any{})
	jobs.wait(t)

	if got := strings.Join(f.records.userRecords("z1"), "|"); got != "sub NS ns1.elsewhere.net." {
		t.Fatalf("records %q (status %s, error %q), want the backup's NS record", got, jobs.status, jobs.errText)
	}
	if strings.Contains(jobs.errText, "dns ") {
		t.Fatalf("error %q: the DNS step should have had nothing to report", jobs.errText)
	}
}
