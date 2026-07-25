package senders

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// Webhook posts the raw envelope to an admin-configured URL with an
// HMAC-SHA256 signature the receiver can verify. Useful for custom
// automations (PagerDuty-like ingestion, Zapier catch hooks).
type Webhook struct {
	client *http.Client
	now    func() time.Time
}

func NewWebhook() *Webhook { return &Webhook{client: newHTTPClient(), now: time.Now} }

func (w *Webhook) Kind() string { return models.NotificationChannelKindWebhook }

type webhookPayload struct {
	EventKind string `json:"event_kind"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Deeplink  string `json:"deeplink,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	Timestamp string `json:"ts"`
}

func (w *Webhook) Send(ctx context.Context, channel models.NotificationChannel, env notifications.Envelope) error {
	if channel.Config.URL == "" {
		return fmt.Errorf("webhook: missing url in channel config: %w", notifications.ErrPermanent)
	}
	if channel.Config.HMACSecret == "" {
		return fmt.Errorf("webhook: missing hmac_secret in channel config: %w", notifications.ErrPermanent)
	}
	payload := webhookPayload{
		EventKind: env.EventKind,
		Severity:  env.Severity,
		Title:     env.Title,
		Body:      env.Body,
		Deeplink:  env.Deeplink,
		UserID:    env.UserID,
		Timestamp: w.now().UTC().Format(time.RFC3339),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("webhook: marshal payload: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(channel.Config.HMACSecret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.Config.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Jabali-Signature", sig)
	req.Header.Set("User-Agent", "jabali-panel-webhook/1")

	resp, err := clientForChannel(w.client, channel).Do(req)
	if err != nil {
		return fmt.Errorf("webhook: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
		return fmt.Errorf("webhook: %d %s: %w", resp.StatusCode, string(respBody), notifications.ErrPermanent)
	}
	return fmt.Errorf("webhook: %d %s", resp.StatusCode, string(respBody))
}
