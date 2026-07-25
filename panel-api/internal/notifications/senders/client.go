// Package senders hosts the concrete ChannelSender implementations for
// M14 (ADR-0056). Each file corresponds to one channel kind; the
// dispatcher looks them up by kind via the notifications.Registry.
//
// Senders share a single *http.Client with a 10s timeout. Retries are
// the dispatcher's job — if a transport returns a transient error
// (non-nil err that isn't wrapped with notifications.ErrPermanent), the
// stream entry stays in the PEL for reclaim. Permanent errors (4xx on
// admin-configured URLs, malformed config, webpush 410 Gone on a single
// sub which we swallow after deleting the row) return ErrPermanent so
// the envelope doesn't loop forever.
package senders

import (
	"net/http"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssrfguard"
)

// DefaultHTTPTimeout bounds outbound senders. Admin-configured URLs
// point at third parties we don't control, so failures should surface
// fast rather than holding the dispatcher hostage.
const DefaultHTTPTimeout = 10 * time.Second

// newHTTPClient builds a *http.Client every HTTP-based sender uses.
// Exposed (lowercase, package-private) so tests can poke a transport
// via httptest.NewServer and still exercise the real request-shape
// code.
//
// Each call returns a client with its OWN Transport (cloned from the
// stdlib default). Without this, every sender silently shares
// http.DefaultTransport — fine in production where senders are
// long-lived singletons, but in CI under t.Parallel + -race the shared
// idle-conn pool gets stomped: when one parallel test's httptest
// Server.Close() races with another test's in-flight Post, the second
// test fails with `transport connection broken: http: CloseIdleConnections
// called`. Per-client transports isolate the pool so a tear-down on
// one server can't kill another sender's request.
func newHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Timeout: DefaultHTTPTimeout, Transport: t}
}

// sharedGuardedClient is the SSRF-guarded client used for tenant-owned channels
// (JAB-171 phase 4). Its transport resolves + pins every dial and rejects
// loopback / link-local (cloud metadata) / private ranges, defeating a tenant
// pointing an ntfy/discord/webhook URL at an internal address. Server-wide/admin
// channels keep the normal client — an operator may legitimately target an
// internal host. Package-level singleton: the guard holds no per-channel state.
var sharedGuardedClient = newGuardedHTTPClient()

func newGuardedHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = ssrfguard.GuardedDialContext
	return &http.Client{Timeout: DefaultHTTPTimeout, Transport: t}
}

// clientForChannel returns the SSRF-guarded client for a tenant-owned channel
// (UserID set) and the sender's default client for server-wide/admin channels.
// Selecting on the channel itself makes the guard fail-closed per channel,
// independent of which call site invoked Send.
func clientForChannel(def *http.Client, channel models.NotificationChannel) *http.Client {
	if channel.UserID != nil {
		return sharedGuardedClient
	}
	return def
}
