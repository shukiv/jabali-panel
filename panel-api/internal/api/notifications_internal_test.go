package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

func newTestRouter(t *testing.T, queue *notifications.Queue) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterNotificationsInternalRoutes(r.Group("/api/v1"), queue)
	return r
}

func newTestQueue(t *testing.T) (*notifications.Queue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return notifications.NewQueue(client), mr
}

func TestInternalEnqueue_Happy(t *testing.T) {
	t.Parallel()
	q, mr := newTestQueue(t)
	r := newTestRouter(t, q)

	body, _ := json.Marshal(map[string]any{
		"event_kind": "service.down",
		"severity":   "error",
		"title":      "jabali-panel.service failing",
		"body":       "status: failed",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/notifications/enqueue", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Internal routes are called by the agent over the unix socket in
	// production; "@" is what net/http sets for a unix peer. Loopback TCP is
	// no longer trusted by default (see RequireLocalhost).
	req.RemoteAddr = "@"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code, "body=%s", rec.Body.String())
	var resp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
	// Stream should have exactly one entry.
	entries, err := mr.DB(0).Stream("jabali:notifications:queue")
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestInternalEnqueue_MissingQueueReturns503(t *testing.T) {
	t.Parallel()
	r := newTestRouter(t, nil)
	body, _ := json.Marshal(map[string]any{"event_kind": "service.down", "severity": "error", "title": "t"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/notifications/enqueue", bytes.NewReader(body))
	// Internal routes are called by the agent over the unix socket in
	// production; "@" is what net/http sets for a unix peer. Loopback TCP is
	// no longer trusted by default (see RequireLocalhost).
	req.RemoteAddr = "@"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestInternalEnqueue_BadEventKind(t *testing.T) {
	t.Parallel()
	q, _ := newTestQueue(t)
	r := newTestRouter(t, q)
	body, _ := json.Marshal(map[string]any{"event_kind": "nope", "severity": "error", "title": "t"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/notifications/enqueue", bytes.NewReader(body))
	// Internal routes are called by the agent over the unix socket in
	// production; "@" is what net/http sets for a unix peer. Loopback TCP is
	// no longer trusted by default (see RequireLocalhost).
	req.RemoteAddr = "@"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestInternalEnqueue_NonLocalhostForbidden(t *testing.T) {
	t.Parallel()
	q, _ := newTestQueue(t)
	r := newTestRouter(t, q)
	body, _ := json.Marshal(map[string]any{"event_kind": "service.down", "severity": "error", "title": "t"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/notifications/enqueue", bytes.NewReader(body))
	req.RemoteAddr = "10.0.0.5:1234"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestInternalEnqueue_TitleTooLong(t *testing.T) {
	t.Parallel()
	q, _ := newTestQueue(t)
	r := newTestRouter(t, q)
	title := make([]byte, 0, 220)
	for i := 0; i < 220; i++ {
		title = append(title, 'a')
	}
	body, _ := json.Marshal(map[string]any{"event_kind": "service.down", "severity": "error", "title": string(title)})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/notifications/enqueue", bytes.NewReader(body))
	// Internal routes are called by the agent over the unix socket in
	// production; "@" is what net/http sets for a unix peer. Loopback TCP is
	// no longer trusted by default (see RequireLocalhost).
	req.RemoteAddr = "@"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
