package dnscompile

import (
	"strconv"
	"testing"
	"time"
)

func TestBuildMTAStsRecords_HappyPath(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	recs := BuildMTAStsRecords("zone1", "example.com", "203.0.113.7", 1716206400, nil, idCounter(), now)
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	byType := map[string]int{}
	for _, r := range recs {
		byType[r.Type]++
		if r.ZoneID != "zone1" || !r.Managed || r.ManagedBy == nil || *r.ManagedBy != "mta-sts" {
			t.Errorf("%s: bad scope/manager: %+v", r.Type, r)
		}
		if r.TTL != 300 {
			t.Errorf("%s: ttl %d != 300", r.Type, r.TTL)
		}
	}
	if byType["A"] != 1 || byType["TXT"] != 1 {
		t.Errorf("type breakdown: %v", byType)
	}
	for _, r := range recs {
		switch r.Type {
		case "A":
			if r.Name != "mta-sts" || r.Content != "203.0.113.7" {
				t.Errorf("A: name=%q content=%q", r.Name, r.Content)
			}
		case "TXT":
			if r.Name != "_mta-sts" {
				t.Errorf("TXT: name=%q", r.Name)
			}
			// quoted, includes id
			want := `"v=STSv1; id=` + strconv.FormatUint(1716206400, 10) + `"`
			if r.Content != want {
				t.Errorf("TXT content = %q, want %q", r.Content, want)
			}
		}
	}
}

func TestBuildMTAStsRecords_NilWhenEmptyIP(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	if got := BuildMTAStsRecords("z", "x.com", "", 1, nil, idCounter(), now); got != nil {
		t.Errorf("empty ip should return nil, got %+v", got)
	}
}

func TestBuildMTAStsRecords_NilWhenZeroID(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	if got := BuildMTAStsRecords("z", "x.com", "203.0.113.7", 0, nil, idCounter(), now); got != nil {
		t.Errorf("zero id should return nil (no policy ever published), got %+v", got)
	}
}
