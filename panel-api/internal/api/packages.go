package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// PackageReconciler is the narrow surface the update handler needs from
// the reconciler — just the per-user SSH-keys re-run that translates
// the package's ssh_enabled flag into jabali-sftp group membership.
// Defined here so tests can supply a fake without pulling in the full
// reconciler.
type PackageReconciler interface {
	ReconcileSSHKeysForUser(ctx context.Context, userID string) error
	// ReapplyPHPPoolForUser re-renders the user's PHP pool so a package's
	// php_exec_enabled change (GH #402) takes effect without waiting for the
	// periodic sweep.
	ReapplyPHPPoolForUser(ctx context.Context, userID string) error
}

// PackageHandlerConfig plugs the hosting-package CRUD handlers into the router.
type PackageHandlerConfig struct {
	Repo repository.PackageRepository
	// Users + Reconciler enable the post-update fan-out: when an admin
	// flips a package field that affects per-user state (today only
	// ssh_enabled, but the same hook covers future per-user-effective
	// fields), the handler enumerates users on this package and triggers
	// their reconciler so changes apply without waiting up to a minute
	// for the periodic sweep — and without forcing the operator to
	// re-save every user. Both nil-safe.
	Users      repository.UserRepository
	Reconciler PackageReconciler
	Log        *slog.Logger
}

const (
	defaultPackagesPageSize = 20
	maxPackagesPageSize     = 200
)

// RegisterPackageRoutes mounts /packages* under g (admin-only).
func RegisterPackageRoutes(g *gin.RouterGroup, cfg PackageHandlerConfig) {
	h := &packageHandler{cfg: cfg}

	pkgs := g.Group("/packages", middleware.RequireAdmin())
	pkgs.GET("", h.list)
	pkgs.POST("", h.create)
	pkgs.GET("/:id", h.get)
	pkgs.PATCH("/:id", h.update)
	pkgs.DELETE("/:id", h.delete)
}

type packageHandler struct{ cfg PackageHandlerConfig }

// ---- request / response ----

type createPackageRequest struct {
	Name        string `json:"name"               binding:"required"`
	DiskQuotaMB uint32 `json:"disk_quota_mb"`
	// M18 resource limits. Zero = unlimited on every field.
	CPUQuotaPercent  uint32 `json:"cpu_quota_percent"`
	MemoryLimitMB    uint32 `json:"memory_limit_mb"`
	IOReadMbps       uint32 `json:"io_read_mbps"`
	IOWriteMbps      uint32 `json:"io_write_mbps"`
	MaxTasks         uint32 `json:"max_tasks"`
	BandwidthQuotaMB uint32 `json:"bandwidth_quota_mb"`
	MaxDomains       uint32 `json:"max_domains"`
	MaxEmailAccounts uint32 `json:"max_email_accounts"`
	MaxDatabases     uint32 `json:"max_databases"`
	MaxDockerApps    uint32 `json:"max_docker_apps"`
	MaxPythonApps    uint32 `json:"max_python_apps"`
	// Tenant backup limits (GH #454). 0 = backups not included on this plan.
	MaxBackups                    uint32 `json:"max_backups"`
	ScheduledBackupsEnabled       bool   `json:"scheduled_backups_enabled"`
	AllowedBackupDestinationKinds string `json:"allowed_backup_destination_kinds"`
	BackupRetentionPolicy         string `json:"backup_retention_policy"`
	SSHEnabled                    bool   `json:"ssh_enabled"`
	CGIEnabled                    bool   `json:"cgi_enabled"`
	PHPExecEnabled                bool   `json:"php_exec_enabled"`
	FpmMaxChildrenCap             uint32 `json:"fpm_max_children_cap"`
	FpmWorkerMemMb                uint32 `json:"fpm_worker_mem_mb"`
	FpmUserCanEdit                bool   `json:"fpm_user_can_edit"`
	FpmAdvancedMode               bool   `json:"fpm_advanced_mode"`
	FpmVersionDefaults            string `json:"fpm_version_defaults"`
	DockerAppSlugs                string `json:"docker_app_slugs"`
	// M13: nspawn image pin (empty = use server default).
	NspawnImageVersion string `json:"nspawn_image_version"`
}

