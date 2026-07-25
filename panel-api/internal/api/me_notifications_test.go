package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fake route repo ---

type fakeUserRoutesRepo struct {
	mu   sync.Mutex
	rows map[string]*models.UserNotificationRoute
}

func (f *fakeUserRoutesRepo) Create(_ context.Context, r *models.UserNotificationRoute) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[string]*models.UserNotificationRoute{}
	}
	// Emulate UNIQUE(user_id,event_kind,channel_id) with a driver-shaped error
	// so the handler's 409 mapping (isDuplicateKeyErr) is exercised realistically.
	for _, e := range f.rows {
		if e.UserID == r.UserID && e.EventKind == r.EventKind && e.ChannelID == r.ChannelID {
			return errors.New("Error 1062: Duplicate entry")
		}
	}
	dup := *r
	f.rows[r.ID] = &dup
	return nil
}
func (f *fakeUserRoutesRepo) ListByUser(_ context.Context, userID string) ([]models.UserNotificationRoute, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.UserNotificationRoute, 0)
	for _, r := range f.rows {
		if r.UserID == userID {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (f *fakeUserRoutesRepo) ListByUserEvent(_ context.Context, userID, eventKind string) ([]models.UserNotificationRoute, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.UserNotificationRoute, 0)
	for _, r := range f.rows {
		if r.UserID == userID && r.EventKind == eventKind {
			out = append(out, *r)
		}
	}
	return out, nil
}
func (f *fakeUserRoutesRepo) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.rows, id)
	return nil
}
func (f *fakeUserRoutesRepo) DeleteByChannel(_ context.Context, channelID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, r := range f.rows {
		if r.ChannelID == channelID {
			delete(f.rows, id)
		}
	}
	return nil
}

// --- plumbing ---

func meNotifSettings(enabled bool) *mockServerSettingsRepo {
	return &mockServerSettingsRepo{getResult: &models.ServerSettings{TenantNotificationsEnabled: enabled}}
}

func newUserCtxID(userID string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: false})
		c.Next()
	}
}

func newMeNotifRouter(t *testing.T, settings repository.ServerSettingsRepository, channels repository.NotificationChannelRepository, routes repository.UserNotificationRouteRepository, authFn gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	if authFn != nil {
		g.Use(authFn)
	}
	RegisterMeNotificationsRoutes(g, MeNotificationsConfig{
		Channels:       channels,
		Routes:         routes,
		ServerSettings: settings,
	})
	return r
}

func ownedChannel(userID, kind string, cfg models.NotificationChannelConfig) *models.NotificationChannel {
	uid := userID
	return &models.NotificationChannel{
		ID:      ids.NewULID(),
		UserID:  &uid,
		Name:    "ch-" + kind,
		Kind:    kind,
		Config:  cfg,
		Enabled: true,
	}
}

// --- tests ---

func TestMeNotif_GateOff_Is403(t *testing.T) {
	t.Parallel()
	r := newMeNotifRouter(t, meNotifSettings(false), &fakeChannelsRepo{}, &fakeUserRoutesRepo{}, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodGet, "/api/v1/me/notifications/channels", nil)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func TestMeNotif_GateFailsClosedOnSettingsError(t *testing.T) {
	t.Parallel()
	settings := &mockServerSettingsRepo{getErr: context.DeadlineExceeded}
	r := newMeNotifRouter(t, settings, &fakeChannelsRepo{}, &fakeUserRoutesRepo{}, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodGet, "/api/v1/me/notifications/channels", nil)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

func TestMeNotif_CreateNtfy_SetsOwnerAndRedacts(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	r := newMeNotifRouter(t, meNotifSettings(true), repo, &fakeUserRoutesRepo{}, newUserCtxID("u1"))
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/me/notifications/channels", map[string]any{
		"name":   "My phone",
		"kind":   "ntfy",
		"config": map[string]any{"url": "https://ntfy.sh/mytopic"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	// Stored row must be owned by the caller — never a body-supplied user_id.
	require.Len(t, repo.rows, 1)
	for _, row := range repo.rows {
		require.NotNil(t, row.UserID)
		require.Equal(t, "u1", *row.UserID)
	}
}

func TestMeNotif_CreateDisallowedKind_Is422(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"webhook", "sms", "slack", "email"} {
		repo := &fakeChannelsRepo{}
		r := newMeNotifRouter(t, meNotifSettings(true), repo, &fakeUserRoutesRepo{}, newUserCtxID("u1"))
		rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/me/notifications/channels", map[string]any{
			"name":   "x",
			"kind":   kind,
			"config": map[string]any{"url": "https://example.com/h", "hmac_secret": "0123456789abcdef"},
		})
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "kind %s should be rejected: %s", kind, rec.Body.String())
		require.Empty(t, repo.rows, "kind %s must not be persisted", kind)
	}
}

