package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"
)

// MailboxPasswordRateLimitedMarker is logged, with client_ip, when an address
// sends more password changes to the mailbox password page than its budget.
// CrowdSec's jabali/mailbox-password-bf scenario bans an address that keeps
// at it.
const MailboxPasswordRateLimitedMarker = "mailbox_password_rate_limited"

// AllowMailboxPassword spends one try at the mailbox password page (GH #2543)
// from the request's address and, when the form names one, one from that
// mailbox's own budget, which is twice an address's. Each try at the page
// checks a password, so both budgets are the Strict tier's: a typo or two,
// not a guessing run. The mailbox budget holds guessing one mailbox from many
// addresses to what two addresses may try.
//
// An address over its budget is logged with MailboxPasswordRateLimitedMarker.
// A mailbox over its budget is logged with its own marker and a short hash of
// the address, never the address itself: the owner retrying while someone
// else guesses must not have the owner's address banned.
func (l *RateLimiter) AllowMailboxPassword(c *gin.Context, email string, log *slog.Logger) bool {
	ip := clientIP(c)
	if !l.limiterFor(ip, tierMailboxPassword).Allow() {
		if log != nil {
			log.Warn("mailbox password change rate limited",
				"marker", MailboxPasswordRateLimitedMarker, "client_ip", ip)
		}
		return false
	}
	name := strings.ToLower(strings.TrimSpace(email))
	if name == "" {
		return true
	}
	sum := sha256.Sum256([]byte(name))
	h := hex.EncodeToString(sum[:16])
	if !l.limiterFor("mailbox_password:"+h, tierMailboxPasswordAccount).Allow() {
		if log != nil {
			log.Warn("mailbox password change rate limited for the mailbox",
				"marker", "mailbox_password_account_rate_limited",
				"client_ip", ip, "account_hash", h[:12])
		}
		return false
	}
	return true
}
