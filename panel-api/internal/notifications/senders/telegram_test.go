package senders

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

func telegramChannel() models.NotificationChannel {
	return models.NotificationChannel{
		Kind: "telegram",
		Config: models.NotificationChannelConfig{
			BotToken: "123:ABC",
			ChatID:   "-1001234567890",
		},
	}
}

func TestTelegram_SendsChatIDAndText(t *testing.T) {
	t.Parallel()
	var gotPath string
	var payload struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tg := NewTelegram()
	tg.apiBase = ts.URL
	env := notifications.Envelope{Title: "Backup failed", Body: "nightly job died", Deeplink: "https://panel/backups"}
	require.NoError(t, tg.Send(context.Background(), telegramChannel(), env))

	require.Equal(t, "/bot123:ABC/sendMessage", gotPath)
	require.Equal(t, "-1001234567890", payload.ChatID)
	require.True(t, strings.Contains(payload.Text, "Backup failed"), "title in text")
	require.True(t, strings.Contains(payload.Text, "nightly job died"), "body in text")
	require.True(t, strings.Contains(payload.Text, "https://panel/backups"), "deeplink in text")
}

func TestTelegram_MissingConfigIsPermanent(t *testing.T) {
	t.Parallel()
	tg := NewTelegram()
	env := notifications.Envelope{Title: "x", Body: "y"}
	err := tg.Send(context.Background(), models.NotificationChannel{Kind: "telegram"}, env)
	require.Error(t, err)
	require.True(t, errors.Is(err, notifications.ErrPermanent), "missing bot_token → permanent")

	err = tg.Send(context.Background(), models.NotificationChannel{
		Kind:   "telegram",
		Config: models.NotificationChannelConfig{BotToken: "t"},
	}, env)
	require.True(t, errors.Is(err, notifications.ErrPermanent), "missing chat_id → permanent")
}

func TestTelegram_BadTokenIsPermanent(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	}))
	defer ts.Close()

	tg := NewTelegram()
	tg.apiBase = ts.URL
	err := tg.Send(context.Background(), telegramChannel(), notifications.Envelope{Title: "t", Body: "b"})
	require.Error(t, err)
	require.True(t, errors.Is(err, notifications.ErrPermanent), "401 → permanent (don't retry a bad token)")
}
