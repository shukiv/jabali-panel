package commands

// GH #502: a missing OPTIONAL system database (jabali_pdns on a DNS-less
// install) must be SKIPPED, not failed — otherwise the whole system backup
// is reported Partial on an otherwise-clean fresh server.

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

func TestDumpOneSystemDB_MissingDBSkips(t *testing.T) {
	orig := systemDBExists
	t.Cleanup(func() { systemDBExists = orig })
	systemDBExists = func(_ context.Context, _ string) bool { return false }

	st := dumpOneSystemDB(context.Background(), nil, "job1", "host", "", "jabali_pdns")

	if st.Status != backup.StageStatusSkipped {
		t.Fatalf("missing DB status = %q, want %q", st.Status, backup.StageStatusSkipped)
	}
	if st.Status == backup.StageStatusFailed {
		t.Fatal("a missing optional DB must never be Failed (marks the backup Partial)")
	}
	if len(st.Warnings) == 0 {
		t.Error("skip must carry a warning naming the absent DB")
	}
	if len(st.Items) != 1 || st.Items[0] != "jabali_pdns" {
		t.Errorf("items = %v, want [jabali_pdns]", st.Items)
	}
}
