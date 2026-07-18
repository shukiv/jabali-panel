package api

import (
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/skelpath"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// AccountSkeletonHandlerConfig wires the admin skeleton CRUD surface
// (GH #465). The skeleton is the tree of starter files copied into a new
// domain's docroot on creation; this endpoint manages that tree.
type AccountSkeletonHandlerConfig struct {
	Repo repository.AccountSkeletonRepository
	Log  *slog.Logger
}

func RegisterAccountSkeletonRoutes(g *gin.RouterGroup, cfg AccountSkeletonHandlerConfig) {
	if cfg.Repo == nil {
		return
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	h := &accountSkeletonHandler{cfg: cfg}
	admin := g.Group("/admin/settings/account-skeleton")
	admin.Use(middleware.RequireAdmin())
	admin.GET("", h.list)
	admin.GET("/file", h.getFile)
	admin.PUT("/file", h.putFile)
	admin.DELETE("/file", h.deleteFile)
}

type accountSkeletonHandler struct{ cfg AccountSkeletonHandlerConfig }

type skeletonListResponse struct {
	Files      []repository.SkeletonEntry `json:"files"`
	TotalBytes int64                      `json:"total_bytes"`
	Count      int64                      `json:"count"`
	Caps       skeletonCaps               `json:"caps"`
}

type skeletonCaps struct {
	MaxFileBytes  int `json:"max_file_bytes"`
	MaxTotalBytes int `json:"max_total_bytes"`
	MaxFiles      int `json:"max_files"`
}

func (h *accountSkeletonHandler) list(c *gin.Context) {
	ctx := c.Request.Context()
	files, err := h.cfg.Repo.ListPaths(ctx)
	if err != nil {
		h.cfg.Log.Error("skeleton list failed", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	total, _ := h.cfg.Repo.TotalBytes(ctx)
	count, _ := h.cfg.Repo.Count(ctx)
	c.JSON(http.StatusOK, skeletonListResponse{
		Files:      files,
		TotalBytes: total,
		Count:      count,
		Caps: skeletonCaps{
			MaxFileBytes:  models.AccountSkeletonMaxFileBytes,
			MaxTotalBytes: models.AccountSkeletonMaxTotalBytes,
			MaxFiles:      models.AccountSkeletonMaxFiles,
		},
	})
}

func (h *accountSkeletonHandler) getFile(c *gin.Context) {
	p := c.Query("path")
	if err := skelpath.Validate(p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path", "detail": err.Error()})
		return
	}
	row, err := h.cfg.Repo.Get(c.Request.Context(), p)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"rel_path":       row.RelPath,
		"content_base64": base64.StdEncoding.EncodeToString(row.Content),
		"size":           len(row.Content),
	})
}

type skeletonPutRequest struct {
	RelPath       string `json:"rel_path"`
	ContentBase64 string `json:"content_base64"`
}

func (h *accountSkeletonHandler) putFile(c *gin.Context) {
	var req skeletonPutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "detail": err.Error()})
		return
	}
	if err := skelpath.Validate(req.RelPath); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_path", "detail": err.Error()})
		return
	}
	content, err := base64.StdEncoding.DecodeString(req.ContentBase64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_base64"})
		return
	}
	if len(content) > models.AccountSkeletonMaxFileBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file_too_large", "detail": "max 1 MiB per file"})
		return
	}

	ctx := c.Request.Context()
	// Compute the delta against the current row (if any) so a replace that
	// shrinks a file doesn't count double toward the total cap.
	var existingSize int
	isNew := true
	if cur, gerr := h.cfg.Repo.Get(ctx, req.RelPath); gerr == nil {
		existingSize = len(cur.Content)
		isNew = false
	}
	if isNew {
		count, _ := h.cfg.Repo.Count(ctx)
		if count >= models.AccountSkeletonMaxFiles {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "too_many_files", "detail": "max 200 files"})
			return
		}
	}
	total, _ := h.cfg.Repo.TotalBytes(ctx)
	if total-int64(existingSize)+int64(len(content)) > models.AccountSkeletonMaxTotalBytes {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "skeleton_too_large", "detail": "max 20 MiB total"})
		return
	}

	if err := h.cfg.Repo.Upsert(ctx, req.RelPath, content); err != nil {
		h.cfg.Log.Error("skeleton upsert failed", "err", err, "path", req.RelPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save_failed"})
		return
	}
	status := http.StatusOK
	if isNew {
		status = http.StatusCreated
	}
	c.JSON(status, gin.H{"status": "ok", "rel_path": req.RelPath, "size": len(content)})
}

func (h *accountSkeletonHandler) deleteFile(c *gin.Context) {
	p := c.Query("path")
	if err := skelpath.Validate(p); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_path", "detail": err.Error()})
		return
	}
	if err := h.cfg.Repo.Delete(c.Request.Context(), p); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed"})
		return
	}
	c.Status(http.StatusNoContent)
}
