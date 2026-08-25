package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainPHPPoolHandlerConfig wires the domain↔pool binding routes.
//
// Agent / Users / PHPPoolIniOverrides are required so the user-driven
// version switch can fire php.pool.apply immediately, mirroring the admin
// PUT /php-pools/:id flow. Without them, a version change only updates
// the DB and waits for the next reconciler tick — and only converges if
// pool.Status was flipped to "pending" first, which the reconciler uses
// as its work filter.
type DomainPHPPoolHandlerConfig struct {
	Domains             repository.DomainRepository
	PHPPools            repository.PHPPoolRepository
	PHPPoolIniOverrides repository.PHPPoolIniOverrideRepository
	Users               repository.UserRepository
	Agent               agent.AgentInterface
}

// RegisterDomainPHPPoolRoutes adds two routes under the existing /domains
// group that bind or unbind a domain to a PHP-FPM pool. Lives in its own
// file so domains.go stays under the 800-line invariant.
//
// Routes:
//   - POST   /domains/:id/php-pool  { "pool_id": "<ulid>" }  — admin path
//   - POST   /domains/:id/php-pool  { "php_version": "8.3" } — user path
//   - DELETE /domains/:id/php-pool
//
// Both paths require the caller to own the domain (or be admin). For the
// pool_id variant, the pool must also belong to the same user that owns
// the domain — this prevents a user from pointing their domain at another
// user's pool and reading another user's docroot via PHP. The php_version
// variant looks up the domain owner's (single, per ADR-0023) pool and
// returns 409 if the requested version does not match; changing a pool's
// PHP version is an admin operation via /php-pools/:id.
func RegisterDomainPHPPoolRoutes(g *gin.RouterGroup, cfg DomainPHPPoolHandlerConfig) {
	h := &domainPHPPoolHandler{cfg: cfg}
	g.POST("/domains/:id/php-pool", h.bind)
	g.DELETE("/domains/:id/php-pool", h.unbind)
}

type domainPHPPoolHandler struct{ cfg DomainPHPPoolHandlerConfig }

// bindDomainPHPPoolRequest accepts either PoolID (admin-style, explicit
// pool selection) or PHPVersion (user-style, resolved to the owner's
// single pool). Exactly one must be non-empty.
type bindDomainPHPPoolRequest struct {
	PoolID     string `json:"pool_id"`
	PHPVersion string `json:"php_version"`
}

