package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

const testUploadID = "0f8fad5b-d9cb-469f-a165-70867728950e"

// restoreChunkCase runs the admin and the tenant chunk handler against a
// fresh staging directory.
func restoreChunkCase(t *testing.T, run func(t *testing.T, post func(offset int64, body string) *httptest.ResponseRecorder, path string)) {
	for _, side := range []string{"admin", "tenant"} {
		t.Run(side, func(t *testing.T) {
			restoreUploadDir = t.TempDir()
			t.Cleanup(func() { restoreUploadDir = "/var/lib/jabali-uploads" })
			gin.SetMode(gin.TestMode)
			path := restoreUploadPath("u1", testUploadID)
			if side == "tenant" {
				path = restoreUploadTenantPath("u1", testUploadID)
			}
			post := func(offset int64, body string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodPost,
					"/x?upload_id="+testUploadID+"&offset="+strconv.FormatInt(offset, 10), bytes.NewReader([]byte(body)))
				ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
				if side == "admin" {
					(&backupHandler{}).restoreUploadChunk(c)
				} else {
					(&meBackupHandler{}).restoreUploadChunk(c)
				}
				return w
			}
			run(t, post, path)
		})
	}
}

func readStaged(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRestoreUploadChunk_InOrderAssembles(t *testing.T) {
	restoreChunkCase(t, func(t *testing.T, post func(int64, string) *httptest.ResponseRecorder, path string) {
		if w := post(0, "hello "); w.Code != http.StatusOK {
			t.Fatalf("chunk 0: %d %s", w.Code, w.Body.String())
		}
		if w := post(6, "world"); w.Code != http.StatusOK {
			t.Fatalf("chunk 1: %d %s", w.Code, w.Body.String())
		}
		if got := readStaged(t, path); got != "hello world" {
			t.Fatalf("staged %q", got)
		}
	})
}

// A chunk past the end would leave a hole in the archive.
func TestRestoreUploadChunk_GapRefused(t *testing.T) {
	restoreChunkCase(t, func(t *testing.T, post func(int64, string) *httptest.ResponseRecorder, path string) {
		post(0, "hello ")
		w := post(100, "world")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"bad_offset"`) || !strings.Contains(w.Body.String(), `"expected":6`) {
			t.Fatalf("want 409 bad_offset expected 6, got %d %s", w.Code, w.Body.String())
		}
		if got := readStaged(t, path); got != "hello " {
			t.Fatalf("staged %q", got)
		}
	})
}

// Starting over at 0 on top of a longer staged file would keep its tail.
func TestRestoreUploadChunk_RestartOverStagedRefused(t *testing.T) {
	restoreChunkCase(t, func(t *testing.T, post func(int64, string) *httptest.ResponseRecorder, path string) {
		post(0, "a long first archive")
		w := post(0, "short")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"bad_offset"`) {
			t.Fatalf("want 409 bad_offset, got %d %s", w.Code, w.Body.String())
		}
		if got := readStaged(t, path); got != "a long first archive" {
			t.Fatalf("staged %q", got)
		}
	})
}

// A later chunk with nothing staged is not an upload to continue.
func TestRestoreUploadChunk_NoStagedFileRefused(t *testing.T) {
	restoreChunkCase(t, func(t *testing.T, post func(int64, string) *httptest.ResponseRecorder, path string) {
		w := post(6, "world")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "upload_not_found") {
			t.Fatalf("want 409 upload_not_found, got %d %s", w.Code, w.Body.String())
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("a file was staged: %v", err)
		}
	})
}
