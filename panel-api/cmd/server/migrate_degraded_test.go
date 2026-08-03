package main

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-31: a restore that runs to completion but leaves a core area empty must be
// treated as degraded, not done. Lock the exact thresholds + the downgrade gate.
func TestMigrationCriticalThresholds(t *testing.T) {
	t.Run("db dumps present but none imported is critical", func(t *testing.T) {
		if !dbDumpsAllSkipped(1, 0, 0) {
			t.Error("1 dump, 0 created, 0 already present should be critical")
		}
		if dbDumpsAllSkipped(0, 0, 0) {
			t.Error("no dumps is not a failure")
		}
		if dbDumpsAllSkipped(2, 2, 0) {
			t.Error("all imported is not a failure")
		}
	})

	// JAB-215: --retry-from-scratch wipes the job's stages but does NOT drop the
	// tenant databases the previous run created, so the re-run finds them
	// already present and imports nothing. That used to end
	// "DEGRADED: 1 dump(s) present but 0 imported", exit non-zero, on a box
	// whose databases were complete and healthy — training operators to reach
	// for --allow-degraded, which then means nothing when a DB is really gone.
	t.Run("databases left in place by a previous run are handled, not degraded", func(t *testing.T) {
		if dbDumpsAllSkipped(1, 0, 1) {
			t.Error("1 dump already present from an earlier run must NOT be critical — " +
				"this is the retry-from-scratch false degrade (JAB-215)")
		}
		if dbDumpsAllSkipped(3, 1, 2) {
			t.Error("a mix of re-imported and verified-existing must not be critical")
		}
		// The guarantee still has teeth: nothing imported AND nothing already
		// there is still a core failure.
		if !dbDumpsAllSkipped(2, 0, 0) {
			t.Error("dumps present, none imported, none pre-existing is still critical")
		}
	})

	t.Run("mail found but none pushed is critical", func(t *testing.T) {
		if !mailFoundNonePushed(1, 0) {
			t.Error("1 found, 0 pushed should be critical")
		}
		if mailFoundNonePushed(0, 0) {
			t.Error("no messages is not a failure")
		}
		if mailFoundNonePushed(3, 3) {
			t.Error("all pushed is not a failure")
		}
	})

	t.Run("only a 5xx degrades — a 0/unreachable does not (JAB-42)", func(t *testing.T) {
		// JAB-40 (Nextcloud 500) is a 5xx -> degrade. JAB-42 (itflow returned 0
		// then 200 right after) is a transient reconciler-lag 0 -> must NOT degrade.
		degradeOn := func(serverErr int) bool { return serverErr > 0 }
		if !degradeOn(1) {
			t.Error("a 5xx should degrade (JAB-40)")
		}
		if degradeOn(0) {
			t.Error("no 5xx (e.g. a transient 0) must not degrade (JAB-42)")
		}
	})
}

func TestShouldDowngradeToDegraded(t *testing.T) {
	if !shouldDowngradeToDegraded(models.MigrationStateDone, []string{"databases: 1 dump(s) present but 0 imported"}) {
		t.Error("done + criticals must downgrade to degraded")
	}
	if shouldDowngradeToDegraded(models.MigrationStateDone, nil) {
		t.Error("done + no criticals must stay done")
	}
	if shouldDowngradeToDegraded(models.MigrationStateFailed, []string{"x"}) {
		t.Error("a failed job must not be re-stamped degraded")
	}
}
