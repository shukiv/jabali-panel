package senders

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// Discord posts to a Discord Webhook URL. Same shape as Slack but with
// Discord's payload keys (`content` + `embeds`).
type Discord struct {
	client *http.Client
}

func NewDiscord() *Discord { return &Discord{client: newHTTPClient()} }

func (d *Discord) Kind() string { return models.NotificationChannelKindDiscord }

type discordPayload struct {
	Content string          `json:"content"`
	Embeds  []discordEmbed  `json:"embeds,omitempty"`
}

type discordEmbed struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Color       int    `json:"color,omitempty"`
	URL         string `json:"url,omitempty"`
}

func severityColor(s string) int {
	switch s {
	case models.NotificationSeverityCritical, models.NotificationSeverityError:
		return 0xE01E5A // red
	case models.NotificationSeverityWarning:
		return 0xECB22E // yellow
	case models.NotificationSeverityInfo:
		return 0x2EB67D // green
	default:
		return 0x8A8A8A // grey
	}
}

func (d *Discord) Send(ctx context.Context, channel models.NotificationChannel, env notifications.Envelope) error {
	if channel.Config.URL == "" {
		return fmt.Errorf("discord: missing webhook_url in channel config: %w", notifications.ErrPermanent)
	}
	title, body := notifications.RenderForChannel(env, d.Kind())
	payload := discordPayload{
		Content: title,
		Embeds: []discordEmbed{{
			Title:       title,
			Description: body,
			Color:       severityColor(env.Severity),
			URL:         env.Deeplink,
		}},
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("discord: marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.Config.URL, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("discord: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := clientForChannel(d.client, channel).Do(req)
	if err != nil {
		return fmt.Errorf("discord: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return fmt.Errorf("discord: %d %s: %w", resp.StatusCode, string(respBody), notifications.ErrPermanent)
	}
	return fmt.Errorf("discord: %d %s", resp.StatusCode, string(respBody))
}
