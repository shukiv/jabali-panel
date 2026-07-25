package senders

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// The httptest server binds loopback (127.0.0.1). A tenant-owned channel routes
// through the SSRF-guarded client, which rejects loopback before connecting; a
// server-wide (admin) channel keeps the default client and reaches it. This is
// the JAB-171 phase-4 end-to-end guard proof — one server, ownership decides.
func TestSenders_TenantChannelGuarded_AdminChannelNot(t *testing.T) {
	t.Parallel()
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	env := notifications.Envelope{Title: "t", Body: "b"}
	uid := "u1"

	// Server-wide (UserID nil) — reaches the loopback server.
	adminCh := models.NotificationChannel{Kind: "ntfy", Config: models.NotificationChannelConfig{URL: ts.URL}}
	require.NoError(t, NewNtfy().Send(context.Background(), adminCh, env))
	require.Equal(t, 1, hits)

	// Tenant-owned (UserID set) — same URL, but the guarded client refuses the
	// loopback dial. The server must NOT be hit.
	tenantCh := models.NotificationChannel{Kind: "ntfy", UserID: &uid, Config: models.NotificationChannelConfig{URL: ts.URL}}
	err := NewNtfy().Send(context.Background(), tenantCh, env)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ssrf")
	require.Equal(t, 1, hits, "guarded tenant send must not reach the loopback server")
}

// The guard also covers the other tenant-reachable / phase-4 URL senders.
func TestSenders_TenantGuard_AcrossURLKinds(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ts.Close()
	uid := "u1"
	env := notifications.Envelope{Title: "t", Body: "b"}

	cases := []struct {
		name string
		send func(models.NotificationChannel) error
		cfg  models.NotificationChannelConfig
	}{
		{"discord", func(ch models.NotificationChannel) error { return NewDiscord().Send(context.Background(), ch, env) }, models.NotificationChannelConfig{URL: ts.URL}},
		{"webhook", func(ch models.NotificationChannel) error { return NewWebhook().Send(context.Background(), ch, env) }, models.NotificationChannelConfig{URL: ts.URL, HMACSecret: "0123456789abcdef"}},
		{"slack", func(ch models.NotificationChannel) error { return NewSlack().Send(context.Background(), ch, env) }, models.NotificationChannelConfig{URL: ts.URL}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := models.NotificationChannel{Kind: tc.name, UserID: &uid, Config: tc.cfg}
			err := tc.send(ch)
			require.Error(t, err)
			require.Contains(t, err.Error(), "ssrf", "%s tenant send must be SSRF-blocked", tc.name)
		})
	}
}
