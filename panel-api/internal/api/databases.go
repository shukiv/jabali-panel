package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DatabaseHandlerConfig plugs the database handlers into the router.
type DatabaseHandlerConfig struct {
	Databases         repository.DatabaseRepository
	DatabaseUsers     repository.DatabaseUserRepository
	DatabaseGrants    repository.DatabaseUserGrantRepository
	WordPressInstalls repository.WordPressInstallRepository
	Users             repository.UserRepository
	Packages          repository.PackageRepository
	ServerSettings    repository.ServerSettingsRepository
	Agent             agent.AgentInterface
}

const (
	defaultDatabasesPageSize = 20
	maxDatabasesPageSize     = 200
)

// RegisterDatabaseRoutes mounts /databases* under g.
// - GET /databases (admin: all; user: scoped to self)
// - GET /databases/:id (admin: all; user: scoped to self)
// - POST /databases (admin: all; user: own only)
// - DELETE /databases/:id (admin: all; user: own only)
// - GET /databases/:id/backup (admin: all; user: scoped to self)
// - POST /databases/:id/restore (admin: all; user: scoped to self)
func RegisterDatabaseRoutes(g *gin.RouterGroup, cfg DatabaseHandlerConfig) {
	h := &databaseHandler{cfg: cfg}

	databases := g.Group("/databases")
	databases.GET("", h.list)
	databases.GET("/:id", h.get)
	databases.POST("", h.create)
	databases.DELETE("/:id", h.delete)
	databases.GET("/:id/backup", h.backup)
	databases.POST("/:id/restore", h.restore)
}

type databaseHandler struct{ cfg DatabaseHandlerConfig }

// databaseListRow is returned by the list endpoint; it embeds the database model
// and adds a computed size_bytes field fetched from the agent.
type databaseListRow struct {
	models.Database
	SizeBytes int64 `json:"size_bytes"`
}

// ---- helpers ----

// openFile opens a file for reading. Returns an error if the file does not exist or cannot be read.
func openFile(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	return f, nil
}

// copyFile copies from src to dst using io.Copy. This streams the data without buffering
// the entire file in memory.
func copyFile(dst io.Writer, src io.Reader) (int64, error) {
	n, err := io.Copy(dst, src)
	if err != nil {
		return n, fmt.Errorf("failed to copy file: %w", err)
	}
	return n, nil
}

// deleteFile removes a file at the given path. Errors are logged but not returned.
func deleteFile(path string) error {
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}

// createDir creates a directory with mode 0700 if it does not exist.
func createDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return nil
}

