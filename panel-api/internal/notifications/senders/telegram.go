package senders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// Telegram delivers via the Bot API sendMessage endpoint
// (https://api.telegram.org/bot<token>/sendMessage). The bot token comes
// from @BotFather; chat_id is the user/group/channel the bot posts to
// (the bot must have been started by / added to that chat). Plain-text
// body — no parse_mode — so message content never needs Markdown/HTML
// escaping.
type Telegram struct {
	client *http.Client
	// apiBase is overridable in tests; defaults to the public Bot API host.
	apiBase string
}

func NewTelegram() *Telegram {
	return &Telegram{client: newHTTPClient(), apiBase: "https://api.telegram.org"}
}

func (t *Telegram) Kind() string { return models.NotificationChannelKindTelegram }

type telegramSendMessage struct {
	ChatID             string `json:"chat_id"`
	Text               string `json:"text"`
	DisableWebPagePrev bool   `json:"disable_web_page_preview,omitempty"`
}

func (t *Telegram) Send(ctx context.Context, channel models.NotificationChannel, env notifications.Envelope) error {
	if channel.Config.BotToken == "" {
		return fmt.Errorf("telegram: missing bot_token in channel config: %w", notifications.ErrPermanent)
	}
	if channel.Config.ChatID == "" {
		return fmt.Errorf("telegram: missing chat_id in channel config: %w", notifications.ErrPermanent)
	}
	title, body := notifications.RenderForChannel(env, t.Kind())
	text := body
	if title != "" {
		text = title + "\n\n" + body
	}

	payload, err := json.Marshal(telegramSendMessage{
		ChatID:             channel.Config.ChatID,
		Text:               text,
		DisableWebPagePrev: true,
	})
	if err != nil {
		return fmt.Errorf("telegram: marshal: %w", err)
	}

	// The bot token is in the path, so it is never logged as a header.
	endpoint := t.apiBase + "/bot" + url.PathEscape(channel.Config.BotToken) + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	// 401 = bad token, 400 = bad/unknown chat_id, 403 = bot blocked/kicked.
	// All are misconfiguration — permanent, don't retry.
	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusBadRequest ||
		resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("telegram: %d %s: %w", resp.StatusCode, string(respBody), notifications.ErrPermanent)
	}
	return fmt.Errorf("telegram: %d %s", resp.StatusCode, string(respBody))
}
