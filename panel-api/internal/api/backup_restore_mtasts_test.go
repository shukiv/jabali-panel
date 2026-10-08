package api

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a domain whose MTA-STS the restore turned back on gets its two
// DNS records — the policy host's address and the _mta-sts TXT with the
// policy id — once its zone exists. Without them a receiving server can't
// find the policy.

func (r *rdRecords) DeleteByZoneIDAndManagedBy(_ context.Context, zoneID, managedBy string) error {
	kept := r.rows[:0:0]
	for _, x := range r.rows {
		if x.ZoneID == zoneID && x.ManagedBy != nil && *x.ManagedBy == managedBy {
			continue
		}
		kept = append(kept, x)
	}
	r.rows = kept
	return nil
}

// mtaStsRows is the zone's MTA-STS records as "name type content", sorted.
func (r *rdRecords) mtaStsRows(zoneID string) []string {
	var out []string
	for _, x := range r.rows {
		if x.ZoneID == zoneID && x.ManagedBy != nil && *x.ManagedBy == dnscompile.MTAStsRecordsManagedBy {
			out = append(out, x.Name+" "+x.Type+" "+x.Content)
		}
	}
	sort.Strings(out)
	return out
}

func (f *rdFixture) runMTASts(t *testing.T, recs ...internalbackup.MetadataDNSRecord) ([]string, []string) {
	t.Helper()
	meta := internalbackup.AccountMetadata{Domains: []internalbackup.MetadataDomain{
		{ID: "src-d1", Name: "example.com", MTASTSEnabled: true, DNSRecords: recs}}}
	raw, _ := json.Marshal(meta)
	return RestoreBundleDNS(context.Background(), RestoreDNSDeps{
		Domains: f.domains, Zones: f.zones, Records: f.records, Settings: rdSettings{s: f.srv}, Scheduler: f.sched,
	}, raw, "u1")
}

const rdMTAStsWant = `_mta-sts TXT "v=STSv1; id=1700000000"|mta-sts A 203.0.113.10`

func TestRestoreBundleDNS_PublishesMTAStsRecordsOnceTheZoneExists(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	f := newRDFixture()
	d := f.domains.rows["example.com"]
	d.MTASTSEnabled, d.MTASTSId = true, 1700000000
	f.zones.appearAt["d1"] = 3
	applied, warnings := f.runMTASts(t)

	if got := strings.Join(f.records.mtaStsRows("z1"), "|"); got != rdMTAStsWant {
		t.Fatalf("MTA-STS records %q, want %q (warnings %v)", got, rdMTAStsWant, warnings)
	}
	if len(warnings) != 0 || len(applied) != 1 || !strings.Contains(applied[0], "dns → example.com: MTA-STS records published") {
		t.Fatalf("applied %v warnings %v", applied, warnings)
	}
	if got := strings.Join(f.sched.ids, ","); got != "d1,d1" {
		t.Fatalf("scheduled %q, want d1 to create the zone and d1 again to publish the records", got)
	}
}

// A domain the restore didn't turn MTA-STS on for (Apply left it off and said
// why) gets no records, and the restore doesn't wait for its zone.
func TestRestoreBundleDNS_PublishesNoMTAStsRecordsForADomainWithItOff(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	f := newRDFixture()
	applied, warnings := f.runMTASts(t)
	if rows := f.records.mtaStsRows("z1"); len(rows) != 0 || len(applied) != 0 || len(warnings) != 0 || f.zones.lookups["d1"] != 0 {
		t.Fatalf("records %v applied %v warnings %v zone lookups %d; want nothing", rows, applied, warnings, f.zones.lookups["d1"])
	}
}

// Records already there stay as they are; a stale policy id is replaced.
func TestRestoreBundleDNS_MTAStsRecordsAreKeptOrReplaced(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	for name, txts := range map[string][]string{
		"same":  {`"v=STSv1; id=1700000000"`},
		"stale": {`"v=STSv1; id=1600000000"`},
		"extra": {`"v=STSv1; id=1700000000"`, `"v=STSv1; id=1600000000"`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRDFixture()
			d := f.domains.rows["example.com"]
			d.MTASTSEnabled, d.MTASTSId = true, 1700000000
			marker := dnscompile.MTAStsRecordsManagedBy
			f.records.rows = append(f.records.rows,
				models.DNSRecord{ID: "m-a", ZoneID: "z1", Name: "mta-sts", Type: "A", Content: "203.0.113.10", Managed: true, ManagedBy: &marker, IsEnabled: true})
			for i, txt := range txts {
				f.records.rows = append(f.records.rows, models.DNSRecord{ID: "m-t" + string(rune('0'+i)), ZoneID: "z1", Name: "_mta-sts", Type: "TXT", Content: txt, Managed: true, ManagedBy: &marker, IsEnabled: true})
			}
			_, warnings := f.runMTASts(t)
			if got := strings.Join(f.records.mtaStsRows("z1"), "|"); got != rdMTAStsWant || len(warnings) != 0 {
				t.Fatalf("MTA-STS records %q warnings %v, want %q", got, warnings, rdMTAStsWant)
			}
		})
	}
}