// writeToFile writes the contents of a multipart file to disk at the given path with mode 0600.
// It ensures the parent directory exists.
func writeToFile(path string, src io.Reader, size int64) error {
	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	// Create the file with mode 0600
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	// Copy the multipart file to disk
	if _, err := io.Copy(f, src); err != nil {
		// Clean up the partial file
		_ = os.Remove(path)
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// ---- handlers ----

func (h *databaseHandler) list(c *gin.Context) {
	page, pageSize, opts := parseListOptions(c, defaultDatabasesPageSize, maxDatabasesPageSize)

	// Get current user claims
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var dbs []models.Database
	var total int64
	var err error

	// Admins see all databases; users see only their own
	if claims.IsAdmin {
		// Admin owner-scope via ?user_id (#483).
		if uid := c.Query("user_id"); uid != "" {
			if !ids.IsValidULID(uid) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user_id"})
				return
			}
			dbs, total, err = h.cfg.Databases.ListByUserID(c.Request.Context(), uid, opts)
		} else {
			dbs, total, err = h.cfg.Databases.List(c.Request.Context(), opts)
		}
	} else {
		dbs, total, err = h.cfg.Databases.ListByUserID(c.Request.Context(), claims.UserID, opts)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if dbs == nil {
		dbs = []models.Database{}
	}

	// For non-admins, enforce the panel-wide naming convention: a user
	// can only see databases whose names start with their linux-username
	// prefix. Belt-and-suspenders on top of the user_id FK filter — if
	// a row ever lands with the wrong user_id (e.g. via direct DB edit
	// or a legacy WP install that skipped prefixing), it stays hidden.
	if !claims.IsAdmin {
		if u, uErr := h.cfg.Users.FindByID(c.Request.Context(), claims.UserID); uErr == nil && u != nil && u.Username != nil && *u.Username != "" {
			prefix := *u.Username + "_"
			filtered := dbs[:0]
			for _, d := range dbs {
				if strings.HasPrefix(d.Name, prefix) {
					filtered = append(filtered, d)
				}
			}
			if len(filtered) != len(dbs) {
				total -= int64(len(dbs) - len(filtered))
			}
			dbs = filtered
		}
	}

	// Fetch size_bytes for each database. Use a 30-second timeout for all size calls.
	// If any call fails, degrade to size_bytes=0 for that row and log at INFO.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	rows := make([]databaseListRow, len(dbs))
	for i, db := range dbs {
		rows[i] = databaseListRow{Database: db, SizeBytes: 0}

		// Fetch size from agent
		// GH #1005: pass the engine so the agent uses pg_database_size() for a
		// Postgres database instead of MariaDB's information_schema (which has
		// no row for it and always summed to 0 B).
		result, err := h.cfg.Agent.Call(ctx, "db.size", map[string]string{"db_name": db.Name, "engine": db.Engine})
		if err != nil {
			// Log at INFO and continue with size_bytes=0
			slog.Info("failed to fetch database size",
				"db_name", db.Name,
				"error", err.Error())
			continue
		}

		// Parse the size_bytes from the response
		var resp struct {
			SizeBytes int64 `json:"size_bytes"`
		}
		if err := json.Unmarshal(result, &resp); err != nil {
			slog.Info("failed to parse database size response",
				"db_name", db.Name,
				"error", err.Error())
			continue
		}

		rows[i].SizeBytes = resp.SizeBytes
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      rows,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *databaseHandler) get(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	db, err := h.cfg.Databases.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Check authorization: admins can access any database; users can only access their own
	if !claims.IsAdmin && db.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	c.JSON(http.StatusOK, db)
}

type createDatabaseRequest struct {
	Name string `json:"name" binding:"required"`
	// Engine picks the database backend. Empty defaults to "mariadb"
	// for back-compat with pre-M37 clients. "postgres" requires
	// server_settings.postgres_enabled=true; otherwise the handler
	// returns 422 postgres_disabled.
	Engine string `json:"engine,omitempty"`
}

// create is a thin REST wrapper around panel-api/internal/dbops.Create.
// All validation, agent dispatch, prefix handling, quota enforcement,
// and the panel-side row insert live in dbops; this handler is purely
// HTTP envelope + status mapping (M41 ADR-0083 refactor).
func (h *databaseHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req createDatabaseRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	row, err := dbops.Create(c.Request.Context(), dbops.Deps{
		Users:          h.cfg.Users,
		Packages:       h.cfg.Packages,
		Databases:      h.cfg.Databases,
		ServerSettings: h.cfg.ServerSettings,
		Agent:          h.cfg.Agent,
	}, dbops.CreateInput{
		UserID:  claims.UserID,
		RawName: req.Name,
		Engine:  req.Engine,
		AsAdmin: claims.IsAdmin,
	})
	if err != nil {
		dbopsRESTError(c, err)
		return
	}
	c.JSON(http.StatusCreated, row)
}

// dbopsRESTError translates dbops sentinels to HTTP status + JSON.
func dbopsRESTError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, dbops.ErrNameInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_database_name", "detail": err.Error()})
	case errors.Is(err, dbops.ErrEngineInvalid):
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_engine", "detail": err.Error()})
	case errors.Is(err, dbops.ErrUserNotFound):
		c.JSON(http.StatusBadRequest, gin.H{"error": "user_not_found"})
	case errors.Is(err, dbops.ErrUserNoUsername):
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	case errors.Is(err, dbops.ErrPostgresOff):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "postgres_disabled", "detail": err.Error()})
	case errors.Is(err, dbops.ErrQuotaExceeded):
		c.JSON(http.StatusConflict, gin.H{"error": "quota_exceeded", "resource": "databases", "detail": err.Error()})
	case errors.Is(err, dbops.ErrNameTaken):
		c.JSON(http.StatusConflict, gin.H{"error": "database_name_exists"})
	case errors.Is(err, dbops.ErrAgentFailed):
		respondAgentErr(c, "agent_failed", err)
	case errors.Is(err, dbops.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
	}
}

