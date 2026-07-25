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

// Slack posts to a Slack Incoming Webhook URL. The URL is the per-channel
// secret configured by the admin; no auth header is required — Slack
// authenticates on the URL path itself.
type Slack struct {
	client *http.Client
}

func NewSlack() *Slack { return &Slack{client: newHTTPClient()} }

func (s *Slack) Kind() string { return models.NotificationChannelKindSlack }

type slackPayload struct {
	Text   string        `json:"text"`
	Blocks []interface{} `json:"blocks,omitempty"`
}

func (s *Slack) Send(ctx context.Context, channel models.NotificationChannel, env notifications.Envelope) error {
	if channel.Config.URL == "" {
		return fmt.Errorf("slack: missing webhook_url in channel config: %w", notifications.ErrPermanent)
	}
	title, body := notifications.RenderForChannel(env, s.Kind())
	payload := slackPayload{Text: title + "\n" + body}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack: marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.Config.URL, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("slack: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := clientForChannel(s.client, channel).Do(req)
	if err != nil {
		return fmt.Errorf("slack: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	// 4xx on a Slack webhook means bad URL or revoked integration.
	// Permanent: retries won't fix it.
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return fmt.Errorf("slack: %d %s: %w", resp.StatusCode, string(respBody), notifications.ErrPermanent)
	}
	return fmt.Errorf("slack: %d %s", resp.StatusCode, string(respBody))
}
