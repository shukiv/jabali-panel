package pdns

import (
	"strings"
	"testing"
)

// TestZoneDeletePlan_OrderAndShape pins the invariant that broke GH #1620:
// child rows (records, domainmetadata, ...) are deleted BEFORE the parent
// `domains` row, each scoped by a domain_id subquery, and the parent delete is
// last. No DB — this asserts the statement plan, not a live schema.
func TestZoneDeletePlan_OrderAndShape(t *testing.T) {
	all := zoneDeletePlan(map[string]bool{"comments": true, "cryptokeys": true})
	wantTables := []string{"records", "domainmetadata", "comments", "cryptokeys", "domains"}
	if len(all) != len(wantTables) {
		t.Fatalf("plan len = %d, want %d: %+v", len(all), len(wantTables), all)
	}
	for i, w := range wantTables {
		if all[i].table != w {
			t.Fatalf("step %d table = %q, want %q", i, all[i].table, w)
		}
	}

	// Every child (all but the last) scopes by the domain_id subquery and never
	// deletes the parent `domains` row at its head — otherwise a same-name
	// duplicate would strand children, or the parent would vanish first.
	for _, s := range all[:len(all)-1] {
		if !strings.Contains(s.sql, "domain_id IN (SELECT id FROM domains WHERE name = ?)") {
			t.Fatalf("child %s must scope by domain_id subquery, got %q", s.table, s.sql)
		}
		if strings.HasPrefix(s.sql, "DELETE FROM domains ") {
			t.Fatalf("child %s must not delete the parent domains row: %q", s.table, s.sql)
		}
	}

	// Parent is last and is exactly the domains-by-name delete.
	last := all[len(all)-1]
	if last.table != "domains" || last.sql != "DELETE FROM domains WHERE name = ?" {
		t.Fatalf("last step = %+v, want the domains-by-name delete", last)
	}

	// Every statement binds exactly one placeholder — the zone name.
	for _, s := range all {
		if n := strings.Count(s.sql, "?"); n != 1 {
			t.Fatalf("step %s must bind exactly one arg, got %d: %q", s.table, n, s.sql)
		}
	}
}

// TestZoneDeletePlan_OptionalTablesSkipped verifies that a schema lacking the
// optional tables produces a plan touching only the mandatory ones — the
// probe-then-plan design never emits a DELETE for a table that isn't there.
func TestZoneDeletePlan_OptionalTablesSkipped(t *testing.T) {
	min := zoneDeletePlan(map[string]bool{})
	wantTables := []string{"records", "domainmetadata", "domains"}
	if len(min) != len(wantTables) {
		t.Fatalf("minimal plan len = %d, want %d: %+v", len(min), len(wantTables), min)
	}
	for i, w := range wantTables {
		if min[i].table != w {
			t.Fatalf("minimal step %d = %q, want %q", i, min[i].table, w)
		}
	}

	// comments present, cryptokeys absent → comments in, cryptokeys out, order held.
	one := zoneDeletePlan(map[string]bool{"comments": true})
	wantOne := []string{"records", "domainmetadata", "comments", "domains"}
	if len(one) != len(wantOne) {
		t.Fatalf("plan len = %d, want %d: %+v", len(one), len(wantOne), one)
	}
	for i, w := range wantOne {
		if one[i].table != w {
			t.Fatalf("step %d = %q, want %q", i, one[i].table, w)
		}
	}
}

// TestOrphanSweepPlan_ShapeAndSafety pins the invariants of the GH #1620 sweep.
// The single most important one: the plan must NEVER emit a `DELETE FROM
// domains`. Orphans are defined by the absence of their parent `domains` row, so
// the table is read-only here; a delete step would be catastrophic (it would
// remove live zones). This is new code, so there is no "unfixed" version to fail
// against — the test exists to guard that safety property against future edits.
func TestOrphanSweepPlan_ShapeAndSafety(t *testing.T) {
	all := orphanSweepPlan(map[string]bool{"comments": true, "cryptokeys": true})
	wantTables := []string{"records", "domainmetadata", "comments", "cryptokeys"}
	if len(all) != len(wantTables) {
		t.Fatalf("plan len = %d, want %d: %+v", len(all), len(wantTables), all)
	}
	for i, w := range wantTables {
		if all[i].table != w {
			t.Fatalf("step %d table = %q, want %q", i, all[i].table, w)
		}
	}

	for _, s := range all {
		// Every step targets a CHILD table by the orphan predicate — never the
		// parent `domains` row, and never a name-scoped delete.
		if s.table == "domains" || strings.Contains(s.deleteSQL, "DELETE FROM domains") {
			t.Fatalf("orphan sweep must never delete the parent domains row: %q", s.deleteSQL)
		}
		if !strings.Contains(s.deleteSQL, "domain_id NOT IN (SELECT id FROM domains)") {
			t.Fatalf("step %s must scope by the orphan predicate, got %q", s.table, s.deleteSQL)
		}
		// The orphan predicate binds no args — it is a self-contained subquery.
		if n := strings.Count(s.deleteSQL, "?"); n != 0 {
			t.Fatalf("step %s must bind no args, got %d: %q", s.table, n, s.deleteSQL)
		}
		// The count statement counts the same table under the same predicate.
		if !strings.HasPrefix(s.countSQL, "SELECT COUNT(*) FROM "+s.table+" ") {
			t.Fatalf("step %s count must be COUNT(*) on the same table, got %q", s.table, s.countSQL)
		}
		if !strings.Contains(s.countSQL, "domain_id NOT IN (SELECT id FROM domains)") {
			t.Fatalf("step %s count must use the orphan predicate, got %q", s.table, s.countSQL)
		}
	}
}

// TestOrphanSweepPlan_OptionalTablesSkipped verifies the optional tables are
// gated exactly like zoneDeletePlan — absent tables produce no statement.
func TestOrphanSweepPlan_OptionalTablesSkipped(t *testing.T) {
	min := orphanSweepPlan(map[string]bool{})
	want := []string{"records", "domainmetadata"}
	if len(min) != len(want) {
		t.Fatalf("minimal plan len = %d, want %d: %+v", len(min), len(want), min)
	}
	for i, w := range want {
		if min[i].table != w {
			t.Fatalf("minimal step %d = %q, want %q", i, min[i].table, w)
		}
	}

	one := orphanSweepPlan(map[string]bool{"cryptokeys": true})
	wantOne := []string{"records", "domainmetadata", "cryptokeys"}
	if len(one) != len(wantOne) {
		t.Fatalf("plan len = %d, want %d: %+v", len(one), len(wantOne), one)
	}
	for i, w := range wantOne {
		if one[i].table != w {
			t.Fatalf("step %d = %q, want %q", i, one[i].table, w)
		}
	}
}
