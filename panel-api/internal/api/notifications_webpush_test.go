package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes ---

type fakeWPSettingsRepo struct {
	row *models.ServerSettings
}

func (f *fakeWPSettingsRepo) Get(context.Context) (*models.ServerSettings, error) {
	if f.row == nil {
		return nil, repository.ErrNotFound
	}
	return f.row, nil
}
func (f *fakeWPSettingsRepo) Upsert(context.Context, *models.ServerSettings) error { return nil }
func (f *fakeWPSettingsRepo) SetDigestLastSent(context.Context, string) error      { return nil }
func (f *fakeWPSettingsRepo) RecordDRSync(context.Context, string, string, string) error {
	return nil
}

func (f *fakeWPSettingsRepo) EnsureVAPID(context.Context, string) (bool, error) { return false, nil }

type fakeSubsRepo struct {
	mu           sync.Mutex
	byEndpoint   map[string]*models.WebPushSubscription
	upsertCount  int
	deletedCount int
}

func (f *fakeSubsRepo) Upsert(_ context.Context, sub *models.WebPushSubscription) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byEndpoint == nil {
		f.byEndpoint = map[string]*models.WebPushSubscription{}
	}
	f.upsertCount++
	if existing, ok := f.byEndpoint[sub.Endpoint]; ok {
		// Simulate the real repo's behaviour: keep the existing ID,
		// update keys/user/last_used_at.
		existing.P256dh = sub.P256dh
		existing.Auth = sub.Auth
		existing.UserID = sub.UserID
		existing.UserAgent = sub.UserAgent
		now := time.Now()
		existing.LastUsedAt = &now
		return nil
	}
	dup := *sub
	f.byEndpoint[sub.Endpoint] = &dup
	return nil
}
func (f *fakeSubsRepo) FindByID(context.Context, string) (*models.WebPushSubscription, error) {
	return nil, nil
}
func (f *fakeSubsRepo) FindByUser(context.Context, string) ([]models.WebPushSubscription, error) {
	return nil, nil
}
func (f *fakeSubsRepo) FindAll(context.Context) ([]models.WebPushSubscription, error) {
	return nil, nil
}
func (f *fakeSubsRepo) FindAllAdmins(context.Context) ([]models.WebPushSubscription, error) {
	return nil, nil
}
func (f *fakeSubsRepo) FindByEndpoint(_ context.Context, ep string) (*models.WebPushSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.byEndpoint[ep]; ok {
		out := *r
		return &out, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeSubsRepo) DeleteByEndpoint(_ context.Context, ep string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byEndpoint[ep]; !ok {
		return repository.ErrNotFound
	}
	delete(f.byEndpoint, ep)
	f.deletedCount++
	return nil
}
func (f *fakeSubsRepo) DeleteByID(context.Context, string) error    { return nil }
func (f *fakeSubsRepo) TouchLastUsed(context.Context, string) error { return nil }

// --- helpers ---

func newWebPushRouter(t *testing.T, settings *fakeWPSettingsRepo, subs *fakeSubsRepo, authFn gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	if authFn != nil {
		g.Use(authFn)
	}
	RegisterNotificationsWebPushRoutes(g, NotificationsWebPushHandlerConfig{
		ServerSettings: settings,
		Subs:           subs,
	})
	return r
}

func webpushPtr(s string) *string { return &s }

// --- tests ---

func TestWebPush_VAPIDKey_Happy(t *testing.T) {
	t.Parallel()
	settings := &fakeWPSettingsRepo{row: &models.ServerSettings{ID: 1, VAPIDPublicKey: webpushPtr("PUB")}}
	r := newWebPushRouter(t, settings, &fakeSubsRepo{}, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodGet, "/api/v1/notifications/webpush/vapid-public-key", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "PUB", body["public_key"])
}

func TestWebPush_VAPIDKey_MissingIs503(t *testing.T) {
	t.Parallel()
	settings := &fakeWPSettingsRepo{row: &models.ServerSettings{ID: 1}}
	r := newWebPushRouter(t, settings, &fakeSubsRepo{}, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodGet, "/api/v1/notifications/webpush/vapid-public-key", nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestWebPush_Subscribe_Insert(t *testing.T) {
	t.Parallel()
	subs := &fakeSubsRepo{}
	r := newWebPushRouter(t, &fakeWPSettingsRepo{}, subs, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/notifications/webpush/subscribe", map[string]any{
		"endpoint": "https://push.example/sub1",
		"keys":     map[string]string{"p256dh": "pkey", "auth": "aaaa"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Equal(t, 1, subs.upsertCount)
	require.Len(t, subs.byEndpoint, 1)
}

func TestWebPush_Subscribe_UpsertKeepsEndpoint(t *testing.T) {
	t.Parallel()
	subs := &fakeSubsRepo{byEndpoint: map[string]*models.WebPushSubscription{
		"https://push.example/sub1": {ID: "existing-id", Endpoint: "https://push.example/sub1", UserID: "u1", P256dh: "old", Auth: "old"},
	}}
	r := newWebPushRouter(t, &fakeWPSettingsRepo{}, subs, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/notifications/webpush/subscribe", map[string]any{
		"endpoint": "https://push.example/sub1",
		"keys":     map[string]string{"p256dh": "new", "auth": "new"},
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, 1, subs.upsertCount)
	// Still exactly one row — upsert, not duplicate insert.
	require.Len(t, subs.byEndpoint, 1)
	require.Equal(t, "new", subs.byEndpoint["https://push.example/sub1"].P256dh)
}

func TestWebPush_Subscribe_MissingKeysIs422(t *testing.T) {
	t.Parallel()
	r := newWebPushRouter(t, &fakeWPSettingsRepo{}, &fakeSubsRepo{}, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/notifications/webpush/subscribe", map[string]any{
		"endpoint": "https://push.example/sub1",
	})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestWebPush_Subscribe_Unauthenticated(t *testing.T) {
	t.Parallel()
	r := newWebPushRouter(t, &fakeWPSettingsRepo{}, &fakeSubsRepo{}, nil)
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/notifications/webpush/subscribe", map[string]any{
		"endpoint": "https://push.example/sub1",
		"keys":     map[string]string{"p256dh": "k", "auth": "a"},
	})
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestWebPush_Unsubscribe_Own(t *testing.T) {
	t.Parallel()
	subs := &fakeSubsRepo{byEndpoint: map[string]*models.WebPushSubscription{
		"https://push.example/sub1": {ID: "s1", Endpoint: "https://push.example/sub1", UserID: "u1"},
	}}
	r := newWebPushRouter(t, &fakeWPSettingsRepo{}, subs, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodDelete, "/api/v1/notifications/webpush/subscribe", map[string]any{
		"endpoint": "https://push.example/sub1",
	})
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, subs.deletedCount)
}

func TestWebPush_Unsubscribe_OtherUserIs403(t *testing.T) {
	t.Parallel()
	subs := &fakeSubsRepo{byEndpoint: map[string]*models.WebPushSubscription{
		"https://push.example/sub1": {ID: "s1", Endpoint: "https://push.example/sub1", UserID: "someone-else"},
	}}
	r := newWebPushRouter(t, &fakeWPSettingsRepo{}, subs, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodDelete, "/api/v1/notifications/webpush/subscribe", map[string]any{
		"endpoint": "https://push.example/sub1",
	})
	require.Equal(t, http.StatusForbidden, rec.Code)
}
