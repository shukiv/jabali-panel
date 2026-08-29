package commands

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1360: docker and db fan out one ManifestStage per app / per database,
// all sharing the same Name ("docker" / "db"). The apply gate must decide
// per-stage (by index), NOT by a name-keyed map that collapses N same-named
// statuses to the last one — that dropped every good app on a single later
// failure.
func TestMaterializedStages_PerStageNotCollapsedByName(t *testing.T) {
	// Two docker apps: the first materialized fine, the second failed. The
	// old name-keyed logic recorded statusOf["docker"] = the *last* status
	// (failed) and skipped BOTH; index alignment keeps the first.
	manifest := []backup.ManifestStage{
		{Name: backup.StageHome},
		{Name: backup.StageDocker, Items: []string{"nextcloud"}},
		{Name: backup.StageDocker, Items: []string{"gitea"}},
	}
	results := []backupRestoreStage{
		{Name: backup.StageHome, Status: backup.StageStatusOK},
		{Name: backup.StageDocker, Status: backup.StageStatusOK},
		{Name: backup.StageDocker, Status: backup.StageStatusFailed},
	}
	got := materializedStages(manifest, results)
	want := []bool{true, true, false}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stage %d (%s) applicable=%v, want %v", i, manifest[i].Name, got[i], want[i])
		}
	}
}

// The reverse ordering — first docker app failed, second OK — must apply the
// second (a name-keyed map keyed on the last status would have kept both by
// luck; the point is the decision is genuinely per-index).
func TestMaterializedStages_FirstFailedSecondApplies(t *testing.T) {
	manifest := []backup.ManifestStage{
		{Name: backup.StageDocker, Items: []string{"a"}},
		{Name: backup.StageDocker, Items: []string{"b"}},
	}
	results := []backupRestoreStage{
		{Name: backup.StageDocker, Status: backup.StageStatusFailed},
		{Name: backup.StageDocker, Status: backup.StageStatusOK},
	}
	got := materializedStages(manifest, results)
	if got[0] || !got[1] {
		t.Fatalf("want [false true], got %v", got)
	}
}

// Fail closed: a short/misaligned results slice must not apply the stages it
// can't vouch for (no out-of-range read, trailing stages stay false).
func TestMaterializedStages_ShortResultsFailClosed(t *testing.T) {
	manifest := []backup.ManifestStage{
		{Name: backup.StageHome},
		{Name: backup.StageDB, Items: []string{"db1"}},
	}
	results := []backupRestoreStage{
		{Name: backup.StageHome, Status: backup.StageStatusOK},
	}
	got := materializedStages(manifest, results)
	if !got[0] || got[1] {
		t.Fatalf("want [true false] on a short results slice, got %v", got)
	}
}
