package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes ---

type fakeChannelsRepo struct {
	mu   sync.Mutex
	rows map[string]*models.NotificationChannel
}

func (f *fakeChannelsRepo) Create(_ context.Context, ch *models.NotificationChannel) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rows == nil {
		f.rows = map[string]*models.NotificationChannel{}
	}
	dup := *ch
	f.rows[ch.ID] = &dup
	return nil
}
func (f *fakeChannelsRepo) Update(_ context.Context, ch *models.NotificationChannel) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[ch.ID]; !ok {
		return repository.ErrNotFound
	}
	dup := *ch
	f.rows[ch.ID] = &dup
	return nil
}
func (f *fakeChannelsRepo) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[id]; !ok {
		return repository.ErrNotFound
	}
	delete(f.rows, id)
	return nil
}
func (f *fakeChannelsRepo) FindByID(_ context.Context, id string) (*models.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	out := *ch
	return &out, nil
}
func (f *fakeChannelsRepo) ListAll(_ context.Context, _ repository.ListOptions) ([]models.NotificationChannel, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.NotificationChannel, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, *r)
	}
	return out, int64(len(out)), nil
}
func (f *fakeChannelsRepo) FindEnabledByKind(context.Context, string) ([]models.NotificationChannel, error) {
	return nil, nil
}
func (f *fakeChannelsRepo) FindEnabledAll(context.Context) ([]models.NotificationChannel, error) {
	return nil, nil
}

func (f *fakeChannelsRepo) FindEnabledServerWide(context.Context) ([]models.NotificationChannel, error) {
	return nil, nil
}

func (f *fakeChannelsRepo) ListByUser(_ context.Context, userID string) ([]models.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]models.NotificationChannel, 0)
	for _, r := range f.rows {
		if r.UserID != nil && *r.UserID == userID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *fakeChannelsRepo) SealAllPlaintext(context.Context) (int, error) { return 0, nil }

// --- test plumbing ---

func newAdminCtx() gin.HandlerFunc {
	return func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		c.Next()
	}
}

func newUserCtx() gin.HandlerFunc {
	return func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: false})
		c.Next()
	}
}

func newChannelsRouter(t *testing.T, repo repository.NotificationChannelRepository, queue *notifications.Queue, auth gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	if auth != nil {
		g.Use(auth)
	}
	RegisterNotificationsChannelsRoutes(g, NotificationsChannelsHandlerConfig{
		Channels: repo,
		Queue:    queue,
	})
	return r
}

func newQueueForChannelTest(t *testing.T) (*notifications.Queue, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = c.Close() })
	return notifications.NewQueue(c), mr
}

func doNotifJSON(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// --- tests ---

func TestChannels_Create_SlackHappy(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/channels", map[string]any{
		"name": "Ops Slack",
		"kind": "slack",
		"config": map[string]any{"url": "https://hooks.slack.com/services/T/B/X"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var out models.NotificationChannel
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.ID)
	require.True(t, out.Enabled)
}

func TestChannels_Create_UnknownKindIs422(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/channels", map[string]any{
		"name":   "bogus",
		"kind":   "nope",
		"config": map[string]any{},
	})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestChannels_Create_WebhookShortHMACIs422(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/channels", map[string]any{
		"name":   "wh",
		"kind":   "webhook",
		"config": map[string]any{"url": "https://panel/x", "hmac_secret": "short"},
	})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "hmac_secret")
}

func TestChannels_Create_NtfyNonHTTPURLIs422(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/channels", map[string]any{
		"name":   "n",
		"kind":   "ntfy",
		"config": map[string]any{"url": "ws://x"},
	})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

func TestChannels_Create_NonAdminIs403(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	r := newChannelsRouter(t, repo, nil, newUserCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/channels", map[string]any{
		"name":   "x",
		"kind":   "slack",
		"config": map[string]any{"url": "https://x"},
	})
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestChannels_List(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{rows: map[string]*models.NotificationChannel{
		"a": {ID: "a", Name: "Ops", Kind: "slack", Enabled: true, Config: models.NotificationChannelConfig{URL: "https://x"}},
	}}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodGet, "/api/v1/admin/notifications/channels", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp channelListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 1, resp.Total)
	require.Len(t, resp.Data, 1)
	require.Equal(t, 1, resp.Page)
	require.Greater(t, resp.PageSize, 0)
}

