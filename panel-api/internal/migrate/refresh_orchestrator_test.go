package migrate

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type recordingAgent struct {
	calls  []string
	failOn string
}

func (r *recordingAgent) Call(_ context.Context, command string, _ any) (json.RawMessage, error) {
	r.calls = append(r.calls, command)
	if command == r.failOn {
		return nil, errFake
	}
	if command == "migration.refresh_backup" {
		return json.RawMessage(`{"snapshot":"/snap","db_dump":"/dump.sql"}`), nil
	}
	return json.RawMessage(`{"ok":true}`), nil
}

var errFake = fakeErr("boom")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

func TestRunRefresh_BackupFirstOrder(t *testing.T) {
	a := &recordingAgent{}
	in := RefreshInput{Docroot: "/home/u/d/public_html", DBName: "db", OSUser: "u", Domain: "x.test", SrcDocroot: "/src", SrcSQL: "/src.sql"}
	res, err := RunRefresh(context.Background(), a, in, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Snapshot != "/snap" {
		t.Fatalf("snapshot not captured: %+v", res)
	}
	// backup MUST be first; writers only after.
	want := []string{"migration.refresh_backup", "migration.refresh_files", "migration.refresh_db", "migration.refresh_reconcile", "nginx.cache.purge"}
	if len(a.calls) != len(want) {
		t.Fatalf("calls=%v want=%v", a.calls, want)
	}
	for i := range want {
		if a.calls[i] != want[i] {
			t.Fatalf("order wrong at %d: got %s want %s (%v)", i, a.calls[i], want[i], a.calls)
		}
	}
}

func TestRunRefresh_BackupFailAbortsBeforeWriters(t *testing.T) {
	a := &recordingAgent{failOn: "migration.refresh_backup"}
	in := RefreshInput{Docroot: "/home/u/d/public_html", DBName: "db", OSUser: "u", SrcDocroot: "/src", SrcSQL: "/src.sql"}
	if _, err := RunRefresh(context.Background(), a, in, time.Unix(0, 0).UTC()); err == nil {
		t.Fatal("expected abort on backup failure")
	}
	// NO writer ran after the backup failed — the dest is untouched.
	if len(a.calls) != 1 || a.calls[0] != "migration.refresh_backup" {
		t.Fatalf("writers ran after backup failure: %v", a.calls)
	}
}
