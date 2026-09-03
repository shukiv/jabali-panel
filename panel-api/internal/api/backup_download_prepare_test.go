package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestDownloadJobPreparable(t *testing.T) {
	cases := []struct {
		name string
		job  *models.BackupJob
		code int
	}{
		{"succeeded+snapshot", &models.BackupJob{Status: models.BackupJobStatusSucceeded, SnapshotID: "s1"}, 0},
		{"partial+snapshot", &models.BackupJob{Status: models.BackupJobStatusPartial, SnapshotID: "s1"}, 0},
		{"queued", &models.BackupJob{Status: models.BackupJobStatusQueued, SnapshotID: "s1"}, http.StatusNotFound},
		{"succeeded no snapshot", &models.BackupJob{Status: models.BackupJobStatusSucceeded}, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		if code, _ := downloadJobPreparable(tc.job); code != tc.code {
			t.Errorf("%s: got %d, want %d", tc.name, code, tc.code)
		}
	}
}

func TestConsumePreparedDir(t *testing.T) {
	dir := t.TempDir()
	old := downloadPrepMarkerDir
	downloadPrepMarkerDir = dir
	t.Cleanup(func() { downloadPrepMarkerDir = old })

	write := func(jobID string, o downloadPrepOutcome) {
		b, _ := json.Marshal(o)
		if err := os.WriteFile(downloadPrepMarkerPath(jobID), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// No marker → "".
	if p := consumePreparedDir("nope"); p != "" {
		t.Errorf("no marker: got %q, want empty", p)
	}

	// Ready with a real dir → returns it AND removes the marker (one-shot).
	ready := filepath.Join(dir, "mat")
	if err := os.Mkdir(ready, 0o750); err != nil {
		t.Fatal(err)
	}
	write("j1", downloadPrepOutcome{Status: "ready", Path: ready})
	if p := consumePreparedDir("j1"); p != ready {
		t.Errorf("ready: got %q, want %q", p, ready)
	}
	if _, err := os.Stat(downloadPrepMarkerPath("j1")); !os.IsNotExist(err) {
		t.Error("marker should be removed after a successful consume")
	}
	// Second consume finds nothing.
	if p := consumePreparedDir("j1"); p != "" {
		t.Errorf("second consume: got %q, want empty", p)
	}

	// Still preparing → "" (marker kept for the next poll).
	write("j2", downloadPrepOutcome{Status: "preparing"})
	if p := consumePreparedDir("j2"); p != "" {
		t.Errorf("preparing: got %q, want empty", p)
	}
	if _, err := os.Stat(downloadPrepMarkerPath("j2")); err != nil {
		t.Error("a preparing marker must NOT be removed")
	}

	// Ready but the dir is gone → "" and the stale marker is dropped.
	write("j3", downloadPrepOutcome{Status: "ready", Path: filepath.Join(dir, "gone")})
	if p := consumePreparedDir("j3"); p != "" {
		t.Errorf("stale ready: got %q, want empty", p)
	}
	if _, err := os.Stat(downloadPrepMarkerPath("j3")); !os.IsNotExist(err) {
		t.Error("a stale ready marker should be removed")
	}
}
