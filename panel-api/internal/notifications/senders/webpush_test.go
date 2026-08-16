package senders

import (
	"time"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// --- fakes ---

type fakeSettingsRepo struct {
	row *models.ServerSettings
	err error
}

func (f *fakeSettingsRepo) Get(ctx context.Context) (*models.ServerSettings, error) {
	return f.row, f.err
}
func (f *fakeSettingsRepo) Upsert(ctx context.Context, s *models.ServerSettings) error { return nil }
func (f *fakeSettingsRepo) SetDigestLastSent(context.Context, string) error            { return nil }
func (f *fakeSettingsRepo) RecordDRSync(context.Context, string, string, string) error { return nil }

func (f *fakeSettingsRepo) EnsureVAPID(ctx context.Context, hostname string) (bool, error) {
	return false, nil
}

func (f *fakeSettingsRepo) ReassertDRPairing(context.Context, string, string, *time.Time) error {
	return nil
}

type fakeSubs struct {
	mu      sync.Mutex
	byUser  map[string][]models.WebPushSubscription
	deleted []string
	touched []string
}

func (f *fakeSubs) Upsert(ctx context.Context, sub *models.WebPushSubscription) error { return nil }
func (f *fakeSubs) FindByID(ctx context.Context, id string) (*models.WebPushSubscription, error) {
	return nil, nil
}
func (f *fakeSubs) FindByUser(ctx context.Context, userID string) ([]models.WebPushSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byUser[userID], nil
}
func (f *fakeSubs) FindAll(ctx context.Context) ([]models.WebPushSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []models.WebPushSubscription{}
	for _, rows := range f.byUser {
		out = append(out, rows...)
	}
	return out, nil
}

func (f *fakeSubs) FindAllAdmins(ctx context.Context) ([]models.WebPushSubscription, error) {
	return f.FindAll(ctx)
}
func (f *fakeSubs) FindByEndpoint(ctx context.Context, endpoint string) (*models.WebPushSubscription, error) {
	return nil, nil
}
func (f *fakeSubs) DeleteByEndpoint(ctx context.Context, endpoint string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, endpoint)
	return nil
}
func (f *fakeSubs) DeleteByID(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *fakeSubs) TouchLastUsed(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, id)
	return nil
}

// --- helpers ---

func strPtr(s string) *string { return &s }

func newWithFakes(t *testing.T, row *models.ServerSettings, byUser map[string][]models.WebPushSubscription, send webpushSendFunc) (*WebPush, *fakeSubs) {
	t.Helper()
	subs := &fakeSubs{byUser: byUser}
	w := NewWebPush(&fakeSettingsRepo{row: row}, subs, slog.New(slog.DiscardHandler)).withSender(send)
	return w, subs
}

func cannedResp(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}}
}

// --- tests ---

func TestWebPush_BroadcastFansOutToEverySub(t *testing.T) {
	t.Parallel()
	row := &models.ServerSettings{ID: 1, VAPIDPublicKey: strPtr("pub"), VAPIDPrivateKey: strPtr("priv"), VAPIDSubject: strPtr("mailto:a@b")}
	byUser := map[string][]models.WebPushSubscription{
		"u1": {{ID: "s1", Endpoint: "https://push.example/sub1", Auth: "a", P256dh: "p"}},
		"u2": {{ID: "s2", Endpoint: "https://push.example/sub2", Auth: "a", P256dh: "p"}},
	}
	var sent int
	w, _ := newWithFakes(t, row, byUser, func(ctx context.Context, msg []byte, s *webpush.Subscription, o *webpush.Options) (*http.Response, error) {
		sent++
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody}, nil
	})
	// Empty UserID = system-wide envelope. Both u1's and u2's browsers
	// should be pushed.
	require.NoError(t, w.Send(context.Background(), models.NotificationChannel{Kind: "webpush"}, notifications.Envelope{Title: "x"}))
	require.Equal(t, 2, sent)
}

