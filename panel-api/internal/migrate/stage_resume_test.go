package migrate

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-216. A restore OOM-wedged its host, the box needed a console reboot, and
// the job was left in `restoring`. The runner re-enters a resume at analyze, the
// transition table refused restoring -> analyzing, and the only way out was
// --retry-from-scratch — redoing hours of work that had already succeeded.
//
// A state written to the database describing a process that no longer exists is
// a stale marker, not a lock.
func TestRestoringIsResumable(t *testing.T) {
	if !IsValidJobTransition(models.MigrationStateRestoring, models.MigrationStateAnalyzing) {
		t.Error("restoring -> analyzing refused: a job interrupted by host death cannot be resumed")
	}
	if !IsValidJobTransition(models.MigrationStateRestoring, models.MigrationStatePending) {
		t.Error("restoring -> pending refused: --retry-from-scratch would depend on bypassing this table")
	}
}

// The resumable states must behave alike — an operator should not have to know
// which flavour of interruption they hit.
func TestResumableStatesAgree(t *testing.T) {
	for _, from := range []string{
		models.MigrationStateFailed,
		models.MigrationStateDegraded,
		models.MigrationStateRestoring,
	} {
		if !IsValidJobTransition(from, models.MigrationStateAnalyzing) {
			t.Errorf("%s -> analyzing refused, but %s is meant to be resumable", from, from)
		}
	}
}

// Terminal really is terminal — resumability must not have leaked into states
// where a re-run would duplicate completed work or revive a cancelled job.
func TestTerminalStatesStayTerminal(t *testing.T) {
	for _, from := range []string{models.MigrationStateDone, models.MigrationStateCancelled} {
		for _, to := range []string{
			models.MigrationStateAnalyzing,
			models.MigrationStateRestoring,
			models.MigrationStatePending,
		} {
			if IsValidJobTransition(from, to) {
				t.Errorf("%s -> %s allowed — terminal states must not restart", from, to)
			}
		}
	}
}

// The forward path must be unchanged by the resume additions.
func TestForwardPathIntact(t *testing.T) {
	for _, step := range [][2]string{
		{models.MigrationStatePending, models.MigrationStateAnalyzing},
		{models.MigrationStateAnalyzing, models.MigrationStateFixPerms},
		{models.MigrationStateFixPerms, models.MigrationStateValidating},
		{models.MigrationStateValidating, models.MigrationStateRestoring},
		{models.MigrationStateRestoring, models.MigrationStateDone},
	} {
		if !IsValidJobTransition(step[0], step[1]) {
			t.Errorf("forward step %s -> %s broken", step[0], step[1])
		}
	}
}