func TestChannels_Update_Partial(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{rows: map[string]*models.NotificationChannel{
		"a": {ID: "a", Name: "Ops", Kind: "slack", Enabled: true, Config: models.NotificationChannelConfig{URL: "https://x"}},
	}}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	enabled := false
	rec := doNotifJSON(t, r, http.MethodPatch, "/api/v1/admin/notifications/channels/a", map[string]any{"enabled": enabled})
	require.Equal(t, http.StatusOK, rec.Code)
	row, err := repo.FindByID(context.Background(), "a")
	require.NoError(t, err)
	require.False(t, row.Enabled)
	require.Equal(t, "Ops", row.Name) // untouched
}

func TestChannels_Delete(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{rows: map[string]*models.NotificationChannel{
		"a": {ID: "a", Name: "Ops", Kind: "slack"},
	}}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodDelete, "/api/v1/admin/notifications/channels/a", nil)
	require.Equal(t, http.StatusNoContent, rec.Code)
	_, err := repo.FindByID(context.Background(), "a")
	require.True(t, errors.Is(err, repository.ErrNotFound))
}

func TestChannels_Test_PublishesEnvelope(t *testing.T) {
	t.Parallel()
	q, mr := newQueueForChannelTest(t)
	repo := &fakeChannelsRepo{rows: map[string]*models.NotificationChannel{
		"a": {ID: "a", Name: "Ops", Kind: "slack", Enabled: true, Config: models.NotificationChannelConfig{URL: "https://x"}},
	}}
	r := newChannelsRouter(t, repo, q, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/channels/a/test", nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	entries, err := mr.DB(0).Stream("jabali:notifications:queue")
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestChannels_Test_NoQueueReturns503(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{rows: map[string]*models.NotificationChannel{
		"a": {ID: "a", Name: "Ops", Kind: "slack"},
	}}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/channels/a/test", nil)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestChannels_Broadcast_Happy(t *testing.T) {
	t.Parallel()
	q, mr := newQueueForChannelTest(t)
	repo := &fakeChannelsRepo{}
	r := newChannelsRouter(t, repo, q, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/broadcast", map[string]any{
		"title":    "Maintenance window",
		"body":     "Panel restart at 02:00 UTC",
		"severity": "info",
	})
	require.Equal(t, http.StatusAccepted, rec.Code)
	entries, err := mr.DB(0).Stream("jabali:notifications:queue")
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestChannels_Broadcast_BadSeverityIs422(t *testing.T) {
	t.Parallel()
	q, _ := newQueueForChannelTest(t)
	repo := &fakeChannelsRepo{}
	r := newChannelsRouter(t, repo, q, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/broadcast", map[string]any{
		"title":    "t",
		"severity": "hmm",
	})
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
}

// JAB-171 phase 3: a channel's secret is never echoed by create or list.
func TestChannels_Create_SecretNotEchoed(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodPost, "/api/v1/admin/notifications/channels", map[string]any{
		"name":   "tg",
		"kind":   "telegram",
		"config": map[string]any{"bot_token": "123:SUPER-SECRET", "chat_id": "42"},
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "SUPER-SECRET", "create must not echo the bot token")
	require.NotContains(t, rec.Body.String(), "bot_token")
}

func TestChannels_List_SecretNotEchoed(t *testing.T) {
	t.Parallel()
	repo := &fakeChannelsRepo{rows: map[string]*models.NotificationChannel{
		"a": {ID: "a", Name: "tg", Kind: "telegram", Enabled: true,
			Config: models.NotificationChannelConfig{BotToken: "enc:1:sealedvalue", ChatID: "42"}},
	}}
	r := newChannelsRouter(t, repo, nil, newAdminCtx())
	rec := doNotifJSON(t, r, http.MethodGet, "/api/v1/admin/notifications/channels", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "sealedvalue", "list must not echo the sealed secret")
	require.Contains(t, rec.Body.String(), "\"chat_id\":\"42\"", "public config fields should still appear")
}