type updatePackageRequest struct {
	Name             *string `json:"name"`
	DiskQuotaMB      *uint32 `json:"disk_quota_mb"`
	CPUQuotaPercent  *uint32 `json:"cpu_quota_percent"`
	MemoryLimitMB    *uint32 `json:"memory_limit_mb"`
	IOReadMbps       *uint32 `json:"io_read_mbps"`
	IOWriteMbps      *uint32 `json:"io_write_mbps"`
	MaxTasks         *uint32 `json:"max_tasks"`
	BandwidthQuotaMB *uint32 `json:"bandwidth_quota_mb"`
	MaxDomains       *uint32 `json:"max_domains"`
	MaxEmailAccounts *uint32 `json:"max_email_accounts"`
	MaxDatabases     *uint32 `json:"max_databases"`
	MaxDockerApps    *uint32 `json:"max_docker_apps"`
	MaxPythonApps    *uint32 `json:"max_python_apps"`
	// Tenant backup limits (GH #454).
	MaxBackups                    *uint32 `json:"max_backups"`
	ScheduledBackupsEnabled       *bool   `json:"scheduled_backups_enabled"`
	AllowedBackupDestinationKinds *string `json:"allowed_backup_destination_kinds"`
	BackupRetentionPolicy         *string `json:"backup_retention_policy"`
	SSHEnabled                    *bool   `json:"ssh_enabled"`
	CGIEnabled                    *bool   `json:"cgi_enabled"`
	PHPExecEnabled                *bool   `json:"php_exec_enabled"`
	FpmMaxChildrenCap             *uint32 `json:"fpm_max_children_cap"`
	FpmWorkerMemMb                *uint32 `json:"fpm_worker_mem_mb"`
	FpmUserCanEdit                *bool   `json:"fpm_user_can_edit"`
	FpmAdvancedMode               *bool   `json:"fpm_advanced_mode"`
	FpmVersionDefaults            *string `json:"fpm_version_defaults"`
	DockerAppSlugs                *string `json:"docker_app_slugs"`
	NspawnImageVersion            *string `json:"nspawn_image_version"`
}

// ---- handlers ----

