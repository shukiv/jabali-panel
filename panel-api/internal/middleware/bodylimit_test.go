package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func bodyLimitRouter(limit int64, exempt map[string]struct{}) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(limit, exempt))
	echo := func(c *gin.Context) {
		b, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "read"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"n": len(b)})
	}
	r.POST("/small", echo)
	r.POST("/big/:id/upload", echo)
	r.GET("/get", echo)
	return r
}

func TestBodyLimit_SmallBodyPasses(t *testing.T) {
	r := bodyLimitRouter(64, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/small", strings.NewReader(`{"a":1}`))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("small body: got %d, want 200 (body %s)", w.Code, w.Body.String())
	}
}

func TestBodyLimit_DeclaredOversizedIs413(t *testing.T) {
	r := bodyLimitRouter(64, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/small", strings.NewReader(strings.Repeat("x", 200)))
	// httptest.NewRequest sets ContentLength from the reader — the
	// header fast-path must reject before any body read.
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversized: got %d, want 413", w.Code)
	}
}

func TestBodyLimit_ChunkedOversizedIs413(t *testing.T) {
	r := bodyLimitRouter(64, nil)
	w := httptest.NewRecorder()
	// io.NopCloser hides the length: ContentLength stays -1 (chunked),
	// so only the buffered read can catch the overrun.
	req := httptest.NewRequest(http.MethodPost, "/small",
		io.NopCloser(strings.NewReader(strings.Repeat("x", 200))))
	if req.ContentLength != -1 {
		t.Fatalf("precondition: want unknown ContentLength, got %d", req.ContentLength)
	}
	r.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversized: got %d, want 413", w.Code)
	}
}

func TestBodyLimit_ExactLimitPasses(t *testing.T) {
	r := bodyLimitRouter(64, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/small", strings.NewReader(strings.Repeat("x", 64)))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("exact-limit body: got %d, want 200", w.Code)
	}
}

func TestBodyLimit_ExemptRouteUnlimited(t *testing.T) {
	exempt := map[string]struct{}{"POST /big/:id/upload": {}}
	r := bodyLimitRouter(64, exempt)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/big/7/upload", strings.NewReader(strings.Repeat("x", 500)))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("exempt route: got %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"n":500`) {
		t.Fatalf("exempt route body truncated: %s", w.Body.String())
	}
}

func TestBodyLimit_HandlerSeesFullBufferedBody(t *testing.T) {
	r := bodyLimitRouter(64, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/small", strings.NewReader(strings.Repeat("y", 40)))
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"n":40`) {
		t.Fatalf("buffered body not replayed to handler: %s", w.Body.String())
	}
}

func TestBodyLimit_GetIgnored(t *testing.T) {
	r := bodyLimitRouter(64, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/get", strings.NewReader(strings.Repeat("x", 500)))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET with body: got %d, want 200 (method gate)", w.Code)
	}
}