func TestMeNotif_CreateTelegram_SecretNotEchoed(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	r := newMeNotifRouter(t, meNotifSettings(true), repo, &fakeUserRoutesRepo{}, newUserCtxID("u1"))
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/me/notifications/channels", map[string]any{
		"name":   "tg",
		"kind":   "telegram",
		"config": map[string]any{"bot_token": "123:secretzzz", "chat_id": "42"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "secretzzz", "bot_token must not be echoed")
	// And a subsequent list also redacts.
	lrec := doNotifJSON(t, r, http.MethodGet, "/api/v1/me/notifications/channels", nil)
	require.Equal(t, http.StatusOK, lrec.Code)
	require.NotContains(t, lrec.Body.String(), "secretzzz")
}

func TestMeNotif_ListReturnsOnlyOwn(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	mine := ownedChannel("u1", "ntfy", models.NotificationChannelConfig{URL: "https://ntfy.sh/a"})
	theirs := ownedChannel("u2", "ntfy", models.NotificationChannelConfig{URL: "https://ntfy.sh/b"})
	_ = repo.Create(context.Background(), mine)
	_ = repo.Create(context.Background(), theirs)
	r := newMeNotifRouter(t, meNotifSettings(true), repo, &fakeUserRoutesRepo{}, newUserCtxID("u1"))
	rec := doNotifJSON(t, r, http.MethodGet, "/api/v1/me/notifications/channels", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), mine.ID)
	require.NotContains(t, rec.Body.String(), theirs.ID)
}

func TestMeNotif_MutateOtherUsersChannel_Is404(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	theirs := ownedChannel("u2", "ntfy", models.NotificationChannelConfig{URL: "https://ntfy.sh/b"})
	_ = repo.Create(context.Background(), theirs)
	r := newMeNotifRouter(t, meNotifSettings(true), repo, &fakeUserRoutesRepo{}, newUserCtxID("u1"))

	del := doNotifJSON(t, r, http.MethodDelete, "/api/v1/me/notifications/channels/"+theirs.ID, nil)
	require.Equal(t, http.StatusNotFound, del.Code, del.Body.String())

	patch := doNotifJSON(t, r, http.MethodPatch, "/api/v1/me/notifications/channels/"+theirs.ID, map[string]any{"name": "hijack"})
	require.Equal(t, http.StatusNotFound, patch.Code, patch.Body.String())

	// The row must be untouched.
	require.Equal(t, "ch-ntfy", repo.rows[theirs.ID].Name)
}

func TestMeNotif_RouteToUnownedChannel_Is404(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	theirs := ownedChannel("u2", "ntfy", models.NotificationChannelConfig{URL: "https://ntfy.sh/b"})
	_ = repo.Create(context.Background(), theirs)
	routes := &fakeUserRoutesRepo{}
	r := newMeNotifRouter(t, meNotifSettings(true), repo, routes, newUserCtxID("u1"))
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/me/notifications/routes", map[string]any{
		"event_kind": "backup.completed",
		"channel_id": theirs.ID,
	})
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	require.Empty(t, routes.rows)
}

func TestMeNotif_RouteHappyThenDeleteOwnership(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	mine := ownedChannel("u1", "ntfy", models.NotificationChannelConfig{URL: "https://ntfy.sh/a"})
	_ = repo.Create(context.Background(), mine)
	routes := &fakeUserRoutesRepo{}
	r := newMeNotifRouter(t, meNotifSettings(true), repo, routes, newUserCtxID("u1"))

	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/me/notifications/routes", map[string]any{
		"event_kind": "backup.completed",
		"channel_id": mine.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.Len(t, routes.rows, 1)

	// A different user cannot delete u1's route.
	var routeID string
	for id := range routes.rows {
		routeID = id
	}
	rOther := newMeNotifRouter(t, meNotifSettings(true), repo, routes, newUserCtxID("u2"))
	del := doNotifJSON(t, rOther, http.MethodDelete, "/api/v1/me/notifications/routes/"+routeID, nil)
	require.Equal(t, http.StatusNotFound, del.Code, del.Body.String())
	require.Len(t, routes.rows, 1, "route must survive the foreign delete")

	// The owner can.
	delOwn := doNotifJSON(t, r, http.MethodDelete, "/api/v1/me/notifications/routes/"+routeID, nil)
	require.Equal(t, http.StatusNoContent, delOwn.Code, delOwn.Body.String())
	require.Empty(t, routes.rows)
}