func (h *packageHandler) list(c *gin.Context) {
	page, pageSize, opts := parseListOptions(c, defaultPackagesPageSize, maxPackagesPageSize)

	pkgs, total, err := h.cfg.Repo.List(c.Request.Context(), opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"data":      pkgs,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *packageHandler) create(c *gin.Context) {
	var req createPackageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	now := time.Now().UTC()
	// GH #339 phase 2: FPM policy defaults + validation.
	if req.FpmMaxChildrenCap == 0 {
		req.FpmMaxChildrenCap = 20
	}
	if req.FpmWorkerMemMb == 0 {
		req.FpmWorkerMemMb = 64
	}
	if req.FpmMaxChildrenCap > phpPoolAdminMaxChildrenCap {
		c.JSON(http.StatusBadRequest, gin.H{"error": "fpm_cap_too_high", "detail": fmt.Sprintf("fpm_max_children_cap must be <= %d", phpPoolAdminMaxChildrenCap)})
		return
	}
	if req.FpmVersionDefaults == "" {
		req.FpmVersionDefaults = "{}"
	}
	if req.FpmAdvancedMode {
		req.FpmUserCanEdit = true // advanced implies can-edit
	}
	// GH #454: validate + canonicalise the allowed backup destination kinds.
	normKinds, nkErr := models.NormalizeBackupKindsCSV(req.AllowedBackupDestinationKinds)
	if nkErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_backup_kinds", "detail": nkErr.Error()})
		return
	}
	req.AllowedBackupDestinationKinds = normKinds
	if !models.IsValidBackupRetentionPolicy(req.BackupRetentionPolicy) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_backup_retention_policy", "detail": "must be reject or prune"})
		return
	}
	if req.BackupRetentionPolicy == "" {
		req.BackupRetentionPolicy = models.BackupRetentionReject
	}
	pkg := &models.HostingPackage{
		ID:               ids.NewULID(),
		Name:             req.Name,
		DiskQuotaMB:      req.DiskQuotaMB,
		CPUQuotaPercent:  req.CPUQuotaPercent,
		MemoryLimitMB:    req.MemoryLimitMB,
		IOReadMbps:       req.IOReadMbps,
		IOWriteMbps:      req.IOWriteMbps,
		MaxTasks:         req.MaxTasks,
		BandwidthQuotaMB: req.BandwidthQuotaMB,
		MaxDomains:       req.MaxDomains,
		MaxEmailAccounts: req.MaxEmailAccounts,
		MaxDatabases:     req.MaxDatabases,
		MaxDockerApps:    req.MaxDockerApps,
		MaxPythonApps:    req.MaxPythonApps,

		MaxBackups:                    req.MaxBackups,
		ScheduledBackupsEnabled:       req.ScheduledBackupsEnabled,
		AllowedBackupDestinationKinds: req.AllowedBackupDestinationKinds,
		BackupRetentionPolicy:         req.BackupRetentionPolicy,

		SSHEnabled:         req.SSHEnabled,
		CGIEnabled:         req.CGIEnabled,
		PHPExecEnabled:     req.PHPExecEnabled,
		FpmMaxChildrenCap:  req.FpmMaxChildrenCap,
		FpmWorkerMemMb:     req.FpmWorkerMemMb,
		FpmUserCanEdit:     req.FpmUserCanEdit,
		FpmAdvancedMode:    req.FpmAdvancedMode,
		FpmVersionDefaults: req.FpmVersionDefaults,
		DockerAppSlugs:     req.DockerAppSlugs,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if v := strings.TrimSpace(req.NspawnImageVersion); v != "" {
		if !isImageNamePattern(v) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "invalid_nspawn_image_version",
				"detail": "must match [a-z0-9-]+",
			})
			return
		}
		pkg.NspawnImageVersion = &v
	}

	if err := validatePackageLimits(pkg); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	if err := h.cfg.Repo.Create(c.Request.Context(), pkg); err != nil {
		if isConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "already_exists", "detail": "package name taken"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusCreated, pkg)
}

// validatePackageLimits enforces the bounds from internal/limits on the
// M18 resource-limit fields before write. Runs at both create and
// update — the agent validates again as defense-in-depth, but returning
// a clean 422 here is much better UX than a 502-agent-error later.
func validatePackageLimits(pkg *models.HostingPackage) error {
	e := limits.EffectiveLimits{
		DiskQuotaMB:     pkg.DiskQuotaMB,
		CPUQuotaPercent: pkg.CPUQuotaPercent,
		MemoryLimitMB:   pkg.MemoryLimitMB,
		IOReadMbps:      pkg.IOReadMbps,
		IOWriteMbps:     pkg.IOWriteMbps,
		MaxTasks:        pkg.MaxTasks,
	}
	return e.Validate()
}

func (h *packageHandler) get(c *gin.Context) {
	pkg, err := h.cfg.Repo.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, pkg)
}