func TestWebPush_BroadcastNoSubsIsNoOp(t *testing.T) {
	t.Parallel()
	row := &models.ServerSettings{ID: 1, VAPIDPublicKey: strPtr("pub"), VAPIDPrivateKey: strPtr("priv"), VAPIDSubject: strPtr("mailto:a@b")}
	w, _ := newWithFakes(t, row, map[string][]models.WebPushSubscription{}, func(ctx context.Context, msg []byte, s *webpush.Subscription, o *webpush.Options) (*http.Response, error) {
		t.Fatal("send should not be called")
		return nil, nil
	})
	require.NoError(t, w.Send(context.Background(), models.NotificationChannel{Kind: "webpush"}, notifications.Envelope{Title: "x"}))
}

func TestWebPush_MissingVAPIDIsPermanent(t *testing.T) {
	t.Parallel()
	w, _ := newWithFakes(t, &models.ServerSettings{ID: 1}, nil, func(ctx context.Context, msg []byte, s *webpush.Subscription, o *webpush.Options) (*http.Response, error) {
		t.Fatal("send should not be called")
		return nil, nil
	})
	err := w.Send(context.Background(), models.NotificationChannel{Kind: "webpush"}, notifications.Envelope{Title: "x", UserID: "u1"})
	require.True(t, errors.Is(err, notifications.ErrPermanent))
}

func TestWebPush_NoSubsIsNoOp(t *testing.T) {
	t.Parallel()
	row := &models.ServerSettings{ID: 1, VAPIDPublicKey: strPtr("pub"), VAPIDPrivateKey: strPtr("priv"), VAPIDSubject: strPtr("mailto:a@b")}
	w, _ := newWithFakes(t, row, map[string][]models.WebPushSubscription{}, func(ctx context.Context, msg []byte, s *webpush.Subscription, o *webpush.Options) (*http.Response, error) {
		t.Fatal("send should not be called")
		return nil, nil
	})
	require.NoError(t, w.Send(context.Background(), models.NotificationChannel{Kind: "webpush"}, notifications.Envelope{Title: "x", UserID: "u1"}))
}

func TestWebPush_SuccessTouches(t *testing.T) {
	t.Parallel()
	row := &models.ServerSettings{ID: 1, VAPIDPublicKey: strPtr("pub"), VAPIDPrivateKey: strPtr("priv"), VAPIDSubject: strPtr("mailto:a@b")}
	byUser := map[string][]models.WebPushSubscription{
		"u1": {{ID: "s1", Endpoint: "https://push.example/sub1", Auth: "a", P256dh: "p"}},
	}
	w, subs := newWithFakes(t, row, byUser, func(ctx context.Context, msg []byte, s *webpush.Subscription, o *webpush.Options) (*http.Response, error) {
		return cannedResp(http.StatusCreated), nil
	})
	require.NoError(t, w.Send(context.Background(), models.NotificationChannel{Kind: "webpush"}, notifications.Envelope{Title: "x", UserID: "u1"}))
	require.Equal(t, []string{"s1"}, subs.touched)
}

func TestWebPush_410DeletesSub(t *testing.T) {
	t.Parallel()
	row := &models.ServerSettings{ID: 1, VAPIDPublicKey: strPtr("pub"), VAPIDPrivateKey: strPtr("priv"), VAPIDSubject: strPtr("mailto:a@b")}
	byUser := map[string][]models.WebPushSubscription{
		"u1": {
			{ID: "s1", Endpoint: "https://push.example/sub1", Auth: "a", P256dh: "p"},
			{ID: "s2", Endpoint: "https://push.example/sub2", Auth: "a", P256dh: "p"},
		},
	}
	w, subs := newWithFakes(t, row, byUser, func(ctx context.Context, msg []byte, s *webpush.Subscription, o *webpush.Options) (*http.Response, error) {
		if s.Endpoint == "https://push.example/sub1" {
			return cannedResp(http.StatusGone), nil
		}
		return cannedResp(http.StatusCreated), nil
	})
	require.NoError(t, w.Send(context.Background(), models.NotificationChannel{Kind: "webpush"}, notifications.Envelope{Title: "x", UserID: "u1"}))
	require.Equal(t, []string{"https://push.example/sub1"}, subs.deleted)
	require.Equal(t, []string{"s2"}, subs.touched)
}

