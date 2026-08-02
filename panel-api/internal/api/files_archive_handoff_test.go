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

// JAB-192. The GH #756 folder-download fix handed the archive from the agent to
// panel-api through /tmp. panel-api runs PrivateTmp=yes and the agent does not,
// so they sit in different mount namespaces and the panel could not see a file
// the agent had just reported writing:
//
//	host /tmp    : /tmp/jabali-archive-nstest.tar.gz 14B
//	panel-api ns : ls: cannot access ... No such file or directory
//
// The staging path itself is fixed (archives now go to /var/lib/jabali-uploads).
// What remained is why that bug was so hard to see: the failure surfaced as a
// bare 500 carrying the raw os error, indistinguishable from any other internal
// fault, with nothing saying the archive had been created and then lost between
// two processes.
//
// A missing file here can only be a handoff failure — the agent reported
// success, so the archive existed. It is always a server-side problem and never
// something the caller did, and it deserves to say so.

func TestStreamArchive_MissingStagedFileIsNotAnOpaque500(t *testing.T) {
	// Agent reports success and hands back a path that does not exist — exactly
	// what the PrivateTmp namespace split produced.
	ghost := filepath.Join(t.TempDir(), "never-written.tar.gz")

	agent := &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		switch cmd {
		case "files.read":
			return nil, errors.New("agent: failed_precondition: path is a directory")
		case "files.archive":
			b, _ := json.Marshal(filesArchiveAgentResult{ArchivePath: ghost, Size: 16})
			return b, nil
		default:
			return nil, errors.New("unexpected command " + cmd)
		}
	}}
	r := setupFilesRouter(t, "user1", agent)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/files/download?path=/home/alice/mydir", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500 (it IS a server fault)", w.Code)
	}

	var body struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON body: %v (%s)", err, w.Body.String())
	}

	if body.Error != "archive_unavailable" {
		t.Errorf("error code = %q, want %q — a lost handoff must be "+
			"distinguishable from a generic internal_error, which is what made "+
			"the original bug opaque", body.Error, "archive_unavailable")
	}
	if !strings.Contains(body.Detail, "server configuration problem") {
		t.Errorf("detail should tell the caller this is not their fault: %q", body.Detail)
	}
	// The staging path is internal detail; echoing it back leaks filesystem
	// layout to any tenant who can hit download (JAB-114).
	if strings.Contains(w.Body.String(), ghost) {
		t.Errorf("response leaked the internal staging path:\n%s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "no such file or directory") {
		t.Errorf("raw os error text leaked to the client:\n%s", w.Body.String())
	}
}

// The success path must be untouched — this is the behaviour GH #756 restored.
func TestStreamArchive_SuccessPathUnchanged(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "arc.tar.gz")
	if err := os.WriteFile(scratch, []byte("FAKE-TARGZ-BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	agent := &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		switch cmd {
		case "files.read":
			return nil, errors.New("agent: failed_precondition: path is a directory")
		case "files.archive":
			b, _ := json.Marshal(filesArchiveAgentResult{ArchivePath: scratch, Size: 16})
			return b, nil
		}
		return nil, errors.New("unexpected " + cmd)
	}}
	r := setupFilesRouter(t, "user1", agent)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/files/download?path=/home/alice/mydir", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type: got %q want application/gzip", ct)
	}
}