func (h *packageHandler) update(c *gin.Context) {
	pkg, err := h.cfg.Repo.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	var req updatePackageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	// Snapshot the fields whose change requires a per-user reconcile
	// fan-out, so we can compare to the new values once Update returns
	// successfully. Read BEFORE the field copies overwrite pkg.
	prevSSHEnabled := pkg.SSHEnabled
	prevPHPExec := pkg.PHPExecEnabled

	if req.Name != nil {
		pkg.Name = *req.Name
	}
	if req.DiskQuotaMB != nil {
		pkg.DiskQuotaMB = *req.DiskQuotaMB
	}
	if req.CPUQuotaPercent != nil {
		pkg.CPUQuotaPercent = *req.CPUQuotaPercent
	}
	if req.MemoryLimitMB != nil {
		pkg.MemoryLimitMB = *req.MemoryLimitMB
	}
	if req.IOReadMbps != nil {
		pkg.IOReadMbps = *req.IOReadMbps
	}
	if req.IOWriteMbps != nil {
		pkg.IOWriteMbps = *req.IOWriteMbps
	}
	if req.MaxTasks != nil {
		pkg.MaxTasks = *req.MaxTasks
	}
	if req.BandwidthQuotaMB != nil {
		pkg.BandwidthQuotaMB = *req.BandwidthQuotaMB
	}
	if req.MaxDomains != nil {
		pkg.MaxDomains = *req.MaxDomains
	}
	if req.MaxEmailAccounts != nil {
		pkg.MaxEmailAccounts = *req.MaxEmailAccounts
	}
	if req.MaxDatabases != nil {
		pkg.MaxDatabases = *req.MaxDatabases
	}
	if req.MaxDockerApps != nil {
		pkg.MaxDockerApps = *req.MaxDockerApps
	}
	if req.MaxPythonApps != nil {
		pkg.MaxPythonApps = *req.MaxPythonApps
	}
	if req.MaxBackups != nil {
		pkg.MaxBackups = *req.MaxBackups
	}
	if req.ScheduledBackupsEnabled != nil {
		pkg.ScheduledBackupsEnabled = *req.ScheduledBackupsEnabled
	}
	if req.AllowedBackupDestinationKinds != nil {
		normKinds, nkErr := models.NormalizeBackupKindsCSV(*req.AllowedBackupDestinationKinds)
		if nkErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_backup_kinds", "detail": nkErr.Error()})
			return
		}
		pkg.AllowedBackupDestinationKinds = normKinds
	}
	if req.BackupRetentionPolicy != nil {
		if !models.IsValidBackupRetentionPolicy(*req.BackupRetentionPolicy) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_backup_retention_policy", "detail": "must be reject or prune"})
			return
		}
		v := *req.BackupRetentionPolicy
		if v == "" {
			v = models.BackupRetentionReject
		}
		pkg.BackupRetentionPolicy = v
	}
	if req.SSHEnabled != nil {
		pkg.SSHEnabled = *req.SSHEnabled
	}
	if req.CGIEnabled != nil {
		pkg.CGIEnabled = *req.CGIEnabled
	}
	if req.PHPExecEnabled != nil {
		pkg.PHPExecEnabled = *req.PHPExecEnabled
	}
	if req.FpmMaxChildrenCap != nil {
		if *req.FpmMaxChildrenCap > phpPoolAdminMaxChildrenCap {
			c.JSON(http.StatusBadRequest, gin.H{"error": "fpm_cap_too_high", "detail": fmt.Sprintf("fpm_max_children_cap must be <= %d", phpPoolAdminMaxChildrenCap)})
			return
		}
		pkg.FpmMaxChildrenCap = *req.FpmMaxChildrenCap
	}
	if req.FpmWorkerMemMb != nil {
		pkg.FpmWorkerMemMb = *req.FpmWorkerMemMb
	}
	if req.FpmUserCanEdit != nil {
		pkg.FpmUserCanEdit = *req.FpmUserCanEdit
	}
	if req.FpmAdvancedMode != nil {
		pkg.FpmAdvancedMode = *req.FpmAdvancedMode
	}
	if req.FpmVersionDefaults != nil {
		pkg.FpmVersionDefaults = *req.FpmVersionDefaults
	}
	if pkg.FpmAdvancedMode {
		pkg.FpmUserCanEdit = true // advanced implies can-edit
	}
	if req.DockerAppSlugs != nil {
		pkg.DockerAppSlugs = *req.DockerAppSlugs
	}
	if req.NspawnImageVersion != nil {
		v := strings.TrimSpace(*req.NspawnImageVersion)
		if v == "" {
			pkg.NspawnImageVersion = nil
		} else {
			if !isImageNamePattern(v) {
				c.JSON(http.StatusBadRequest, gin.H{
					"error":  "invalid_nspawn_image_version",
					"detail": "must match [a-z0-9-]+",
				})
				return
			}
			pkg.NspawnImageVersion = &v
		}
	}
	pkg.UpdatedAt = time.Now().UTC()

	if err := validatePackageLimits(pkg); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	if err := h.cfg.Repo.Update(c.Request.Context(), pkg); err != nil {
		if isConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "already_exists", "detail": "package name taken"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Fan out to every user on this package whenever ssh_enabled flipped.
	// Done in a detached goroutine + fresh background context so the
	// admin response isn't blocked by per-user agent calls (each user
	// triggers an ssh.user.{join,leave}_sftp_group + authorized_keys
	// rewrite). The 60s reconciler sweep is the safety net; this just
	// makes the change feel immediate.
	if req.SSHEnabled != nil && *req.SSHEnabled != prevSSHEnabled {
		h.fanOutSSHReconcile(pkg.ID)
	}
	// GH #402: re-render every pool on this package when php_exec_enabled
	// flipped, so the disable_functions change applies without waiting for
	// the periodic sweep.
	if req.PHPExecEnabled != nil && *req.PHPExecEnabled != prevPHPExec {
		h.fanOutPHPPoolReapply(pkg.ID)
	}

	c.JSON(http.StatusOK, pkg)
}

