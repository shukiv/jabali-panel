package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

type reverseProxyPortRequest struct {
	Port int `json:"port" binding:"required"`
}

// setReverseProxyPort serves PUT /domains/:id/reverse-proxy-port (GH #2544):
// move a reverse-proxy domain to another loopback port after create. Owner-
// scoped like rename (admin or the domain's own tenant). The port passes the
// gates a port chosen at create does (domainops.ChangeReverseProxyPort), and
// the vhost re-renders on the reconciler's next pass.
func (h *domainHandler) setReverseProxyPort(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	domain, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	var req reverseProxyPortRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "message": "port is required"})
		return
	}

	oldPort := domain.ReverseProxyPort
	if err := domainops.ChangeReverseProxyPort(ctx, h.cfg.Domains, h.cfg.Agent, domain, req.Port); err != nil {
		status, code, msg := reverseProxyPortChangeError(err)
		if status == http.StatusInternalServerError {
			slog.Error("reverse-proxy port change failed", "domain_id", domain.ID, "err", err)
		}
		c.JSON(status, gin.H{"error": code, "message": msg})
		return
	}
	if domain.ReverseProxyPort != oldPort && h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(domain.ID)
	}
	c.JSON(http.StatusOK, h.enrichDomainResponse(ctx, *domain))
}

// reverseProxyPortChangeError maps a change failure to status, code and
// message. The codes match the create door's for the same port problems.
func reverseProxyPortChangeError(err error) (int, string, string) {
	var invalid *domainops.InvalidPortError
	switch {
	case errors.Is(err, domainops.ErrNotReverseProxy):
		return http.StatusConflict, "not_reverse_proxy", "this domain serves its own files, not a reverse proxy"
	case errors.As(err, &invalid):
		return http.StatusBadRequest, "reverse_proxy_port_invalid", invalid.Reason.Error()
	case errors.Is(err, domainops.ErrReverseProxyPortSystemBound):
		return http.StatusConflict, "reverse_proxy_port_system_bound", "that port is already in use by a system service — choose another"
	case errors.Is(err, domainops.ErrReverseProxyPortInUse):
		return http.StatusConflict, "reverse_proxy_port_in_use", "that port is already assigned to another domain — choose another"
	default:
		return http.StatusInternalServerError, "internal", "the port could not be changed"
	}
}