func TestWebPush_5xxReturnsTransient(t *testing.T) {
	t.Parallel()
	row := &models.ServerSettings{ID: 1, VAPIDPublicKey: strPtr("pub"), VAPIDPrivateKey: strPtr("priv"), VAPIDSubject: strPtr("mailto:a@b")}
	byUser := map[string][]models.WebPushSubscription{
		"u1": {{ID: "s1", Endpoint: "https://push.example/sub1", Auth: "a", P256dh: "p"}},
	}
	w, _ := newWithFakes(t, row, byUser, func(ctx context.Context, msg []byte, s *webpush.Subscription, o *webpush.Options) (*http.Response, error) {
		return cannedResp(http.StatusBadGateway), nil
	})
	err := w.Send(context.Background(), models.NotificationChannel{Kind: "webpush"}, notifications.Envelope{Title: "x", UserID: "u1"})
	require.Error(t, err)
	require.False(t, errors.Is(err, notifications.ErrPermanent))
}

// One dead/slow sub must NOT fail the whole envelope when another delivered —
// otherwise a single stale browser floods the DLQ (webpush resilience).
func TestWebPush_PartialSuccessNotDLQ(t *testing.T) {
	t.Parallel()
	row := &models.ServerSettings{ID: 1, VAPIDPublicKey: strPtr("pub"), VAPIDPrivateKey: strPtr("priv"), VAPIDSubject: strPtr("mailto:a@b")}
	byUser := map[string][]models.WebPushSubscription{
		"u1": {
			{ID: "s1", Endpoint: "https://push.example/dead", Auth: "a", P256dh: "p"},
			{ID: "s2", Endpoint: "https://push.example/live", Auth: "a", P256dh: "p"},
		},
	}
	w, subs := newWithFakes(t, row, byUser, func(ctx context.Context, msg []byte, s *webpush.Subscription, o *webpush.Options) (*http.Response, error) {
		if s.Endpoint == "https://push.example/dead" {
			return cannedResp(http.StatusBadGateway), nil // transient on a dead sub
		}
		return cannedResp(http.StatusCreated), nil
	})
	// delivered>0 → success, no DLQ; the transient sub is left for next time.
	require.NoError(t, w.Send(context.Background(), models.NotificationChannel{Kind: "webpush"}, notifications.Envelope{Title: "x", UserID: "u1"}))
	require.Equal(t, []string{"s2"}, subs.touched)
	require.Empty(t, subs.deleted)
}

// A 400 Bad Request endpoint is pruned (invalid keys/endpoint), and with no
// other subs the envelope succeeds (nothing left to deliver to) — no DLQ.
func TestWebPush_400PrunesAndSucceeds(t *testing.T) {
	t.Parallel()
	row := &models.ServerSettings{ID: 1, VAPIDPublicKey: strPtr("pub"), VAPIDPrivateKey: strPtr("priv"), VAPIDSubject: strPtr("mailto:a@b")}
	byUser := map[string][]models.WebPushSubscription{
		"u1": {{ID: "s1", Endpoint: "https://push.example/bad", Auth: "a", P256dh: "p"}},
	}
	w, subs := newWithFakes(t, row, byUser, func(ctx context.Context, msg []byte, s *webpush.Subscription, o *webpush.Options) (*http.Response, error) {
		return cannedResp(http.StatusBadRequest), nil
	})
	require.NoError(t, w.Send(context.Background(), models.NotificationChannel{Kind: "webpush"}, notifications.Envelope{Title: "x", UserID: "u1"}))
	require.Equal(t, []string{"https://push.example/bad"}, subs.deleted)
}
