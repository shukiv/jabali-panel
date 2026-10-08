package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// loginWhitelistDefaultTTLHours is the fallback allowlist lifetime (when the
// server setting is unreadable), and loginWhitelistDedup is how often we
// refresh it. The dedup window is shorter than the TTL so an active session
// keeps the entry alive (re-adding refreshes the expiration); an idle session
// lets it lapse.
const (
	loginWhitelistDefaultTTLHours = 168 // 7 days, refreshed on activity (GH #598)
	loginWhitelistDedup           = 24 * time.Hour
)

// WhitelistLoginIP adds an authenticated user's client IP to the jabali
// CrowdSec allowlist (time-boxed) the first time a session is seen within the
// dedup window, so a logged-in admin or user is never bounced by CrowdSec from
// the IP they're actively working from. Best-effort and fully out-of-band: the
// add runs in a background goroutine and any missing dependency (Redis, agent)
// downgrades to a no-op — it must never block or fail the request.
//
// Mounted after RequireKratosSession (needs claims). Sibling to
// TrackAdminLogin; fires for ADMIN logins only (GH #709 — a tenant login must
// not add a server-wide CrowdSec allowlist bypass).
func WhitelistLoginIP(rdb *redis.Client, agentCli agent.AgentInterface, settings repository.ServerSettingsRepository, log *slog.Logger) gin.HandlerFunc {
	if log == nil {
		log = slog.Default()
	}
	if rdb == nil || agentCli == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		defer c.Next()
		claims := ginctx.Claims(c)
		if claims == nil || claims.UserID == "" {
			return
		}
		// GH #709: auto-allowlist ADMIN logins ONLY. A tenant login used to add
		// the source IP to the server-wide CrowdSec allowlist, so a compromised
		// tenant account/SSH key could shield the attacker's public IP from ALL
		// CrowdSec decisions for the TTL. Admins are trusted operators who must
		// not be locked out; tenants are not, and a tenant tripping CrowdSec
		// SHOULD still be actioned.
		if !claims.IsAdmin {
			return
		}
		cookie, err := c.Cookie("ory_kratos_session")
		if err != nil || cookie == "" {
			return
		}
		ip := clientIP(c)
		if !whitelistableIP(ip) {
			return
		}

		// Dedup per (session, IP) so we don't shell out to cscli on every
		// request — once per dedup window is enough to keep the TTL fresh.
		digest := sha256.Sum256([]byte(cookie + "|" + ip))
		key := "jabali:login-wl-seen:" + hex.EncodeToString(digest[:16])
		setCtx, cancel := context.WithTimeout(c.Request.Context(), 500*time.Millisecond)
		defer cancel()
		ok, err := rdb.SetNX(setCtx, key, "1", loginWhitelistDedup).Result()
		if err != nil {
			log.Debug("login-whitelist: redis setnx failed", "err", err)
			return
		}
		if !ok {
			return
		}

		// Read the toggle/TTL only once per dedup window (we're past the SetNX
		// gate), so the uncached server_settings Get stays off the hot path —
		// at most once per 24h per session, not per request. Default-safe: if
		// settings are unavailable, fall back to enabled + the 7d default so a
		// transient DB blip doesn't silently drop protection.
		ttlHours := loginWhitelistDefaultTTLHours
		if settings != nil {
			sCtx, sCancel := context.WithTimeout(c.Request.Context(), 500*time.Millisecond)
			s, sErr := settings.Get(sCtx)
			sCancel()
			if sErr == nil && s != nil {
				if !s.CrowdsecLoginAllowlistEnabled {
					// Disabled: just return. We intentionally KEEP the dedup key
					// so we don't re-read server_settings on every subsequent
					// request (the whole reason the read sits past the SetNX
					// gate). Trade-off: re-enabling reaches an already-active
					// session within the dedup window (<=24h); new sessions get
					// it immediately.
					return
				}
				if s.CrowdsecLoginAllowlistTTLHours > 0 {
					ttlHours = s.CrowdsecLoginAllowlistTTLHours
				}
			}
		}
		ttl := strconv.Itoa(ttlHours) + "h"

		email := claims.Email
		logger := log
		go func() {
			addCtx, addCancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer addCancel()
			_, err := agentCli.Call(addCtx, "security.crowdsec.allowlists.add", map[string]any{
				"value":      ip,
				"reason":     "auto-whitelist: login " + truncate(email, 120),
				"expiration": ttl,
			})
			if err != nil {
				// GH #357: no CrowdSec on this box (installed without the
				// security module). Keep the dedup guard: the next request
				// would get the same answer, so ask once per dedup window, as
				// with the toggle off. Before, every admin request made an
				// agent call and logged a warning.
				var ae *agent.AgentError
				if errors.As(err, &ae) && ae.Code == agent.CodeFailedPrecondition && ae.Message == agentwire.MsgCrowdSecNotInstalled {
					logger.Debug("login-whitelist: crowdsec not installed; skipped", "ip", ip)
					return
				}
				// Roll back the dedup guard so the next request retries.
				delCtx, delCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
				defer delCancel()
				_ = rdb.Del(delCtx, key).Err()
				logger.Warn("login-whitelist: allowlist add failed", "ip", ip, "err", err)
			}
		}()
	}
}

// whitelistableIP returns true only for a routable IP worth allowlisting.
// Loopback / unspecified / link-local / private addresses are skipped — they
// are never the target of a CrowdSec public-IP decision, so allowlisting them
// is pointless (and would bloat the list).
func whitelistableIP(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	// Skip only TRULY-local addresses that are never a real remote client.
	// GH: private/LAN IPs (192.168/10/172.16) ARE allowlisted — the panel is
	// commonly reached over a LAN/VPN, and (combined with the admin-only gate)
	// the operator's own login IP should show up + be exempt. Note: this means
	// CrowdSec will never ban an allowlisted LAN IP; bounded because only
	// authenticated ADMIN logins reach here (#709).
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return true
}
