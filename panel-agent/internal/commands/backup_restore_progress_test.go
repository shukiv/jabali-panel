package commands

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

// GH #1993: a restore from an uploaded file reports what it is doing, so the
// panel can show the admin progress by stage.

const progressJobID = "01K0000000000000000000PRG1"

// Each stage is reported while it runs: the database a CREATE DATABASE is for
// is the item backup.restore_progress names at that moment.
func TestApplyAccountRestore_ReportsEachStageWhileItRuns(t *testing.T) {
	me := currentUsername(t)
	p := startRestoreProgress(progressJobID, 0)
	seen := map[string]restoreProgressSnapshot{}
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		line := name + " " + strings.Join(args, " ")
		if i := strings.Index(line, "CREATE DATABASE `"); i >= 0 {
			rest := line[i+len("CREATE DATABASE `"):]
			seen[rest[:strings.Index(rest, "`")]] = p.snapshot(progressJobID)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	root := t.TempDir()
	stages := []backup.ManifestStage{
		{Name: backup.StageDB, Items: []string{me + "_one"}},
		{Name: backup.StageDB, Items: []string{me + "_two"}},
	}
	results := stageUpload(t, root, stages)
	enf := restoreEnforcement{Mode: restoreModeUpload, DBPrefix: me + "_"}
	ctx := withRestoreProgress(context.Background(), p)

	_, warnings := applyAccountRestore(ctx, root, me, backup.ManifestUser{Username: me}, stages, results, enf)

	for i, db := range []string{me + "_one", me + "_two"} {
		got, ok := seen[db]
		want := restoreProgressSnapshot{JobID: progressJobID, Phase: restorePhaseApplying, Stage: backup.StageDB, Item: db, Index: i + 1, Count: 2}
		if !ok || got != want {
			t.Errorf("while creating %s progress was %+v, want %+v (warnings %v)", db, got, want, warnings)
		}
	}
}

// Unpacking counts the archive bytes zstd has read, up to the archive's size.
func TestSafeExtractZstdTarProgress_CountsTheArchive(t *testing.T) {
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Skip("zstd not installed")
	}
	withRealExec(t) // zstd -dc only reads the archive; the extractor writes under t.TempDir()
	dir := t.TempDir()
	plain := filepath.Join(dir, "a.tar")
	body := strings.Repeat("jabali ", 20000)
	if err := os.WriteFile(plain, buildTar(t, []tentry{{typ: tar.TypeDir, name: "job"}, {typ: tar.TypeReg, name: "job/f.txt", body: body}}).Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("zstd", "-q", plain, "-o", plain+".zst").CombinedOutput(); err != nil {
		t.Fatalf("zstd: %v %s", err, out)
	}
	fi, err := os.Stat(plain + ".zst")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out")
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	var read int64
	if _, err := safeExtractZstdTarProgress(context.Background(), plain+".zst", dest, func(n int64) { read += n }); err != nil {
		t.Fatal(err)
	}
	if read != fi.Size() {
		t.Errorf("counted %d bytes read, want the archive's %d", read, fi.Size())
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "job", "f.txt")); string(got) != body {
		t.Errorf("extracted %d bytes, want %d", len(got), len(body))
	}
}

func TestBackupRestoreProgressVerb(t *testing.T) {
	p := startRestoreProgress(progressJobID, 1000)
	p.addUnpacked(400)
	call := func(id string) (restoreProgressSnapshot, error) {
		raw, _ := json.Marshal(map[string]string{"job_id": id})
		out, err := backupRestoreProgressHandler(context.Background(), raw)
		if err != nil {
			return restoreProgressSnapshot{}, err
		}
		return out.(restoreProgressSnapshot), nil
	}

	got, err := call(progressJobID)
	if err != nil || got.Phase != restorePhaseUnpacking || got.BytesDone != 400 || got.BytesTotal != 1000 {
		t.Fatalf("unpacking: %+v %v", got, err)
	}
	p.applying(backup.StageHome, "", 1, 3)
	if got, _ = call(progressJobID); got.Phase != restorePhaseApplying || got.Stage != backup.StageHome || got.Index != 1 || got.Count != 3 {
		t.Errorf("applying: %+v", got)
	}
	p.finish()
	if got, _ = call(progressJobID); got.Phase != restorePhaseDone {
		t.Errorf("finished: %+v", got)
	}

	var ae *agentwire.AgentError
	if _, err := call("01K0000000000000000000ZZZ9"); !errors.As(err, &ae) || ae.Code != agentwire.CodeNotFound {
		t.Errorf("unknown job: %v, want not_found", err)
	}
	if _, err := call("../x"); err == nil {
		t.Error("a job_id that is not a ULID was accepted")
	}

	// A finished restore's progress goes once it is old.
	p.mu.Lock()
	p.finishedAt = time.Now().Add(-restoreProgressTTL - time.Minute)
	p.mu.Unlock()
	startRestoreProgress("01K0000000000000000000PRG2", 0)
	if lookupRestoreProgress(progressJobID) != nil {
		t.Error("an old finished restore's progress was kept")
	}
}
