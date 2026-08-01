package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-204: the bell SSE stream must emit an initial "inbox" event with the
// unread count + latest row id on connect, and close when the client goes
// away. Change-detection over time is exercised by the fingerprint logic
// this initial event shares; the ticker path just re-runs it.
func TestInboxStream_InitialEventAndClientClose(t *testing.T) {
	t.Parallel()
	repo := &fakeHistoryRepo{rows: map[string]*models.NotificationHistory{
		"n1": {ID: "n1", UserID: strPtrForTest("u1"), EventKind: "x", Title: "unread one"},
		"n2": {ID: "n2", UserID: strPtrForTest("u2"), EventKind: "x", Title: "not mine"},
	}}
	r := newInboxRouter(t, repo, newUserCtx())

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/notifications/inbox/stream", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
		// handler returned once the client context expired — correct.
	case <-time.After(5 * time.Second):
		t.Fatal("stream handler did not return after client context cancellation")
	}

	require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	require.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	body := rec.Body.String()
	require.Contains(t, body, "event: inbox")
	// u1 has exactly one unread row (n1); n2 belongs to someone else.
	require.Contains(t, body, `"unread":1`)
	require.Contains(t, body, `"latest_id":"n1"`)
}

func TestInboxStream_Unauthenticated(t *testing.T) {
	t.Parallel()
	repo := &fakeHistoryRepo{rows: map[string]*models.NotificationHistory{}}
	r := newInboxRouter(t, repo, nil) // no claims middleware
	rec := doNotifJSON(t, r, http.MethodGet, "/api/v1/notifications/inbox/stream", nil)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.NotContains(t, rec.Body.String(), "event: inbox")
}

func TestInboxStream_ContentTypeNotSetBeforeAuth(t *testing.T) {
	t.Parallel()
	// A 401 must be a JSON error, not a half-opened event stream.
	repo := &fakeHistoryRepo{rows: map[string]*models.NotificationHistory{}}
	r := newInboxRouter(t, repo, nil)
	rec := doNotifJSON(t, r, http.MethodGet, "/api/v1/notifications/inbox/stream", nil)
	require.False(t, strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream"))
}
