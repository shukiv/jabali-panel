package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"

	"github.com/gin-gonic/gin"
)

// MailboxPasswordRateLimitedMarker is logged, with client_ip, when an address
// sends more password changes to the mailbox password page than its budget.
// CrowdSec's jabali/mailbox-password-bf scenario bans an address that keeps
// at it.
const MailboxPasswordRateLimitedMarker = "mailbox_password_rate_limited"

// AllowMailboxPassword spends one try at the mailbox password page (GH #2543)
// from the request's address and, when the form names one, one from that
// mailbox's own budget.
//
// The address budget is the panel sign-in's (the Kratos tier), in its own
// bucket: an office behind one address, its users all changing passwords the
// same morning, gets the room a panel sign-in gets, and isn't banned for it.
// The mailbox budget is twice the Strict tier's, across all addresses: a typo
// or two, not a guessing run, however many addresses the guesses come from.
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
	// Folded as the mailboxes table compares addresses (utf8mb4_unicode_ci:
	// case, accents and width don't count), so a variant spelling that finds
	// the same mailbox spends the same budget.
	name := foldAccountName(email)
	if name == nil {
		return true
	}
	sum := sha256.Sum256(name)
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
