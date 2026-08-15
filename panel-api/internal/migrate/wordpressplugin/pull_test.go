package wordpressplugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
)

// JAB-240: a manifest declaring more bytes than the job budget must fail
// the pull outright — never fetch-what-fits and zero-pad the declared
// remainder (padding writes attacker-declared bytes unbudgeted).
func TestPullFilesByFile_ManifestPastBudgetFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(restPath+"/files-manifest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"root": "/src",
			"files": []map[string]any{
				{"path": "small.txt", "size": 10},
				// Declared huge; the server would only ever send a few bytes.
				{"path": "huge.bin", "size": int64(1) << 40},
			},
		})
	})
	mux.HandleFunc(restPath+"/file", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "tok", true)
	c.hc = srv.Client()                         // SafeHTTPClient rejects loopback; the guard under test is the budget
	c.SetBudget(hostreserve.NewBudget(1 << 20)) // 1 MiB job budget

	dst := filepath.Join(t.TempDir(), "files.tar.gz")
	err := c.pullFilesByFile(t.Context(), dst, 0)
	if err == nil {
		t.Fatal("petabyte manifest entry passed a 1 MiB budget")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("wrong error: %v", err)
	}
	// The staging file must not have ballooned.
	if fi, statErr := os.Stat(dst); statErr == nil && fi.Size() > 1<<20 {
		t.Fatalf("staging file grew to %d bytes despite the failed pull", fi.Size())
	}
}

// JAB-240: entries within budget still assemble normally (guard doesn't
// break the legitimate path).
func TestPullFilesByFile_WithinBudgetSucceeds(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(restPath+"/files-manifest", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"root":  "/src",
			"files": []map[string]any{{"path": "a.txt", "size": 10}},
		})
	})
	mux.HandleFunc(restPath+"/file", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0123456789"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "tok", true)
	c.hc = srv.Client()
	c.SetBudget(hostreserve.NewBudget(1 << 20))
	dst := filepath.Join(t.TempDir(), "files.tar.gz")
	if err := c.pullFilesByFile(t.Context(), dst, 0); err != nil {
		t.Fatalf("legitimate pull failed under budget: %v", err)
	}
	fi, err := os.Stat(dst)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("no tarball assembled: %v", err)
	}
}