func (h *databaseHandler) delete(c *gin.Context) {
	ctx := c.Request.Context()

	d, err := h.cfg.Databases.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		slog.ErrorContext(ctx, "databases.delete: FindByID failed", "err", err, "db_id", c.Param("id"))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	if !claims.IsAdmin && d.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Guard: wordpress_installs.db_id is RESTRICT. Surface a 409 with
	// the install id so the caller knows what to tear down first.
	if h.cfg.WordPressInstalls != nil {
		wp, wErr := h.cfg.WordPressInstalls.FindByDBID(ctx, d.ID)
		if wErr != nil && !isNotFound(wErr) {
			slog.ErrorContext(ctx, "databases.delete: wp in-use probe failed", "err", wErr, "db_id", d.ID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if wp != nil {
			c.JSON(http.StatusConflict, gin.H{"error": "in_use_by_wordpress", "wordpress_id": wp.ID})
			return
		}
	}

	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Cascade grants: database_user_grants.database_id is RESTRICT, so the
	// final row delete would 500 if any grant is left behind. Revoke on the
	// MariaDB side first (idempotent since b723fe1), then drop the panel row.
	if h.cfg.DatabaseGrants != nil {
		grants, gErr := h.cfg.DatabaseGrants.ListByDatabaseID(ctx, d.ID)
		if gErr != nil {
			slog.ErrorContext(ctx, "databases.delete: list grants failed", "err", gErr, "db_id", d.ID)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		for _, g := range grants {
			var username string
			if h.cfg.DatabaseUsers != nil {
				if u, uErr := h.cfg.DatabaseUsers.FindByID(ctx, g.DatabaseUserID); uErr == nil && u != nil {
					username = u.Username
				}
			}
			if username != "" {
				if _, rErr := h.cfg.Agent.Call(agentCtx, "db_user.revoke", map[string]any{
					"db_name":      d.Name,
					"db_user_name": username,
				}); rErr != nil {
					slog.WarnContext(ctx, "databases.delete: revoke failed (best-effort)", "err", rErr, "db", d.Name, "user", username)
				}
			}
			if dErr := h.cfg.DatabaseGrants.Delete(ctx, g.ID); dErr != nil {
				slog.ErrorContext(ctx, "databases.delete: grant row delete failed", "err", dErr, "grant_id", g.ID)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
		}
	}

	// Drop the database on the engine the row was created with.
	// d.Name is the full prefixed name.
	dropCmd := "db.drop"
	if d.Engine == "postgres" {
		dropCmd = "db.postgres.drop_db"
	}
	if _, err := h.cfg.Agent.Call(agentCtx, dropCmd, map[string]any{"db_name": d.Name}); err != nil {
		slog.ErrorContext(ctx, "databases.delete: agent drop failed", "err", err, "db_name", d.Name, "engine", d.Engine)
		respondAgentErr(c, "agent_failed", err)
		return
	}

	if err := h.cfg.Databases.Delete(ctx, d.ID); err != nil {
		slog.ErrorContext(ctx, "databases.delete: row delete failed", "err", err, "db_id", d.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *databaseHandler) backup(c *gin.Context) {
	ctx := c.Request.Context()

	// Load the database first
	d, err := h.cfg.Databases.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Check authorization: admins can backup any; users only their own
	if !claims.IsAdmin && d.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Call agent to create the backup
	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	agentCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	result, err := h.cfg.Agent.Call(agentCtx, "db.backup", map[string]any{
		"db_name": d.Name,
	})
	if err != nil {
		respondAgentErr(c, "agent_failed", err)
		return
	}

	// Parse the backup response
	var resp struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Open the backup file. On any failure past this point the agent has
	// already written a full DB dump to disk; delete it before returning so a
	// retrying client doesn't strand root-owned dumps in the backup dir.
	f, err := openFile(resp.Path)
	if err != nil {
		_ = deleteFile(resp.Path)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	defer f.Close()

	// Set response headers for download. JAB-108: d.Name is a user-chosen
	// database name — route it through the RFC 6266 escaper so it can't
	// break out of the quoted filename value.
	dumpName := d.Name + "-" + time.Now().Format("20060102-150405") + ".sql"
	c.Header("Content-Disposition", contentDisposition("attachment", dumpName))
	c.Header("Content-Type", "application/sql")
	c.Header("Content-Length", fmt.Sprintf("%d", resp.SizeBytes))

	// Stream the file to the client
	if _, err := copyFile(c.Writer, f); err != nil {
		// Log the error but don't send response (headers already sent)
		slog.Error("failed to stream backup file", "error", err)
	}

	// Delete the temp file after streaming
	_ = deleteFile(resp.Path)
}

func (h *databaseHandler) restore(c *gin.Context) {
	ctx := c.Request.Context()

	// Load the database first
	d, err := h.cfg.Databases.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	// Check authorization: admins can restore any; users only their own
	if !claims.IsAdmin && d.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Parse multipart form (max 500 MB)
	const maxUploadSize = 500 * 1024 * 1024 // 500 MB
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadSize)
	if err := c.Request.ParseMultipartForm(32 << 20); err != nil { // 32 MB in-memory
		if err.Error() == "http: request body too large" {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file_too_large", "max_size": "500MB"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		}
		return
	}

	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_file"})
		return
	}

	file, err := header.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	defer file.Close()

	// Generate restore file path
	ul := ids.NewULID()
	restorePath := fmt.Sprintf("/var/lib/jabali/restore/%s.sql", ul)

	// Create restore directory if needed
	if err := createDir("/var/lib/jabali/restore"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Write uploaded file to /var/lib/jabali/restore/
	if err := writeToFile(restorePath, file, header.Size); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Call agent to restore the database
	if h.cfg.Agent == nil {
		_ = deleteFile(restorePath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	agentCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	_, err = h.cfg.Agent.Call(agentCtx, "db.restore", map[string]any{
		"db_name": d.Name,
		"path":    restorePath,
	})
	if err != nil {
		// Agent cleanup the file on failure, but delete it here too just in case
		_ = deleteFile(restorePath)
		respondAgentErr(c, "agent_failed", err)
		return
	}

	c.Status(http.StatusNoContent)
}
