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

// SMS POSTs `{to, body}` JSON to a user-configured gateway URL with an
// optional HMAC-SHA256 signature header. The gateway can be a Twilio
// Function, a MessageBird/Telnyx webhook, or any tiny shim that turns
// the JSON into a real SMS via the provider's API.
//
// Why not Twilio SDK directly: provider lock-in is a footgun, the wire
// shape every SMS gateway accepts is roughly identical, and the user's
// own shim adds the per-provider auth headers without leaking those
// credentials to the panel.
type SMS struct {
	client *http.Client
	now    func() time.Time
}

func NewSMS() *SMS { return &SMS{client: newHTTPClient(), now: time.Now} }

func (s *SMS) Kind() string { return models.NotificationChannelKindSMS }

type smsPayload struct {
	To        string `json:"to"`
	Body      string `json:"body"`
	EventKind string `json:"event_kind,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Timestamp string `json:"ts"`
}

func (s *SMS) Send(ctx context.Context, channel models.NotificationChannel, env notifications.Envelope) error {
	if channel.Config.URL == "" {
		return fmt.Errorf("sms: missing url in channel config: %w", notifications.ErrPermanent)
	}
	if channel.Config.ToNumber == "" {
		return fmt.Errorf("sms: missing to_number in channel config: %w", notifications.ErrPermanent)
	}
	body := env.Title
	if env.Body != "" {
		body = env.Title + ": " + env.Body
	}
	// SMS spec hard-caps each segment at 160 GSM-7 chars. Most gateways
	// concatenate up to ~10 segments transparently; truncate at 1000
	// rune-bytes so a runaway body never racks up the operator's bill.
	if len(body) > 1000 {
		body = body[:997] + "..."
	}
	payload := smsPayload{
		To:        channel.Config.ToNumber,
		Body:      body,
		EventKind: env.EventKind,
		Severity:  env.Severity,
		Timestamp: s.now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sms: marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.Config.URL, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("sms: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "jabali-panel-sms/1")
	if channel.Config.HMACSecret != "" {
		mac := hmac.New(sha256.New, []byte(channel.Config.HMACSecret))
		mac.Write(raw)
		req.Header.Set("X-Jabali-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	if channel.Config.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+channel.Config.Bearer)
	}

	resp, err := clientForChannel(s.client, channel).Do(req)
	if err != nil {
		return fmt.Errorf("sms: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
		return fmt.Errorf("sms: %d %s: %w", resp.StatusCode, string(respBody), notifications.ErrPermanent)
	}
	return fmt.Errorf("sms: %d %s", resp.StatusCode, string(respBody))
}
