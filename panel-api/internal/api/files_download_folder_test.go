package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFilesDownload_FolderFallsBackToArchive is the GH #756 regression: hitting
// "download" on a folder used to 500 (files.read can't read a directory). The
// agent now reports "path is a directory" and the handler transparently serves
// the folder as <name>.tar.gz via the archive path.
func TestFilesDownload_FolderFallsBackToArchive(t *testing.T) {
	// A real scratch tar.gz on disk for streamArchive to open + stream.
	scratch := filepath.Join(t.TempDir(), "arc.tar.gz")
	if err := os.WriteFile(scratch, []byte("FAKE-TARGZ-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}

	agent := &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		switch cmd {
		case "files.read":
			// Mirror the agent's typed directory error.
			return nil, errors.New("agent: failed_precondition: path is a directory")
		case "files.archive":
			b, _ := json.Marshal(filesArchiveAgentResult{ArchivePath: scratch, Size: 16})
			return b, nil
		default:
			return nil, errors.New("unexpected command " + cmd)
		}
	}}
	r := setupFilesRouter(t, "user1", agent)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/download?path=/home/alice/mydir", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (folder should fall back to archive, not 500)", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type: got %q want application/gzip", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="mydir.tar.gz"`) {
		t.Errorf("Content-Disposition: got %q want mydir.tar.gz", cd)
	}
	if w.Body.String() != "FAKE-TARGZ-BYTES" {
		t.Errorf("body: got %q", w.Body.String())
	}
	// scratch file must be unlinked after streaming.
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Errorf("scratch archive not cleaned up")
	}
}

// TestFilesDownload_RealFileStillWorks: a plain file download is unaffected.
func TestFilesDownload_RealFileStillWorks(t *testing.T) {
	agent := agentReply(filesReadAgentResult{Path: "/home/alice/notes.txt", Content: "hi", Size: 2})
	r := setupFilesRouter(t, "user1", agent)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/download?path=/home/alice/notes.txt", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "hi" {
		t.Fatalf("plain file download broke: code=%d body=%q", w.Code, w.Body.String())
	}
}
