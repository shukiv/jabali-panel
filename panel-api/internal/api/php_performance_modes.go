package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// PHPPerformanceModeHandlerConfig wires the admin preset-editor routes.
type PHPPerformanceModeHandlerConfig struct {
	Modes repository.PHPPerformanceModeRepository
}

type phpPerformanceModeHandler struct {
	cfg PHPPerformanceModeHandlerConfig
}

// RegisterPHPPerformanceModeRoutes mounts the admin-only preset editor:
//   - GET /php-performance-modes           list the 4 presets
//   - PUT /php-performance-modes/:mode      edit one preset's pm.*
//
// GH #339 phase 2, Step 2. Admin-only — these are the ceiling templates L1
// users select from; the values are re-clamped per-package at apply.
func RegisterPHPPerformanceModeRoutes(g *gin.RouterGroup, cfg PHPPerformanceModeHandlerConfig) {
	h := &phpPerformanceModeHandler{cfg: cfg}
	grp := g.Group("/php-performance-modes", middleware.RequireAdmin())
	grp.GET("", h.list)
	grp.PUT("/:mode", h.update)
}

func (h *phpPerformanceModeHandler) list(c *gin.Context) {
	rows, err := h.cfg.Modes.GetAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
}

type updatePerformanceModeRequest struct {
	PmMode                         string `json:"pm_mode"`
	PmMaxChildren                  uint32 `json:"pm_max_children"`
	PmStartServers                 uint32 `json:"pm_start_servers"`
	PmMinSpareServers              uint32 `json:"pm_min_spare_servers"`
	PmMaxSpareServers              uint32 `json:"pm_max_spare_servers"`
	PmMaxRequests                  uint32 `json:"pm_max_requests"`
	RequestTerminateTimeoutSeconds uint32 `json:"request_terminate_timeout_seconds"`
	ProcessIdleTimeoutSeconds      uint32 `json:"process_idle_timeout_seconds"`
}

func (h *phpPerformanceModeHandler) update(c *gin.Context) {
	mode := c.Param("mode")
	existing, err := h.cfg.Modes.Get(c.Request.Context(), mode)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "mode_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	var req updatePerformanceModeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	// Validate the preset itself against the admin ceiling + the FPM dynamic
	// constraint, reusing resolvePMTuning. A preset is a template; per-package
	// clamping happens later at apply.
	if req.PmMaxChildren == 0 {
		req.PmMaxChildren = 10
	}
	if req.PmMaxChildren > phpPoolAdminMaxChildrenCap {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pm_max_children_too_high"})
		return
	}
	if msg, ok := resolvePMTuning(req.PmMode, req.PmMaxChildren,
		&req.PmStartServers, &req.PmMinSpareServers, &req.PmMaxSpareServers,
		&req.PmMaxRequests, &req.RequestTerminateTimeoutSeconds); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pm_tuning_invalid", "detail": msg})
		return
	}
	if req.ProcessIdleTimeoutSeconds == 0 {
		req.ProcessIdleTimeoutSeconds = 60
	}
	m := &models.PHPPerformanceMode{
		Mode:                           existing.Mode,
		PmMode:                         req.PmMode,
		PmMaxChildren:                  req.PmMaxChildren,
		PmStartServers:                 req.PmStartServers,
		PmMinSpareServers:              req.PmMinSpareServers,
		PmMaxSpareServers:              req.PmMaxSpareServers,
		PmMaxRequests:                  req.PmMaxRequests,
		RequestTerminateTimeoutSeconds: req.RequestTerminateTimeoutSeconds,
		ProcessIdleTimeoutSeconds:      req.ProcessIdleTimeoutSeconds,
	}
	if err := h.cfg.Modes.Update(c.Request.Context(), m); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": m})
}
