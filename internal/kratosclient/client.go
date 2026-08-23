// Package kratosclient provides a minimal HTTP client for Ory Kratos identity platform.
// It handles session validation via the /sessions/whoami endpoint and caches results
// to reduce upstream load during high-concurrency periods.
package kratosclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrUnauthenticated = errors.New("unauthenticated")

// Identity is the session identity response from Kratos /sessions/whoami.
// It captures the minimal fields needed for Jabali's RBAC (email, username, is_admin).
type Identity struct {
	ID     string                 `json:"id"`
	Traits map[string]interface{} `json:"traits"`

	// Session-envelope metadata (JAB-380). /sessions/whoami returns these at
	// the SESSION level, not on the identity; whoamiRemote lifts them onto the
	// returned Identity so the auth middleware can thread them into claims for
	// recent-auth (step-up) gating on the root File Manager + Root Terminal.
	//
	// AuthenticatedAt is when Kratos last authenticated this session (bumped by
	// a `?refresh=true` login). Zero if Kratos omitted/failed to parse it —
	// callers treat zero as "not recently authenticated" (fail closed).
	// AAL is the assurance level ("aal1"/"aal2"); captured for a future
	// TOTP/passkey requirement, not enforced in v1.
	AuthenticatedAt time.Time `json:"-"`
	AAL             string    `json:"-"`
}

// Client is a Kratos session validator. It calls /sessions/whoami and caches results
// by cookie hash to reduce upstream load.
type Client struct {
	publicURL  string
	adminURL   string
	httpClient *http.Client
	cache      *Cache
}

// NewClient returns a Kratos client targeting the given public and admin URLs.
//
// Each URL is one of two forms:
//   - HTTP/HTTPS base URL: "http://localhost:4433" or "https://auth.example.com"
//   - Unix-socket: "unix:/run/jabali-kratos/admin.sock" (M25 Step 2+)
//
// The unix: form installs a custom http.Transport whose DialContext routes by
// hostname to the configured socket path. Both forms are supported on the
// same client — a panel that's been partially migrated (e.g. admin on unix,
// public still on TCP because nginx fronts it) keeps working transparently.
func NewClient(publicURL, adminURL string) *Client {
	publicURL = strings.TrimSuffix(publicURL, "/")
	adminURL = strings.TrimSuffix(adminURL, "/")

	const reqTimeout = 5 * time.Second

	// Funnel both URLs through the unix-rewrite step. Non-unix URLs pass
	// through unchanged; unix URLs become http://<synthetic-host> + a
	// hostname → socket-path entry in `sockets`.
	sockets := make(map[string]string, 2)
	publicURL = rewriteForUnix(publicURL, sockets)
	adminURL = rewriteForUnix(adminURL, sockets)

	return &Client{
		publicURL: publicURL,
		adminURL:  adminURL,
		httpClient: &http.Client{
			Timeout:   reqTimeout,
			Transport: newKratosTransport(sockets, reqTimeout),
		},
		cache: NewCache(10000, 10*time.Second),
	}
}

// AdminReady checks the Kratos admin /admin/health/ready endpoint and returns nil
// on a 2xx response. Used by the boot-order race-mitigation poll in cmd/server
// (M20 race: panel-api can beat Kratos to binding the admin port on slow boots
// and crash its first BootstrapAdmin call). Goes through the same transport
// the rest of the client uses, so unix-socket setups Just Work.
func (c *Client) AdminReady(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.adminURL+"/admin/health/ready", nil)
	if err != nil {
		return fmt.Errorf("ready: request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ready: do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ready: status %d", resp.StatusCode)
	}
	return nil
}

// InvalidateIdentity drops any cached positive whoami results for the given
// Kratos identity ID. Call it after an admin action that revokes or materially
// changes an identity's auth state (session revoke targeting the user, suspend,
// password reset, 2FA reset) so revocation is effective immediately instead of
// lingering until the whoami cache TTL expires. No-op when the client has no
// cache configured.
func (c *Client) InvalidateIdentity(identityID string) {
	if c == nil || c.cache == nil {
		return
	}
	c.cache.DeleteByIdentity(identityID)
}

// ClearCache flushes the entire positive whoami cache. Used for emergency admin
// actions where the affected identity can't be resolved from the inputs (e.g.
// a session revoke that only carries a session ID). Rare + admin-only, so the
// cost of forcing every live session to re-validate once is acceptable.
func (c *Client) ClearCache() {
	if c == nil || c.cache == nil {
		return
	}
	c.cache.Clear()
}

