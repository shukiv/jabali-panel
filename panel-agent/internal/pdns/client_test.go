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
