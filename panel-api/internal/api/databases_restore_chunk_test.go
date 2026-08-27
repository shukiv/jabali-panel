package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1323 — chunked "Restore from file" upload.

func useTempRestoreRoot(t *testing.T) {
	t.Helper()
	orig := restoreRoot
	t.Cleanup(func() { restoreRoot = orig })
	restoreRoot = t.TempDir()
}

func TestRestoreChunk_AssembleAndAsyncFinalize(t *testing.T) {
	useTempRestoreRoot(t)
	// The restore runs on a detached goroutine; the channel publishes the agent
	// call race-free (channel receive is the happens-before edge).
	gotCh := make(chan string, 1)
	agent := &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		path := ""
		if m, ok := params.(map[string]any); ok {
			path, _ = m["path"].(string)
		}
		gotCh <- cmd + "|" + path
		return json.RawMessage(`{}`), nil
	}}
	r, dbRepo := databaseRouterWithAgent("user1", false, agent)
	dbRepo.databases = []models.Database{{ID: "db1", UserID: "user1", Name: "alice_db", Engine: "mysql"}}

	c1 := []byte("CREATE TABLE t (id INT);\n")
	c2 := []byte("INSERT INTO t VALUES (1);\n")

	// Chunk 1 (not final).
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/databases/db1/restore-chunk?upload_id=up-abc&offset=0", bytes.NewReader(c1))
	req.Header.Set("Content-Type", "application/octet-stream")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// Status reflects the bytes staged so far.
	ws := httptest.NewRecorder()
	r.ServeHTTP(ws, httptest.NewRequest("GET", "/api/v1/databases/db1/restore-chunk-status?upload_id=up-abc", nil))
	require.Equal(t, http.StatusOK, ws.Code)
	var st map[string]any
	require.NoError(t, json.Unmarshal(ws.Body.Bytes(), &st))
	assert.Equal(t, float64(len(c1)), st["written"])

	// Chunk 2 (final) → 202 immediately (restore is async).
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/v1/databases/db1/restore-chunk?upload_id=up-abc&offset=25&final=1", bytes.NewReader(c2))
	req2.Header.Set("Content-Type", "application/octet-stream")
	r.ServeHTTP(w2, req2)
	require.Equal(t, http.StatusAccepted, w2.Code)
	var fin map[string]any
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &fin))
	assert.Equal(t, "restoring", fin["status"])

	// The detached restore runs the mysql load against the assembled dump.
	select {
	case got := <-gotCh:
		parts := strings.SplitN(got, "|", 2)
		assert.Equal(t, "db.restore", parts[0])
		require.Len(t, parts, 2)
		data, err := os.ReadFile(parts[1])
		require.NoError(t, err)
		assert.Equal(t, string(c1)+string(c2), string(data), "assembled dump must be chunk1+chunk2 in order")
	case <-time.After(3 * time.Second):
		t.Fatal("detached restore did not call the agent")
	}

	// restore-status transitions to done.
	var status string
	for i := 0; i < 200; i++ {
		wr := httptest.NewRecorder()
		r.ServeHTTP(wr, httptest.NewRequest("GET", "/api/v1/databases/db1/restore-status?upload_id=up-abc", nil))
		if wr.Code == http.StatusOK {
			var s map[string]any
			_ = json.Unmarshal(wr.Body.Bytes(), &s)
			if status, _ = s["status"].(string); status == "done" || status == "failed" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, "done", status)
}

func TestRestoreStatus_UnknownUpload(t *testing.T) {
	useTempRestoreRoot(t)
	r, dbRepo := databaseRouterWithAgent("user1", false, &mockAgent{})
	dbRepo.databases = []models.Database{{ID: "db1", UserID: "user1", Name: "alice_db"}}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/databases/db1/restore-status?upload_id=never", nil))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestRestoreChunk_NonOwnerForbidden(t *testing.T) {
	useTempRestoreRoot(t)
	agent := &mockAgent{}
	r, dbRepo := databaseRouterWithAgent("user2", false, agent) // caller is user2
	dbRepo.databases = []models.Database{{ID: "db1", UserID: "user1", Name: "alice_db"}}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/databases/db1/restore-chunk?upload_id=up-x&offset=0", bytes.NewReader([]byte("x")))
	req.Header.Set("Content-Type", "application/octet-stream")
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Equal(t, 0, agent.callCount, "a forbidden request must never reach the agent")
}

func TestRestoreChunk_BadOffsetConflict(t *testing.T) {
	useTempRestoreRoot(t)
	r, dbRepo := databaseRouterWithAgent("user1", false, &mockAgent{})
	dbRepo.databases = []models.Database{{ID: "db1", UserID: "user1", Name: "alice_db"}}

	// First chunk establishes 5 bytes.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/databases/db1/restore-chunk?upload_id=up-o&offset=0", bytes.NewReader([]byte("hello")))
	req.Header.Set("Content-Type", "application/octet-stream")
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	// A second chunk at the wrong offset (10, not 5) is rejected — no holes.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/v1/databases/db1/restore-chunk?upload_id=up-o&offset=10", bytes.NewReader([]byte("world")))
	req2.Header.Set("Content-Type", "application/octet-stream")
	r.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusConflict, w2.Code)
}
