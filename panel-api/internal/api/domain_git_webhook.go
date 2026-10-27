package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/gitdeploy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2277 (ADR-0178): a domain's deploy-on-push webhook.
//
// The owner creates it under /domains/:id/git/webhook and gives its URL and
// secret to the Git host. The host posts every push to the URL; a push to
// the domain's branch, signed with the secret, deploys that branch as the
// domain's owner, exactly as the Deploy button does. A push that arrives
// while a deployment is running waits for it, and pushes meanwhile collapse
// into one more deployment (the reconciler starts it).
//
// GitLab turns a webhook off after four answers in a row outside 2xx, so
// every signed delivery the panel chooses not to act on (another branch, a
// tag, a ping, a delivery sent again, a push that waits) is answered 2xx
// with what happened. Only what won't change by itself is a 4xx: a bad
// signature, an unknown webhook, a package that no longer allows Git deploy.

// --- the owner's routes ---

// gitWebhookView is a domain's webhook on the wire. Secret is only in the
// answer that generated it.
type gitWebhookView struct {
	Configured bool   `json:"configured"`
	URL        string `json:"url,omitempty"`
	Secret     string `json:"secret,omitempty"`
	// Queued: a push waits for the running deployment to end.
	Queued         bool       `json:"queued"`
	LastDeliveryAt *time.Time `json:"last_delivery_at"`
	// LastResult is what the last signed delivery led to: started, queued,
	// ignored, duplicate, failed, or refused:<error code>.
	LastResult string     `json:"last_result"`
	CreatedAt  *time.Time `json:"created_at,omitempty"`
}

func newGitWebhookView(c *gin.Context, row *models.DomainGitWebhook) gitWebhookView {
	if row == nil {
		return gitWebhookView{}
	}
	created := row.CreatedAt
	return gitWebhookView{
		Configured: true, URL: panelBaseURL(c) + gitdeploy.WebhookPath(row.HookID),
		Queued: row.QueuedAt != nil, LastDeliveryAt: row.LastDeliveryAt, LastResult: row.LastResult,
		CreatedAt: &created,
	}
}

// webhookOf loads the domain's webhook; nil when it has none.
func (h *domainGitHandler) webhookOf(c *gin.Context, o *gitOwner) (*models.DomainGitWebhook, bool) {
	row, err := h.cfg.Webhooks.Get(c.Request.Context(), o.domain.ID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, true
	}
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "git: load the webhook", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	return row, true
}

func (h *domainGitHandler) getWebhook(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	// The previous owner's settings go, and the webhook with them.
	settings, ok := h.stored(c, o)
	if !ok {
		return
	}
	if settings == nil {
		c.JSON(http.StatusOK, gitWebhookView{})
		return
	}
	row, ok := h.webhookOf(c, o)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, newGitWebhookView(c, row))
}

type gitWebhookPutRequest struct {
	// Secret is one the Git host made: GitLab's signing token, or a
	// Bitbucket generated secret. Absent, the panel makes one and answers it
	// this once.
	Secret string `json:"secret"`
}

