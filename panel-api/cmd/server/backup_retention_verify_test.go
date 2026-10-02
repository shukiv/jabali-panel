package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func manifestBody(stages ...internalbackup.ManifestStage) string {
	b, _ := json.Marshal(map[string]any{"schema_version": 1, "kind": "account_backup", "stages": stages})
	return string(b)
}

func okStage(name, id string) internalbackup.ManifestStage {
	return internalbackup.ManifestStage{Name: name, Status: internalbackup.StageStatusOK, SnapshotID: id}
}

func fullID(c byte) string { return strings.Repeat(string(c), 64) }

func TestVerifyDestination_ReportsBrokenNoDataAndUnreadable(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC) }
	tags := func(job, stage string) []string {
		return []string{"kind=account_backup", "job-id=" + job, "user-id=u1", "stage=" + stage}
	}
	snaps := []resticSnapshot{
		{ID: fullID('a'), Time: at(1), Hostname: "box", Tags: tags("A", "home")},
		{ID: fullID('b'), Time: at(1), Hostname: "box", Tags: tags("A", "manifest"), Paths: []string{"/manifest.json"}},
		{ID: fullID('c'), Time: at(2), Hostname: "box", Tags: tags("B", "manifest"), Paths: []string{"/manifest.json"}},
		{ID: fullID('d'), Time: at(3), Hostname: "box", Tags: tags("C", "manifest"), Paths: []string{"/manifest.json"}},
		{ID: fullID('e'), Time: at(4), Hostname: "box", Tags: tags("D", "manifest"), Paths: []string{"/manifest.json"}},
		{ID: fullID('f'), Time: at(5), Hostname: "box", Tags: []string{"stage=home"}}, // no job-id: not a backup set
	}
	manifests := map[string]string{
		// A: whole (its home is present; the abbreviated meta ID matches too).
		fullID('b'): manifestBody(okStage("home", fullID('a')), okStage("meta", fullID('f')[:8]),
			internalbackup.ManifestStage{Name: "mail", Status: internalbackup.StageStatusSkipped}),
		// B: its home snapshot was forgotten.
		fullID('c'): manifestBody(okStage("home", fullID('9')), okStage("meta", fullID('a'))),
		// C: every stage failed, so nothing to restore.
		fullID('d'): manifestBody(internalbackup.ManifestStage{Name: "home", Status: internalbackup.StageStatusFailed}),
	}
	orig := retentionExec
	t.Cleanup(func() { retentionExec = orig })
	var calls [][]string
	body, _ := json.Marshal(snaps)
	retentionExec = func(_ context.Context, _ []string, stdout, stderr io.Writer, _ string, args ...string) error {
		calls = append(calls, args)
		switch {
		case hasArg(args, "snapshots"):
			_, err := stdout.Write(body)
			return err
		case hasArg(args, "dump"):
			id := args[len(args)-2]
			m, ok := manifests[id]
			if !ok {
				fmt.Fprint(stderr, "Fatal: pack not found")
				return errors.New("exit status 1")
			}
			_, err := io.WriteString(stdout, m)
			return err
		}
		return fmt.Errorf("unexpected restic call %v", args)
	}

	rep := verifyDestination(context.Background(), testDest())
	if rep.Error != "" {
		t.Fatal(rep.Error)
	}
	if rep.Checked != 4 {
		t.Errorf("checked = %d, want 4 manifests", rep.Checked)
	}
	var broken []string
	for _, b := range rep.Broken {
		broken = append(broken, b.JobID)
	}
	if !eq(broken, []string{"B", "C"}) {
		t.Fatalf("broken = %v, want [B C]", broken)
	}
	if b := rep.Broken[0]; len(b.Missing) != 1 || b.Missing[0].Stage != "home" || b.Missing[0].SnapshotID != fullID('9') {
		t.Errorf("B missing = %+v, want only its home", b.Missing)
	}
	if !rep.Broken[1].NoData {
		t.Errorf("C should be reported as having no data: %+v", rep.Broken[1])
	}
	if len(rep.Unreadable) != 1 || rep.Unreadable[0].JobID != "D" || !strings.Contains(rep.Unreadable[0].Error, "pack not found") {
		t.Errorf("unreadable = %+v, want D with restic's error", rep.Unreadable)
	}
	for _, c := range calls {
		if !hasArg(c, "--no-lock") {
			t.Errorf("verify must read without a lock: %v", c)
		}
		for _, banned := range []string{"forget", "prune", "unlock"} {
			if hasArg(c, banned) {
				t.Errorf("verify must never run %s: %v", banned, c)
			}
		}
	}
}

func TestReportVerify_ExitsNonZeroOnlyWhenSomethingIsWrong(t *testing.T) {
	ok := []verifyDestReport{{DestinationID: "d1", DestinationName: "n", Checked: 3}}
	if err := reportVerify(newRetentionTestCmd(), ok); err != nil {
		t.Errorf("a clean destination must exit zero: %v", err)
	}
	for name, r := range map[string]verifyDestReport{
		"broken":      {Checked: 1, Broken: []verifyBackup{{JobID: "B", Missing: []verifyMissing{{Stage: "home", SnapshotID: "x"}}}}},
		"unreadable":  {Checked: 1, Unreadable: []verifyBackup{{JobID: "D", Error: "boom"}}},
		"not checked": {Error: "wrong password"},
	} {
		cmd := newRetentionTestCmd()
		if err := reportVerify(cmd, []verifyDestReport{r}); err == nil {
			t.Errorf("%s: want a non-zero exit", name)
		}
	}
}

// End to end on a real repository: a backup whose home snapshot was forgotten
// (what the old per-stage sweep could do) is reported; a whole one is not.
func TestVerifyDestination_RealRestic(t *testing.T) {
	r := newTestRepo(t)
	homeID := func(job string) string {
		for _, s := range r.snapshots(t) {
			if tagValue(s.Tags, "job-id") == job && tagValue(s.Tags, "stage") == "home" {
				return s.ID
			}
		}
		t.Fatalf("no home snapshot for %s", job)
		return ""
	}
	for _, j := range []struct{ id, at string }{{"W1", "2026-01-01 10:00:00"}, {"P1", "2026-01-02 10:00:00"}} {
		r.backupStage(t, j.id, "home", j.at)
		r.run(t, manifestBody(okStage("home", homeID(j.id))),
			"backup", "--host", "box", "--time", j.at,
			"--tag", "kind=account_backup", "--tag", "job-id="+j.id, "--tag", "user-id=u1", "--tag", "stage=manifest",
			"--stdin", "--stdin-filename", "manifest.json")
	}
	r.run(t, "", "forget", homeID("P1"))

	rep := verifyDestination(context.Background(), resticRepo{&models.BackupDestination{ID: "d1", Name: "local", URL: r.dir}, r.pw})
	if rep.Error != "" {
		t.Fatal(rep.Error)
	}
	if rep.Checked != 2 || len(rep.Unreadable) != 0 {
		t.Fatalf("checked=%d unreadable=%+v, want 2 and none", rep.Checked, rep.Unreadable)
	}
	if len(rep.Broken) != 1 || rep.Broken[0].JobID != "P1" || rep.Broken[0].Missing[0].Stage != "home" {
		t.Errorf("broken = %+v, want only P1 missing its home", rep.Broken)
	}
}
