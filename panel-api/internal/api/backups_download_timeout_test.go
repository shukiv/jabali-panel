package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// A streaming download whose duration exceeds the server WriteTimeout must run
// to completion once the handler clears its per-request write deadline via
// http.ResponseController — the fix for GH #462 (backup download stalled at
// ~1.15 GB = 30s of throughput against the 30s WriteTimeout). This exercises the
// exact mechanism streamBackupArtifact uses, through gin's ResponseWriter.
func TestStreamingDefeatsWriteTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Total stream time (~500ms) comfortably exceeds the 150ms WriteTimeout.
	const chunks = 5
	const chunk = "chunkchunkchunk" // 15 bytes
	stream := func(clearDeadline bool) func(c *gin.Context) {
		return func(c *gin.Context) {
			if clearDeadline {
				_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Time{})
			}
			c.Status(http.StatusOK)
			fl, _ := c.Writer.(http.Flusher)
			for i := 0; i < chunks; i++ {
				_, _ = c.Writer.Write([]byte(chunk))
				if fl != nil {
					fl.Flush()
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	run := func(clearDeadline bool) (int, bool) {
		r := gin.New()
		r.GET("/dl", stream(clearDeadline))
		srv := httptest.NewUnstartedServer(r)
		srv.Config.WriteTimeout = 150 * time.Millisecond
		srv.Start()
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/dl")
		if err != nil {
			return 0, false // connection torn down before/at headers
		}
		defer resp.Body.Close()
		b, rerr := io.ReadAll(resp.Body)
		return len(b), rerr == nil
	}

	// With the deadline cleared: the whole body arrives.
	if n, ok := run(true); !ok || n != chunks*len(chunk) {
		t.Fatalf("with deadline cleared: got %d bytes ok=%v, want %d bytes complete", n, ok, chunks*len(chunk))
	}

	// Sanity: without the clear, the 150ms WriteTimeout truncates the stream
	// (fewer bytes or a read error) — confirming the timeout is the real cutter.
	if n, ok := run(false); ok && n == chunks*len(chunk) {
		t.Fatalf("without the deadline clear the stream unexpectedly completed (%d bytes) — the WriteTimeout should have cut it", n)
	}
}
