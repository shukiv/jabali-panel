package commands

import "testing"

// Real `repquota -O csv /` output captured on the .60 test box (ext4, quota
// utilities 4.06), plus a tenant row with a real hard limit and edge cases.
const repquotaCSVFixture = `User,BlockStatus,FileStatus,BlockUsed,BlockSoftLimit,BlockHardLimit,BlockGrace,FileUsed,FileSoftLimit,FileHardLimit,FileGrace
root,ok,ok,18308880,0,0,,274759,0,0,
www-data,ok,ok,51644,0,0,,2384,0,0,
alice,ok,ok,2000000,1000000,2000000,,100,0,0,
empty,ok,ok,0,0,0,,0,0,0,
#1005,ok,ok,500,0,0,,5,0,0,
`

func entryMap(es []QuotaInventoryEntry) map[string]QuotaInventoryEntry {
	m := make(map[string]QuotaInventoryEntry, len(es))
	for _, e := range es {
		m[e.Username] = e
	}
	return m
}

func TestParseRepquotaCSV_RealFixture(t *testing.T) {
	entries, partial := parseRepquotaCSV(repquotaCSVFixture)
	if partial {
		t.Error("clean fixture must not be marked partial")
	}
	m := entryMap(entries)
	// header + #1005 (numeric-uid-only) skipped → root, www-data, alice, empty.
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4: %+v", len(entries), entries)
	}
	if m["root"].UsedKB != 18308880 || m["root"].LimitKB != 0 {
		t.Errorf("root = %+v, want used=18308880 limit=0(unlimited)", m["root"])
	}
	if m["alice"].UsedKB != 2000000 || m["alice"].LimitKB != 2000000 {
		t.Errorf("alice = %+v, want used=2000000 limit=2000000", m["alice"])
	}
	if e, ok := m["empty"]; !ok || e.UsedKB != 0 {
		t.Errorf("empty user should be present with 0 used, got %+v ok=%v", e, ok)
	}
	if _, ok := m["#1005"]; ok {
		t.Error("numeric-uid-only user (#1005) must be skipped")
	}
}

func TestParseRepquotaCSV_GarbledLineIsPartialNotZero(t *testing.T) {
	// A row whose BlockUsed is non-numeric must NOT become a fabricated 0
	// (that would clear a real quota alert); it drops + flags partial.
	in := "User,BlockStatus,FileStatus,BlockUsed,BlockSoftLimit,BlockHardLimit,BlockGrace\n" +
		"alice,ok,ok,1000,0,2000,\n" +
		"bad,ok,ok,NOTANUMBER,0,0,\n"
	entries, partial := parseRepquotaCSV(in)
	if !partial {
		t.Error("a garbled numeric field must set partial=true")
	}
	m := entryMap(entries)
	if len(entries) != 1 || m["alice"].UsedKB != 1000 {
		t.Errorf("the good row must still parse; got %+v", entries)
	}
	if _, ok := m["bad"]; ok {
		t.Error("garbled row must be dropped, never emitted as 0")
	}
}

func TestParseRepquotaCSV_EmptyAndHeaderOnly(t *testing.T) {
	for _, in := range []string{"", "\n\n", "User,BlockStatus,FileStatus,BlockUsed,BlockSoftLimit,BlockHardLimit\n"} {
		entries, partial := parseRepquotaCSV(in)
		if len(entries) != 0 || partial {
			t.Errorf("input %q: want 0 entries + not partial, got %d partial=%v", in, len(entries), partial)
		}
	}
}
