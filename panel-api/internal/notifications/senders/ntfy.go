package senders

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// Ntfy posts to ntfy.sh (or a self-hosted ntfy server). The topic URL
// is the configured endpoint; the body is plain text and metadata
// travels in HTTP headers.
type Ntfy struct {
	client *http.Client
}

func NewNtfy() *Ntfy { return &Ntfy{client: newHTTPClient()} }

func (n *Ntfy) Kind() string { return models.NotificationChannelKindNtfy }

func (n *Ntfy) Send(ctx context.Context, channel models.NotificationChannel, env notifications.Envelope) error {
	if channel.Config.URL == "" {
		return fmt.Errorf("ntfy: missing topic_url in channel config: %w", notifications.ErrPermanent)
	}
	title, body := notifications.RenderForChannel(env, n.Kind())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, channel.Config.URL, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("ntfy: build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	if title != "" {
		req.Header.Set("Title", title)
	}
	if channel.Config.Priority != 0 {
		req.Header.Set("Priority", strconv.Itoa(channel.Config.Priority))
	}
	if len(channel.Config.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(channel.Config.Tags, ","))
	}
	if channel.Config.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+channel.Config.Bearer)
	}
	if env.Deeplink != "" {
		req.Header.Set("Click", env.Deeplink)
	}
	resp, err := clientForChannel(n.client, channel).Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("ntfy: %d %s: %w", resp.StatusCode, string(respBody), notifications.ErrPermanent)
	}
	return fmt.Errorf("ntfy: %d %s", resp.StatusCode, string(respBody))
}
