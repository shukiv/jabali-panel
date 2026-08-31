package api

import (
	"log/slog"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// SSH-login notification ignore list (GH #1310-adjacent, "drfeed spam"). The
// admin manages a set of SSH usernames whose successful logins never notify —
// so a DR feed's SSH pull loop, or any noisy service account, can be silenced
// without disabling ssh.login for everyone. Surfaced under Notifications →
// Events → SSH login. The list is persisted on server_settings and read by the
// ssh.login eventsource.

// sshIgnoreUsernameRe bounds a stored username: no delimiters (comma / newline /
// whitespace) that would corrupt the stored form, and no surprises.
var sshIgnoreUsernameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

const maxSSHIgnoreAccounts = 200

// SSHLoginIgnoreHandlerConfig wires the handler.
type SSHLoginIgnoreHandlerConfig struct {
	Settings repository.ServerSettingsRepository
	Log      *slog.Logger
}

// RegisterSSHLoginIgnoreRoutes mounts:
//
//	GET /admin/settings/ssh-login-ignore
//	PUT /admin/settings/ssh-login-ignore
func RegisterSSHLoginIgnoreRoutes(g *gin.RouterGroup, cfg SSHLoginIgnoreHandlerConfig) {
	if cfg.Settings == nil {
		return
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &sshLoginIgnoreHandler{cfg: cfg}
	grp := g.Group("/admin/settings/ssh-login-ignore")
	grp.Use(middleware.RequireAdmin())
	grp.GET("", h.get)
	grp.PUT("", h.put)
}

type sshLoginIgnoreHandler struct {
	cfg SSHLoginIgnoreHandlerConfig
}

type sshLoginIgnoreDTO struct {
	Accounts []string `json:"accounts"`
}

func (h *sshLoginIgnoreHandler) get(c *gin.Context) {
	s, err := h.cfg.Settings.Get(c.Request.Context())
	if err != nil || s == nil {
		h.cfg.Log.Error("ssh-login-ignore get: settings load failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, sshLoginIgnoreDTO{
		Accounts: models.ParseSSHIgnoreAccounts(s.SSHLoginIgnoreAccounts),
	})
}

func (h *sshLoginIgnoreHandler) put(c *gin.Context) {
	var req sshLoginIgnoreDTO
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if len(req.Accounts) > maxSSHIgnoreAccounts {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "too_many", "detail": "too many ignored accounts"})
		return
	}
	for _, a := range req.Accounts {
		if !sshIgnoreUsernameRe.MatchString(a) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_username", "detail": "invalid username: " + a})
			return
		}
	}

	s, err := h.cfg.Settings.Get(c.Request.Context())
	if err != nil || s == nil {
		h.cfg.Log.Error("ssh-login-ignore put: settings load failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	// Normalise (trim/dedupe/sort) before persisting.
	s.SSHLoginIgnoreAccounts = models.JoinSSHIgnoreAccounts(req.Accounts)
	if err := h.cfg.Settings.Upsert(c.Request.Context(), s); err != nil {
		h.cfg.Log.Error("ssh-login-ignore put: settings upsert failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, sshLoginIgnoreDTO{
		Accounts: models.ParseSSHIgnoreAccounts(s.SSHLoginIgnoreAccounts),
	})
}
