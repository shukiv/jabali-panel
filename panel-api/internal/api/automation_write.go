// automation_write.go — JAB-140 shared write pipeline for the Automation API.
//
// A write-scoped automation token is a headless, server-wide admin key, so every
// mutation runs through this spine: HMAC (RequireAutomationHMAC, already applied)
// → requireWriteScope (scope + writes_enabled) → write rate limit → bindSigned +
// validate → do the action via the SAME repo/service the GUI uses → auditWrite.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- shared response envelope (JAB-140 contract) ---

func autoOK(c *gin.Context, msg string) {
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": "done", "message": msg})
}

// autoOKWarnings is autoOK plus a non-empty `warnings` map (step-named,
// best-effort failures from a multi-step lifecycle op). Empty map → omitted, so
// the clean-path envelope is byte-identical to autoOK.
func autoOKWarnings(c *gin.Context, msg string, warnings map[string]string) {
	body := gin.H{"ok": true, "status": "done", "message": msg}
	if len(warnings) > 0 {
		body["warnings"] = warnings
	}
	c.JSON(http.StatusOK, body)
}

// lifecycleWarnings collapses the userops kratos/domain/os warning strings into a
// keyed map, dropping the empties. Returns nil when every step succeeded.
func lifecycleWarnings(kratos, domain, os string) map[string]string {
	w := map[string]string{}
	if kratos != "" {
		w["kratos"] = kratos
	}
	if domain != "" {
		w["domain"] = domain
	}
	if os != "" {
		w["os"] = os
	}
	if len(w) == 0 {
		return nil
	}
	return w
}

func autoAsync(c *gin.Context, opID, msg string) {
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "operation_id": opID, "status": "pending", "message": msg})
}

// autoErr writes {ok:false, error:<code>, message} and aborts. code ∈
// {scope_denied, not_found, conflict, unsupported, rate_limited, internal}.
func autoErr(c *gin.Context, httpStatus int, code, msg string) {
	c.AbortWithStatusJSON(httpStatus, gin.H{"ok": false, "error": code, "message": msg})
}

// requireWriteScope gates a write route: the verified token must hold `scope`
// AND have writes_enabled. Deny by default. writes_disabled reuses the
// scope_denied code (per the issue's enumerated set) with a distinct message.
func requireWriteScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		if tok == nil {
			autoErr(c, http.StatusUnauthorized, "scope_denied", "automation token required")
			return
		}
		if !tok.WritesEnabled {
			autoErr(c, http.StatusForbidden, "scope_denied", "writes are disabled for this token")
			return
		}
		if !tok.Scopes.Has(scope) {
			autoErr(c, http.StatusForbidden, "scope_denied", "scope "+scope+" not granted to token")
			return
		}
		c.Next()
	}
}

// bindSigned unmarshals the EXACT signed request body (from RequireAutomationHMAC)
// into v — never a re-read of the request stream, so what's validated equals what
// was signed. Empty body leaves v at its zero value (ok for optional-body routes).
// On malformed JSON it writes 422 and returns false.
func bindSigned(c *gin.Context, v any) bool {
	body := middleware.SignedBody(c)
	if len(body) == 0 {
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		autoErr(c, http.StatusUnprocessableEntity, "unsupported", "invalid request body")
		return false
	}
	return true
}

// auditWrite records exactly one M49 audit row for a write (success OR failure):
// actor = automation (token id in Meta), client IP, request id, action, target,
// result. Best-effort; never blocks the request.
func auditWrite(c *gin.Context, audits repository.AuditEventRepository, tok *models.AutomationToken, action, targetType, targetID, result string) {
	if audits == nil || tok == nil {
		return
	}
	ip := middleware.RealClientIP(c)
	meta, _ := json.Marshal(map[string]string{"token_id": tok.ID})
	ev := &models.AuditEvent{
		ID:         ids.NewULID(),
		TS:         time.Now().UTC(),
		ActorKind:  models.AuditActorAutomation,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Result:     result,
		SourceIP:   &ip,
		Meta:       meta,
	}
	if reqID := ginctx.RequestID(c); reqID != "" {
		ev.RequestID = &reqID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = audits.Create(ctx, ev)
}

// writeRateLimiter is a per-token token-bucket for write requests (read floods
// are cheap; write floods provision/mutate). Buckets are created lazily.
type writeRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rate.Limiter
	r       rate.Limit
	b       int
}

// newWriteRateLimiter allows `perMinute` sustained writes per token with `burst`.
func newWriteRateLimiter(perMinute, burst int) *writeRateLimiter {
	if perMinute < 1 {
		perMinute = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &writeRateLimiter{
		buckets: map[string]*rate.Limiter{},
		r:       rate.Every(time.Minute / time.Duration(perMinute)),
		b:       burst,
	}
}

func (l *writeRateLimiter) allow(tokenID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	lim, ok := l.buckets[tokenID]
	if !ok {
		lim = rate.NewLimiter(l.r, l.b)
		l.buckets[tokenID] = lim
	}
	return lim.Allow()
}

// middleware trips 429 rate_limited when the per-token bucket is empty.
func (l *writeRateLimiter) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if tok := middleware.AutomationToken(c); tok != nil && !l.allow(tok.ID) {
			autoErr(c, http.StatusTooManyRequests, "rate_limited", "write rate limit exceeded")
			return
		}
		c.Next()
	}
}

// AutomationNotifier is the narrow M14 publish surface the automation write layer
// needs (satisfied by *notifications.Queue). Nil → notifications are skipped.
type AutomationNotifier interface {
	Publish(ctx context.Context, env notifications.Envelope) (string, error)
}

// notifyWrite broadcasts a high-impact automation write to admins via M14: empty
// UserID makes the dispatcher bell every admin and fan out to enabled channels.
// Best-effort — the audit row remains the authoritative record.
func notifyWrite(cfg AutomationConfig, kind, severity, title, body, deeplink string) {
	if cfg.Notify == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = cfg.Notify.Publish(ctx, notifications.Envelope{
		EventKind: kind,
		Severity:  severity,
		Title:     title,
		Body:      body,
		Deeplink:  deeplink,
	})
}