// putWebhook creates the domain's webhook, or gives it a new secret. The
// URL stays the same.
func (h *domainGitHandler) putWebhook(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	if _, ok := h.requireAllowed(c, o); !ok {
		return
	}
	if _, ok := h.configured(c, o); !ok {
		return
	}
	var req gitWebhookPutRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "detail": "the body must be JSON: secret (optional)"})
		return
	}
	ctx := c.Request.Context()
	secret := strings.TrimSpace(req.Secret)
	generated := secret == ""
	if generated {
		var err error
		if secret, err = gitdeploy.NewWebhookSecret(); err != nil {
			slog.ErrorContext(ctx, "git: make a webhook secret", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	} else if err := gitdeploy.ValidateWebhookSecret(secret); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_webhook_secret", "detail": err.Error()})
		return
	}
	if h.cfg.SSOKey == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sso_key_unavailable", "detail": "the panel's sso.key is not configured, so the secret can't be stored securely"})
		return
	}
	sealed, err := h.cfg.SSOKey.Seal([]byte(secret))
	if err != nil {
		slog.ErrorContext(ctx, "git: seal the webhook secret", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	row, ok := h.webhookOf(c, o)
	if !ok {
		return
	}
	status, now := http.StatusOK, time.Now().UTC()
	if row == nil {
		hookID, err := gitdeploy.NewHookID()
		if err != nil {
			slog.ErrorContext(ctx, "git: make a webhook id", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		row = &models.DomainGitWebhook{DomainID: o.domain.ID, HookID: hookID, CreatedAt: now}
		status = http.StatusCreated
	}
	row.SecretEnc, row.UpdatedAt = sealed, now
	if err := h.cfg.Webhooks.Put(ctx, row); err != nil {
		slog.ErrorContext(ctx, "git: store the webhook", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	view := newGitWebhookView(c, row)
	if generated {
		view.Secret = secret
	}
	c.JSON(status, view)
}

func (h *domainGitHandler) deleteWebhook(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	err := h.cfg.Webhooks.Delete(c.Request.Context(), o.domain.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		slog.ErrorContext(c.Request.Context(), "git: delete the webhook", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Status(http.StatusNoContent)
}

// --- deliveries ---

// gitDeliveryStore remembers delivery IDs: *redis.Client.
type gitDeliveryStore interface {
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// GitWebhookConfig wires the endpoint Git hosts post pushes to.
type GitWebhookConfig struct {
	Webhooks repository.DomainGitWebhookRepository
	Domains  repository.DomainRepository
	Users    repository.UserRepository
	// Deployer starts the deployment; its SSOKey opens the webhook secrets.
	Deployer *gitdeploy.Deployer
	// Reconciler re-applies a domain's vhost once a deployment has moved its
	// document root. nil skips that (tests).
	Reconciler gitdeploy.Scheduler
	// Deliveries remembers deliveries, so one the host sends again, or
	// someone replays, deploys once. nil (no Redis) leaves that out, said
	// once in the log.
	Deliveries gitDeliveryStore
	// Now is the clock; nil uses time.Now (tests).
	Now func() time.Time
}

const (
	// gitDeliveryTTL is how long a delivery is remembered: well past
	// GitLab's retries, which back off for up to a day.
	gitDeliveryTTL = 7 * 24 * time.Hour
	// A webhook starts at most this many deployments a minute; a push past
	// that waits, as one during a run does.
	gitWebhookStartsPerMinute = 6
	gitWebhookStartBurst      = 3
)

// RegisterGitWebhookRoutes adds the endpoint Git hosts post pushes to:
//   - POST /webhooks/git/:hookId
//
// It has no session: a delivery is believed only by its signature.
func RegisterGitWebhookRoutes(g *gin.RouterGroup, cfg GitWebhookConfig) {
	if cfg.Webhooks == nil || cfg.Domains == nil || cfg.Users == nil || cfg.Deployer == nil {
		panic("api.RegisterGitWebhookRoutes: Webhooks, Domains, Users and Deployer are required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	h := &gitWebhookHandler{cfg: cfg, starts: newWriteRateLimiter(gitWebhookStartsPerMinute, gitWebhookStartBurst)}
	g.POST("/webhooks/git/:hookId", h.deliver)
}

type gitWebhookHandler struct {
	cfg    GitWebhookConfig
	starts *writeRateLimiter
}

// noDeliveryStoreOnce says once that deliveries sent again are not
// recognised.
var noDeliveryStoreOnce sync.Once

func (h *gitWebhookHandler) deliver(c *gin.Context) {
	ctx := c.Request.Context()
	hookID := c.Param("hookId")
	if !gitdeploy.ValidHookID(hookID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "webhook_not_found"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, gitdeploy.WebhookMaxBody+1))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || len(body) > gitdeploy.WebhookMaxBody {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body_too_large"})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	hook, err := h.cfg.Webhooks.FindByHookID(ctx, hookID)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "webhook_not_found"})
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "git webhook: load", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	key := h.cfg.Deployer.SSOKey
	if key == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sso_key_unavailable"})
		return
	}
	secret, err := key.Open(hook.SecretEnc)
	if err != nil {
		slog.ErrorContext(ctx, "git webhook: open the secret", "domain", hook.DomainID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if _, err := gitdeploy.VerifyDelivery(c.Request.Header, body, string(secret), h.cfg.Now()); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid_signature"})
		return
	}

	// The domain's Git host sent this.
	now := h.cfg.Now().UTC()
	push, err := gitdeploy.ParsePush(c.Request.Header, body)
	if err != nil {
		h.record(ctx, hook.DomainID, now, models.GitWebhookRefused+"invalid_payload")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_payload", "detail": err.Error()})
		return
	}
	dom, owner, settings, ok := h.load(c, hook.DomainID)
	if !ok {
		return
	}
	if !push.Has(settings.Branch) {
		h.record(ctx, dom.ID, now, models.GitWebhookIgnored)
		c.JSON(http.StatusOK, gin.H{"result": models.GitWebhookIgnored, "detail": "not a push to the branch " + settings.Branch + " (" + push.Event + ")"})
		return
	}
	claimed, seen := h.claimDelivery(ctx, hookID, gitdeploy.DeliveryID(c.Request.Header), body)
	if seen {
		h.record(ctx, dom.ID, now, models.GitWebhookDuplicate)
		c.JSON(http.StatusOK, gin.H{"result": models.GitWebhookDuplicate, "detail": "this delivery was received before"})
		return
	}
	if !h.starts.allow(hookID) {
		h.queue(c, dom.ID, now, claimed)
		return
	}
	h.refreshActive(ctx, dom)
	run, err := h.cfg.Deployer.Deploy(ctx, dom, owner, models.GitDeploySourceWebhook)
	var refused *gitdeploy.Refused
	switch {
	case err == nil:
		h.record(ctx, dom.ID, now, models.GitWebhookStarted)
		c.JSON(http.StatusAccepted, gin.H{"result": models.GitWebhookStarted, "deployment_id": run.ID})
	case errors.Is(err, repository.ErrGitDeploymentActive):
		h.queue(c, dom.ID, now, claimed)
	case errors.As(err, &refused):
		h.record(ctx, dom.ID, now, models.GitWebhookRefused+refused.Code)
		respondGitRefused(c, err)
	default:
		if ae, ok := gitdeploy.Refusal(err); ok {
			// The agent's reason, for the user; stored on the failed run too.
			h.record(ctx, dom.ID, now, models.GitWebhookRefused+ae.Code)
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": ae.Code, "detail": ae.Message})
			return
		}
		h.forgetDelivery(ctx, claimed)
		h.record(ctx, dom.ID, now, models.GitWebhookFailed)
		logAgentError(c, "git_deploy_start_failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "git_deploy_start_failed"})
	}
}

// load reads the webhook's domain, its owner, and the Git settings, which
// must be the owner's.
func (h *gitWebhookHandler) load(c *gin.Context, domainID string) (*models.Domain, *models.User, *models.DomainGitRepo, bool) {
	ctx := c.Request.Context()
	dom, err := h.cfg.Domains.FindByID(ctx, domainID)
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "webhook_not_found"})
		return nil, nil, nil, false
	}
	if err != nil {
		slog.ErrorContext(ctx, "git webhook: load the domain", "domain", domainID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, nil, nil, false
	}
	owner, err := h.cfg.Users.FindByID(ctx, dom.UserID)
	if err != nil || owner == nil {
		slog.ErrorContext(ctx, "git webhook: load the domain's owner", "domain", dom.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, nil, nil, false
	}
	settings, err := h.cfg.Deployer.Repos.Get(ctx, dom.ID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && settings.UserID != dom.UserID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "git_not_configured"})
		return nil, nil, nil, false
	}
	if err != nil {
		slog.ErrorContext(ctx, "git webhook: load the settings", "domain", dom.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, nil, nil, false
	}
	return dom, owner, settings, true
}

// queue keeps the push for when the domain's running deployment has ended.
func (h *gitWebhookHandler) queue(c *gin.Context, domainID string, now time.Time, claimed []string) {
	ctx := c.Request.Context()
	if err := h.cfg.Webhooks.Queue(ctx, domainID, now); err != nil {
		slog.ErrorContext(ctx, "git webhook: queue a push", "domain", domainID, "error", err)
		h.forgetDelivery(ctx, claimed)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	h.record(ctx, domainID, now, models.GitWebhookQueued)
	c.JSON(http.StatusAccepted, gin.H{"result": models.GitWebhookQueued, "detail": "a deployment is in progress; this push deploys when it ends"})
}

// refreshActive brings the domain's run in progress up to date, so a run
// that has already ended doesn't make the push wait.
func (h *gitWebhookHandler) refreshActive(ctx context.Context, dom *models.Domain) {
	d := h.cfg.Deployer
	runs, _, err := d.Deployments.ListByDomain(ctx, dom.ID, dom.UserID, 0, 1)
	if err != nil || len(runs) != 1 || runs[0].State != models.GitDeployRunning {
		return
	}
	if err := gitdeploy.Refresh(ctx, d.Agent, d.Deployments, h.cfg.Reconciler, &runs[0]); err != nil {
		slog.WarnContext(ctx, "git webhook: refresh a deployment", "run", runs[0].ID, "error", err)
	}
}

func (h *gitWebhookHandler) record(ctx context.Context, domainID string, at time.Time, result string) {
	if err := h.cfg.Webhooks.RecordDelivery(ctx, domainID, at, result); err != nil {
		slog.WarnContext(ctx, "git webhook: record a delivery", "domain", domainID, "error", err)
	}
}

// claimDelivery remembers a delivery by its ID and by its signed body, and
// reports whether either came before. The body counts because a GitHub or
// Bitbucket signature covers the body alone: a captured delivery replayed
// under a new ID is still the same body. It returns the keys it took, for
// forgetDelivery. A store that fails doesn't stop the push: a delivery sent
// twice only deploys twice.
func (h *gitWebhookHandler) claimDelivery(ctx context.Context, hookID, deliveryID string, body []byte) ([]string, bool) {
	if h.cfg.Deliveries == nil {
		noDeliveryStoreOnce.Do(func() {
			slog.Warn("git webhook: no Redis configured, so a delivery sent again deploys again")
		})
		return nil, false
	}
	bodySum := sha256.Sum256(body)
	keys := []string{"jabali:gitdeploy:body:" + hookID + ":" + hex.EncodeToString(bodySum[:16])}
	if deliveryID != "" {
		idSum := sha256.Sum256([]byte(deliveryID))
		keys = append(keys, "jabali:gitdeploy:delivery:"+hookID+":"+hex.EncodeToString(idSum[:16]))
	}
	var taken []string
	seen := false
	for _, key := range keys {
		set, err := h.cfg.Deliveries.SetNX(ctx, key, 1, gitDeliveryTTL).Result()
		if err != nil {
			slog.WarnContext(ctx, "git webhook: remember a delivery", "error", err)
			continue
		}
		if set {
			taken = append(taken, key)
		} else {
			seen = true
		}
	}
	return taken, seen
}

// forgetDelivery lets the host's next try of a delivery the panel could not
// act on through.
func (h *gitWebhookHandler) forgetDelivery(ctx context.Context, keys []string) {
	if len(keys) == 0 || h.cfg.Deliveries == nil {
		return
	}
	if err := h.cfg.Deliveries.Del(context.WithoutCancel(ctx), keys...).Err(); err != nil {
		slog.WarnContext(ctx, "git webhook: forget a delivery", "error", err)
	}
}