// A zone that isn't served gets no MTA-STS records; the report says why.
func TestRestoreBundleDNS_MTAStsRecordsNeedAServedZone(t *testing.T) {
	fastRestoreDNSWait(t, time.Second)
	f := newRDFixture()
	d := f.domains.rows["example.com"]
	d.MTASTSEnabled, d.MTASTSId = true, 1700000000
	f.zones.zones["d1"].IsEnabled = false
	_, warnings := f.runMTASts(t)
	if rows := f.records.mtaStsRows("z1"); len(rows) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "dns example.com: MTA-STS records not published: This DNS zone is disabled") {
		t.Fatalf("records %v warnings %v", rows, warnings)
	}
}

// The restore doors that run RestoreBundleDNS tell Apply so: a restore from
// this server's own backup always does, an uploaded file's unless DNS records
// are skipped.
func TestRestoreMetadataDeps_RestoresDNSUnlessTheUploadSkipsIt(t *testing.T) {
	h := &backupHandler{}
	for _, tc := range []struct {
		name     string
		uploaded *uploadedData
		want     bool
	}{
		{"own backup", nil, true},
		{"upload", &uploadedData{}, true},
		{"upload without DNS", &uploadedData{skipDNS: true}, false},
	} {
		if got := h.restoreMetadataDeps(tc.uploaded).RestoresDNS; got != tc.want {
			t.Errorf("%s: RestoresDNS %v, want %v", tc.name, got, tc.want)
		}
	}
	// The upload door passes its choice on.
	src, err := os.ReadFile("backup_restore_upload_confine.go")
	if err != nil || !strings.Contains(string(src), "skipDNS: skips.dns") {
		t.Fatal("restoreUploadedAccount must pass skips.dns on as uploadedData.skipDNS")
	}
}

// GH #1993 (found on the way): `jabali domain mta-sts --enable` turned the
// switch on but published no DNS records — only the mail page's toggle did,
// and the CLI said the reconciler would. Both now run SyncMTAStsDNS.
func TestSyncMTAStsDNS_PublishesWhenOnAndRemovesWhenOff(t *testing.T) {
	f := newRDFixture()
	d := f.domains.rows["example.com"]
	d.ID, d.MTASTSEnabled, d.MTASTSId = "d1", true, 1700000000
	deps := MTAStsDNSDeps{Zones: f.zones, Records: f.records, Settings: rdSettings{s: f.srv}}
	if err := SyncMTAStsDNS(context.Background(), deps, d); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := strings.Join(f.records.mtaStsRows("z1"), "|"); got != rdMTAStsWant {
		t.Fatalf("records %q, want %q", got, rdMTAStsWant)
	}
	d.MTASTSEnabled = false
	if err := SyncMTAStsDNS(context.Background(), deps, d); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if rows := f.records.mtaStsRows("z1"); len(rows) != 0 || len(f.records.rows) != 2 {
		t.Fatalf("MTA-STS records %v, rows %d; want only the zone's own two left", rows, len(f.records.rows))
	}
	d.ID = "d-none"
	if err := SyncMTAStsDNS(context.Background(), deps, d); err == nil {
		t.Fatal("a domain without a zone must say so")
	}
}

func TestDomainMTAStsCLI_SyncsTheDNSRecords(t *testing.T) {
	src, err := os.ReadFile("../../cmd/server/domain_advanced_cmd.go")
	if err != nil || !strings.Contains(string(src), "api.SyncMTAStsDNS(") || strings.Contains(string(src), "The reconciler publishes/removes the DNS") {
		t.Fatal("`jabali domain mta-sts` must publish or remove the records through api.SyncMTAStsDNS")
	}
}