// Whoami validates a Kratos session cookie and returns the authenticated identity.
// The cookie is expected to be the raw cookie value (not the "ory_kratos_session=" prefix).
// Results are cached by cookie hash; cache misses round-trip to the Kratos public endpoint.
func (c *Client) Whoami(ctx context.Context, cookie string) (*Identity, error) {
	if cookie == "" {
		return nil, ErrUnauthenticated
	}

	// Check cache first.
	if identity, ok := c.cache.Get(cookie); ok {
		return identity, nil
	}

	// Cache miss: validate via /sessions/whoami.
	identity, err := c.whoamiRemote(ctx, cookie)
	if err != nil {
		return nil, err
	}

	// Cache the result for future lookups.
	c.cache.Set(cookie, identity)

	return identity, nil
}

// whoamiRemote calls the Kratos public /sessions/whoami endpoint with the session cookie.
func (c *Client) whoamiRemote(ctx context.Context, cookie string) (*Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.publicURL+"/sessions/whoami", nil)
	if err != nil {
		return nil, fmt.Errorf("whoami: failed to create request: %w", err)
	}

	// Attach the session cookie.
	req.AddCookie(&http.Cookie{
		Name:  "ory_kratos_session",
		Value: cookie,
	})

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whoami: request failed: %w", err)
	}
	defer resp.Body.Close()

	// 200 OK = authenticated; 401 = unauthenticated.
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthenticated
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("whoami: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	// Kratos /sessions/whoami returns a Session envelope, not a bare identity:
	//   { "id": "<session_uuid>", "active": true, "authenticated_at": "...",
	//     "authenticator_assurance_level": "aal1",
	//     "identity": { "id": "<identity_uuid>", "traits": {...} } }
	// The top-level "id" is the SESSION id — decoding into Identity directly
	// would populate Identity.ID with that session id and the caller's
	// FindByKratosIdentityID(id) would never match any users row. Decode the
	// envelope, then hand back the nested identity — with the session-level
	// authenticated_at / assurance level lifted onto it (JAB-380 step-up).
	var sess struct {
		Identity        Identity `json:"identity"`
		AuthenticatedAt string   `json:"authenticated_at"`
		AAL             string   `json:"authenticator_assurance_level"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sess); err != nil {
		return nil, fmt.Errorf("whoami: failed to decode response: %w", err)
	}
	if sess.Identity.ID == "" {
		return nil, fmt.Errorf("whoami: response missing identity.id")
	}
	ident := sess.Identity
	ident.AAL = sess.AAL
	// Parse the RFC3339 timestamp; on any parse failure leave the zero value
	// so a recent-auth check fails closed rather than treating an unparseable
	// time as "now".
	if sess.AuthenticatedAt != "" {
		if ts, perr := time.Parse(time.RFC3339Nano, sess.AuthenticatedAt); perr == nil {
			ident.AuthenticatedAt = ts
		}
	}
	return &ident, nil
}

// WhoamiUncached validates a session cookie by round-tripping to Kratos,
// bypassing the positive whoami cache, and refreshes the cache with the fresh
// result. JAB-380 step-up uses it: after a stale-session 403 the user completes
// a `?refresh=true` login on the SAME cookie, so a cached (pre-refresh)
// authenticated_at would otherwise keep failing the recency check for the rest
// of the TTL — a redirect loop. This forces one authoritative read of the
// post-refresh authenticated_at.
func (c *Client) WhoamiUncached(ctx context.Context, cookie string) (*Identity, error) {
	if cookie == "" {
		return nil, ErrUnauthenticated
	}
	identity, err := c.whoamiRemote(ctx, cookie)
	if err != nil {
		return nil, err
	}
	c.cache.Set(cookie, identity)
	return identity, nil
}

// GetTraitEmail extracts the email from a Kratos identity's traits.
// Returns empty string if not found.
func (i *Identity) GetTraitEmail() string {
	if email, ok := i.Traits["email"].(string); ok {
		return email
	}
	return ""
}

// GetTraitUsername extracts the username from a Kratos identity's traits.
// Returns empty string if not found.
func (i *Identity) GetTraitUsername() string {
	if username, ok := i.Traits["username"].(string); ok {
		return username
	}
	return ""
}

// GetTraitIsAdmin extracts the is_admin flag from a Kratos identity's traits.
// Returns false if not found or not a boolean.
func (i *Identity) GetTraitIsAdmin() bool {
	// is_admin might be a bool or a string "true"/"false" depending on how it was set.
	switch v := i.Traits["is_admin"].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}