// fanOutSSHReconcile reconciles every user on the given package in a
// detached goroutine. Bounded list size (10k) matches the periodic
// sweep — anyone with > 10k users on a single package has bigger
// problems than this fan-out missing a tail.
//
// Errors are logged, never returned: the admin already got their 200,
// and the periodic sweep will catch any user we couldn't reach.
func (h *packageHandler) fanOutSSHReconcile(packageID string) {
	if h.cfg.Users == nil || h.cfg.Reconciler == nil {
		return
	}
	users := h.cfg.Users
	rec := h.cfg.Reconciler
	log := h.cfg.Log
	if log == nil {
		log = slog.Default()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		all, _, err := users.List(ctx, repository.ListOptions{Limit: 10000})
		if err != nil {
			log.Warn("package update: list users for ssh reconcile", "package_id", packageID, "err", err)
			return
		}
		count := 0
		for i := range all {
			u := &all[i]
			if u.PackageID == nil || *u.PackageID != packageID {
				continue
			}
			perCtx, perCancel := context.WithTimeout(ctx, 30*time.Second)
			if err := rec.ReconcileSSHKeysForUser(perCtx, u.ID); err != nil {
				log.Warn("package update: ssh reconcile user", "package_id", packageID, "user_id", u.ID, "err", err)
			} else {
				count++
			}
			perCancel()
		}
		log.Info("package update: ssh reconcile fan-out complete", "package_id", packageID, "users", count)
	}()
}

// fanOutPHPPoolReapply re-renders every pool on the given package in a
// detached goroutine (GH #402). Same shape + bounds as fanOutSSHReconcile;
// errors logged, never returned. The periodic sweep is the safety net.
func (h *packageHandler) fanOutPHPPoolReapply(packageID string) {
	if h.cfg.Users == nil || h.cfg.Reconciler == nil {
		return
	}
	users := h.cfg.Users
	rec := h.cfg.Reconciler
	log := h.cfg.Log
	if log == nil {
		log = slog.Default()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		all, _, err := users.List(ctx, repository.ListOptions{Limit: 10000})
		if err != nil {
			log.Warn("package update: list users for php pool reapply", "package_id", packageID, "err", err)
			return
		}
		count := 0
		for i := range all {
			u := &all[i]
			if u.PackageID == nil || *u.PackageID != packageID {
				continue
			}
			perCtx, perCancel := context.WithTimeout(ctx, 30*time.Second)
			if err := rec.ReapplyPHPPoolForUser(perCtx, u.ID); err != nil {
				log.Warn("package update: php pool reapply user", "package_id", packageID, "user_id", u.ID, "err", err)
			} else {
				count++
			}
			perCancel()
		}
		log.Info("package update: php pool reapply fan-out complete", "package_id", packageID, "users", count)
	}()
}

func (h *packageHandler) delete(c *gin.Context) {
	if err := h.cfg.Repo.Delete(c.Request.Context(), c.Param("id")); err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Status(http.StatusNoContent)
}

func isConflict(err error) bool {
	return errors.Is(err, repository.ErrConflict)
}

func isNotFound(err error) bool {
	return errors.Is(err, repository.ErrNotFound)
}
