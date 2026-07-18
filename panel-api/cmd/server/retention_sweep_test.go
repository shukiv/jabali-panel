package main

import (
	"strings"
	"testing"
	"time"
)

// The retention sweep interpolates table + tsColumn straight into a DELETE, so
// every target must be a well-formed, non-empty identifier with a sane window.
func TestRetentionTargets_WellFormed(t *testing.T) {
	if len(retentionTargets) == 0 {
		t.Fatal("no retention targets defined")
	}
	identRe := func(s string) bool {
		if s == "" {
			return false
		}
		for _, r := range s {
			if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				return false
			}
		}
		return true
	}
	seen := map[string]bool{}
	for _, tgt := range retentionTargets {
		if !identRe(tgt.table) {
			t.Errorf("table %q is not a bare identifier (SQL-injection guard)", tgt.table)
		}
		if !identRe(tgt.tsColumn) {
			t.Errorf("%s: tsColumn %q is not a bare identifier", tgt.table, tgt.tsColumn)
		}
		if tgt.window <= 0 {
			t.Errorf("%s: window must be positive, got %s", tgt.table, tgt.window)
		}
		if tgt.window < 24*time.Hour {
			t.Errorf("%s: window %s is under a day — too aggressive, likely a mistake", tgt.table, tgt.window)
		}
		if seen[tgt.table] {
			t.Errorf("%s: duplicate target", tgt.table)
		}
		seen[tgt.table] = true
	}
	if retentionBatchSize <= 0 {
		t.Errorf("retentionBatchSize must be positive, got %d", retentionBatchSize)
	}
}

// audit_events is a hash-chained tamper-evident log; a blind DELETE would dangle
// the chain. It must never be swept here — guard the exclusion so a future edit
// can't add it back without tripping this test.
func TestRetentionTargets_ExcludesHashChainedAudit(t *testing.T) {
	for _, tgt := range retentionTargets {
		if tgt.table == "audit_events" {
			t.Fatal("audit_events (hash-chained) must NOT be in the retention sweep — see JAB-105")
		}
	}
}

// Security-forensics tables get a generous window so incidents survive.
func TestRetentionTargets_SecurityTablesKeptLong(t *testing.T) {
	minSecurity := 365 * retentionDay
	security := map[string]bool{"malware_events": true, "snuffleupagus_incidents": true, "db_admin_audit": true}
	for _, tgt := range retentionTargets {
		if security[tgt.table] && tgt.window < minSecurity {
			t.Errorf("%s is security-sensitive; window %s is under the 1y forensics floor", tgt.table, tgt.window)
		}
	}
}

// JAB-102: the migration_jobs prune must be gated to TERMINAL states — never
// delete an in-flight job. JAB-124: bw_daily is a configurable target.
func TestRetentionTargets_P2MigrationAndBandwidth(t *testing.T) {
	byTable := map[string]retentionTarget{}
	for _, tgt := range retentionTargets {
		byTable[tgt.table] = tgt
	}

	mj, ok := byTable["migration_jobs"]
	if !ok {
		t.Fatal("migration_jobs must be a retention target (JAB-102)")
	}
	if !strings.Contains(mj.extraWhere, "state IN") {
		t.Errorf("migration_jobs prune must filter on terminal state, got extraWhere=%q", mj.extraWhere)
	}
	for _, s := range []string{"pending", "analyzing", "restoring"} {
		if strings.Contains(mj.extraWhere, s) {
			t.Errorf("migration_jobs prune must NOT include the in-flight state %q", s)
		}
	}
	if mj.tsColumn != "ended_at" {
		t.Errorf("migration_jobs must prune on ended_at (NULL for in-flight), got %q", mj.tsColumn)
	}

	bw, ok := byTable["bw_daily"]
	if !ok {
		t.Fatal("bw_daily must be a retention target (JAB-124)")
	}
	if bw.category != "bandwidth" {
		t.Errorf("bw_daily category = %q, want bandwidth", bw.category)
	}
}