func (h *domainPHPPoolHandler) bind(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req bindDomainPHPPoolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	// Require exactly one of pool_id or php_version.
	if (req.PoolID == "") == (req.PHPVersion == "") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pool_id_or_php_version_required"})
		return
	}

	ctx := c.Request.Context()
	domainID := c.Param("id")

	dom, err := h.cfg.Domains.FindByID(ctx, domainID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		slog.ErrorContext(ctx, "bind php-pool: load domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Resolve request → pool. Two paths:
	//   - pool_id: explicit selection; pool must belong to the domain owner.
	//   - php_version: look up the domain owner's single pool; require its
	//     version to match. Changing a pool's PHP version is admin-only
	//     (via PUT /php-pools/:id) per ADR-0023.
	var pool *models.PHPPool
	if req.PoolID != "" {
		pool, err = h.cfg.PHPPools.FindByID(ctx, req.PoolID)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
				return
			}
			slog.ErrorContext(ctx, "bind php-pool: load pool", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		// Cross-user binding is refused even for admins — an admin who wants
		// to run alice's domain in bob's pool almost certainly has a bug, not
		// an intent. If the use case ever comes up, it gets its own endpoint.
		if pool.UserID != dom.UserID {
			c.JSON(http.StatusForbidden, gin.H{"error": "pool_not_owned_by_domain_user"})
			return
		}
	} else {
		// GH #329: per-domain PHP version. Bind this domain to the (user,
		// version) pool — creating it if needed — instead of mutating the
		// shared pool (which would change every sibling domain's version).
		// The strict version-format guard doubles as the injection defense:
		// the value flows into an FPM pool slug + systemd instance name
		// downstream, so it must never carry path/traversal characters.
		if !cliPhpVersionRe.MatchString(req.PHPVersion) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_php_version"})
			return
		}
		pools, lerr := h.cfg.PHPPools.ListByUserID(ctx, dom.UserID)
		if lerr != nil {
			slog.ErrorContext(ctx, "bind php-pool: list user pools", "error", lerr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if len(pools) == 0 {
			// The reconciler provisions every user's default pool; until it
			// exists there is nothing to base a versioned pool on.
			c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found_for_user"})
			return
		}
		defaultPool := &pools[0] // earliest created = default (slug == username)
		switch {
		case req.PHPVersion == defaultPool.PHPVersion:
			// The user's default version — bind to the default pool itself
			// (keeps the legacy per-user socket; nothing to create).
			pool = defaultPool
		default:
			existing, ferr := h.cfg.PHPPools.FindByUserAndVersion(ctx, dom.UserID, req.PHPVersion)
			if ferr == nil {
				pool = existing
			} else if errors.Is(ferr, repository.ErrNotFound) {
				// Create the versioned pool (pending). The reconciler applies
				// it with the versioned slug/socket and regenerates this
				// domain's vhost within a tick. pm_* copied from the default.
				// JAB-344: clone the COMPLETE tuning model (incl. performance_mode)
				// via the shared constructor so HTTP and CLI produce identical
				// versioned pools.
				pool = models.NewVersionedPHPPool(ids.NewULID(), req.PHPVersion, defaultPool)
				if err := h.cfg.PHPPools.Create(ctx, pool); err != nil {
					slog.ErrorContext(ctx, "bind php-pool: create versioned pool", "error", err, "php_version", req.PHPVersion)
					c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
					return
				}
				slog.InfoContext(ctx, "php_pool.versioned_created", "user_id", dom.UserID, "pool_id", pool.ID, "php_version", req.PHPVersion)
			} else {
				slog.ErrorContext(ctx, "bind php-pool: lookup versioned pool", "error", ferr)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
		}
	}

	poolID := pool.ID
	oldPoolID := dom.PHPPoolID
	// SetPHPPoolID is the dedicated bind path. Domain.Update's column
	// allowlist intentionally excludes php_pool_id so generic PATCH
	// cannot mutate the binding — bind/unbind go through this method.
	if err := h.cfg.Domains.SetPHPPoolID(ctx, dom.ID, &poolID); err != nil {
		slog.ErrorContext(ctx, "bind php-pool: update domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	dom.PHPPoolID = &poolID

	oldPoolIDStr := ""
	if oldPoolID != nil {
		oldPoolIDStr = *oldPoolID
	}
	slog.InfoContext(ctx, "domain_php_pool.bound", "user_id", claims.UserID, "domain_id", dom.ID, "pool_id", poolID, "old_pool_id", oldPoolIDStr, "new_pool_id", poolID)

	c.JSON(http.StatusOK, gin.H{
		"domain_id":   dom.ID,
		"php_pool_id": poolID,
	})
}

func (h *domainPHPPoolHandler) unbind(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	ctx := c.Request.Context()
	domainID := c.Param("id")

	dom, err := h.cfg.Domains.FindByID(ctx, domainID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		slog.ErrorContext(ctx, "unbind php-pool: load domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	oldPoolID := dom.PHPPoolID
	oldPoolIDStr := ""
	if oldPoolID != nil {
		oldPoolIDStr = *oldPoolID
	}
	// Use the dedicated method for the same reason as bind — see above.
	if err := h.cfg.Domains.SetPHPPoolID(ctx, dom.ID, nil); err != nil {
		slog.ErrorContext(ctx, "unbind php-pool: update domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	dom.PHPPoolID = nil

	slog.InfoContext(ctx, "domain_php_pool.unbound", "user_id", claims.UserID, "domain_id", dom.ID, "old_pool_id", oldPoolIDStr, "new_pool_id", "")

	c.JSON(http.StatusOK, gin.H{
		"domain_id":   dom.ID,
		"php_pool_id": nil,
	})
}