func TestMeNotif_KindAllowlist_DefaultBlocksWebhook_AdminOptInAllows(t *testing.T) {
	t.Parallel()
	body := map[string]any{
		"name":   "wh",
		"kind":   "webhook",
		"config": map[string]any{"url": "https://example.com/hook", "hmac_secret": "0123456789abcdef"},
	}

	// Default allowlist (empty settings → safe set) blocks webhook.
	rDefault := newMeNotifRouter(t, meNotifSettings(true), &fakeChannelsRepo{}, &fakeUserRoutesRepo{}, newUserCtxID("u1"))
	recBlocked := doNotifJSON(t, rDefault, http.MethodPost, "/api/v1/me/notifications/channels", body)
	require.Equal(t, http.StatusUnprocessableEntity, recBlocked.Code, recBlocked.Body.String())
	require.Contains(t, recBlocked.Body.String(), "kind_not_allowed_for_tenant")

	// Admin opts webhook into the allowlist → tenant may now create it.
	settings := &mockServerSettingsRepo{getResult: &models.ServerSettings{
		TenantNotificationsEnabled: true,
		TenantNotificationKinds:    models.TenantNotificationKinds{"ntfy", "webhook"},
	}}
	repo := &fakeChannelsRepo{}
	rAllowed := newMeNotifRouter(t, settings, repo, &fakeUserRoutesRepo{}, newUserCtxID("u1"))
	recOK := doNotifJSON(t, rAllowed, http.MethodPost, "/api/v1/me/notifications/channels", body)
	require.Equal(t, http.StatusCreated, recOK.Code, recOK.Body.String())
	require.Len(t, repo.rows, 1)
	for _, row := range repo.rows {
		require.Equal(t, "webhook", row.Kind)
		require.NotNil(t, row.UserID)
		require.Equal(t, "u1", *row.UserID)
	}
}

func TestMeNotif_ChannelQuota_Is409WhenFull(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	// Seed the per-user cap worth of channels for u1.
	for i := 0; i < 10; i++ {
		_ = repo.Create(context.Background(), ownedChannel("u1", "ntfy", models.NotificationChannelConfig{URL: "https://ntfy.sh/a"}))
	}
	r := newMeNotifRouter(t, meNotifSettings(true), repo, &fakeUserRoutesRepo{}, newUserCtxID("u1"))
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/me/notifications/channels", map[string]any{
		"name":   "one too many",
		"kind":   "ntfy",
		"config": map[string]any{"url": "https://ntfy.sh/b"},
	})
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "too_many_channels")
	require.Len(t, repo.rows, 10, "the 11th channel must not be persisted")
}

func TestMeNotif_TestSendRateLimited_After5PerMin(t *testing.T) {
	t.Parallel()
	// One router → one handler → one limiter shared across the requests.
	r := newMeNotifRouter(t, meNotifSettings(true), &fakeChannelsRepo{}, &fakeUserRoutesRepo{}, newUserCtxID("u1"))
	// Registry is nil, so an allowed call falls through to 503; the 6th call is
	// refused by the rate limiter (5/min) BEFORE reaching that path.
	var last int
	for i := 0; i < 6; i++ {
		rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/me/notifications/channels/anyid/test", nil)
		last = rec.Code
	}
	require.Equal(t, http.StatusTooManyRequests, last, "6th test-send within a minute must be 429")
}

func TestMeNotif_InvalidEventKind_Is422(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	mine := ownedChannel("u1", "ntfy", models.NotificationChannelConfig{URL: "https://ntfy.sh/a"})
	_ = repo.Create(context.Background(), mine)
	r := newMeNotifRouter(t, meNotifSettings(true), repo, &fakeUserRoutesRepo{}, newUserCtxID("u1"))
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/me/notifications/routes", map[string]any{
		"event_kind": "bad kind!",
		"channel_id": mine.ID,
	})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}
