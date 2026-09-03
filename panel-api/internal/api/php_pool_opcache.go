package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// OPcache / JIT controls (GH #1332). Per-pool = per (user, PHP version): a
// pool's OPcache SHM is shared by every domain the user runs on that version,
// so these settings are version-scoped, exactly like the Performance tuning.
// Stored as php_pool_ini_overrides (rendered php_admin_value/flag), which the
// agent already applies via php.pool.apply — no new pool columns.
//
// A change to OPcache SHM sizing / JIT only takes effect at FPM MASTER start; a
// graceful USR2 reload (what php.pool.apply does on a same-version change) keeps
// the old SHM. So the PUT chains php.opcache.reset (a master RESTART) after the
// apply, so "Disable OPcache" / a new JIT buffer are observable immediately.

const (
	dirOpcacheEnable        = "opcache.enable"
	dirOpcacheValidateTS    = "opcache.validate_timestamps"
	dirOpcacheMemory        = "opcache.memory_consumption"
	dirOpcacheMaxFiles      = "opcache.max_accelerated_files"
	dirOpcacheRevalFreq     = "opcache.revalidate_freq"
	dirOpcacheJit           = "opcache.jit"
	dirOpcacheJitBufferSize = "opcache.jit_buffer_size"
)

// Clamp bounds — flat caps for v1 (per-package caps a future knob, mirroring
// the FPM clampToPackageCap philosophy). jit_buffer_size + memory_consumption
// allocate real memory per master, so bounding them protects the fleet's small
// (2 GB) boxes (the kswapd-deathspiral class).
const (
	opMemoryMinMB = 8
	opMemoryMaxMB = 512
	opFilesMin    = 100
	opFilesMax    = 1000000
	opRevalMaxSec = 3600
	opJitBufMinMB = 8
	opJitBufMaxMB = 256
	opcacheJitOn  = "tracing" // the only "on" CRTO we expose; tenants never see the integer flags
	opcacheJitOff = "off"
)

// opcacheSettings is the curated, nullable view. A nil field means "no
// override — use the server default"; the UI shows the default as a placeholder
// (the item-5 preset-transparency lesson: distinguish set from default).
type opcacheSettings struct {
	PHPVersion          string  `json:"php_version"`
	Enable              *bool   `json:"enable"`
	ValidateTimestamps  *bool   `json:"validate_timestamps"`
	MemoryConsumptionMB *uint32 `json:"memory_consumption_mb"`
	MaxAcceleratedFiles *uint32 `json:"max_accelerated_files"`
	RevalidateFreq      *uint32 `json:"revalidate_freq"`
	JitEnabled          *bool   `json:"jit_enabled"`
	JitBufferSizeMB     *uint32 `json:"jit_buffer_size_mb"`
}

// getOpcache returns the caller's OPcache/JIT overrides for a PHP version.
func (h *phpUserTuningHandler) getOpcache(c *gin.Context) {
	user, pkg := h.ownerPackage(c)
	if user == nil || pkg == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "fpm_editing_not_allowed"})
		return
	}
	pool, ok := h.userPool(c, user.ID, c.Query("php_version"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
		return
	}
	c.JSON(http.StatusOK, h.readOpcache(c, pool))
}

func (h *phpUserTuningHandler) readOpcache(c *gin.Context, pool *models.PHPPool) opcacheSettings {
	s := opcacheSettings{PHPVersion: pool.PHPVersion}
	rows, err := h.cfg.PHPPoolIniOverrides.ListByPool(c.Request.Context(), pool.ID)
	if err != nil {
		return s
	}
	for i := range rows {
		switch rows[i].Directive {
		case dirOpcacheEnable:
			s.Enable = boolPtr(strings.EqualFold(rows[i].Value, "on"))
		case dirOpcacheValidateTS:
			s.ValidateTimestamps = boolPtr(strings.EqualFold(rows[i].Value, "on"))
		case dirOpcacheMemory:
			s.MemoryConsumptionMB = uint32PtrFromStr(rows[i].Value)
		case dirOpcacheMaxFiles:
			s.MaxAcceleratedFiles = uint32PtrFromStr(rows[i].Value)
		case dirOpcacheRevalFreq:
			s.RevalidateFreq = uint32PtrFromStr(rows[i].Value)
		case dirOpcacheJit:
			s.JitEnabled = boolPtr(rows[i].Value != opcacheJitOff && rows[i].Value != "")
		case dirOpcacheJitBufferSize:
			// stored as "<N>M" → report N.
			v := rows[i].Value
			if len(v) > 0 && (v[len(v)-1] == 'M' || v[len(v)-1] == 'm') {
				v = v[:len(v)-1]
			}
			s.JitBufferSizeMB = uint32PtrFromStr(v)
		}
	}
	return s
}

type setOpcacheRequest struct {
	PHPVersion          string  `json:"php_version"`
	Enable              *bool   `json:"enable"`
	ValidateTimestamps  *bool   `json:"validate_timestamps"`
	MemoryConsumptionMB *uint32 `json:"memory_consumption_mb"`
	MaxAcceleratedFiles *uint32 `json:"max_accelerated_files"`
	RevalidateFreq      *uint32 `json:"revalidate_freq"`
	JitEnabled          *bool   `json:"jit_enabled"`
	JitBufferSizeMB     *uint32 `json:"jit_buffer_size_mb"`
}

// setOpcache validates+clamps, writes the override rows (or deletes them when a
// field is unset → server default), re-applies the pool, and restarts its
// master so OPcache/JIT changes take effect immediately.
func (h *phpUserTuningHandler) setOpcache(c *gin.Context) {
	user, pkg := h.ownerPackage(c)
	if user == nil || pkg == nil || !pkg.FpmUserCanEdit {
		c.JSON(http.StatusForbidden, gin.H{"error": "fpm_editing_not_allowed"})
		return
	}
	var req setOpcacheRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json"})
		return
	}
	if req.MemoryConsumptionMB != nil && (*req.MemoryConsumptionMB < opMemoryMinMB || *req.MemoryConsumptionMB > opMemoryMaxMB) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_memory", "detail": "opcache.memory_consumption must be 8–512 MB"})
		return
	}
	if req.MaxAcceleratedFiles != nil && (*req.MaxAcceleratedFiles < opFilesMin || *req.MaxAcceleratedFiles > opFilesMax) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_max_files", "detail": "opcache.max_accelerated_files must be 100–1000000"})
		return
	}
	if req.RevalidateFreq != nil && *req.RevalidateFreq > opRevalMaxSec {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_reval_freq", "detail": "opcache.revalidate_freq must be 0–3600 s"})
		return
	}
	if req.JitBufferSizeMB != nil && (*req.JitBufferSizeMB < opJitBufMinMB || *req.JitBufferSizeMB > opJitBufMaxMB) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_jit_buffer", "detail": "opcache.jit_buffer_size must be 8–256 MB"})
		return
	}

	pool, ok := h.userPool(c, user.ID, req.PHPVersion)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
		return
	}

	// Map struct → directive rows (upsert on set, delete on unset).
	apply := func(dir string, set bool, value, kind string) bool {
		if set {
			return h.upsertOverride(c, pool.ID, dir, value, kind) == nil
		}
		return h.deleteOverride(c, pool.ID, dir) == nil
	}
	okAll := true
	okAll = apply(dirOpcacheEnable, req.Enable != nil, onOff(req.Enable), "flag") && okAll
	okAll = apply(dirOpcacheValidateTS, req.ValidateTimestamps != nil, onOff(req.ValidateTimestamps), "flag") && okAll
	okAll = apply(dirOpcacheMemory, req.MemoryConsumptionMB != nil, u32str(req.MemoryConsumptionMB), "value") && okAll
	okAll = apply(dirOpcacheMaxFiles, req.MaxAcceleratedFiles != nil, u32str(req.MaxAcceleratedFiles), "value") && okAll
	okAll = apply(dirOpcacheRevalFreq, req.RevalidateFreq != nil, u32str(req.RevalidateFreq), "value") && okAll
	// JIT: two controls, three directives. Enabled → opcache.jit=tracing (+
	// buffer); disabled → opcache.jit=off and the buffer override is dropped.
	if req.JitEnabled != nil {
		if *req.JitEnabled {
			okAll = h.upsertOverride(c, pool.ID, dirOpcacheJit, opcacheJitOn, "value") == nil && okAll
		} else {
			okAll = h.upsertOverride(c, pool.ID, dirOpcacheJit, opcacheJitOff, "value") == nil && okAll
		}
	} else {
		okAll = h.deleteOverride(c, pool.ID, dirOpcacheJit) == nil && okAll
	}
	if req.JitBufferSizeMB != nil {
		okAll = h.upsertOverride(c, pool.ID, dirOpcacheJitBufferSize, u32str(req.JitBufferSizeMB)+"M", "value") == nil && okAll
	} else {
		okAll = h.deleteOverride(c, pool.ID, dirOpcacheJitBufferSize) == nil && okAll
	}
	if !okAll {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "override_write_failed"})
		return
	}

	// Apply the pool (renders the overrides + reloads) SYNCHRONOUSLY so the
	// conf is on disk before the restart below re-reads it.
	pool.Status = "pending"
	_ = h.cfg.PHPPools.Update(c.Request.Context(), pool)
	reconcilePHPPoolViaAgent(h.cfg.Agent, h.cfg.Users, h.cfg.PHPPoolIniOverrides, h.cfg.PHPPools, pool)
	if fresh, err := h.cfg.PHPPools.FindByID(c.Request.Context(), pool.ID); err == nil && fresh != nil && fresh.Status == "error" {
		detail := ""
		if fresh.LastError != nil {
			detail = *fresh.LastError
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "pool_apply_failed", "detail": detail})
		return
	}

	// Restart the master so OPcache SHM / JIT changes take effect (a graceful
	// reload would keep the old SHM). Reuses the item-10 reset verb.
	username := ""
	if user.Username != nil {
		username = *user.Username
	}
	isDefault := true
	if list, err := h.cfg.PHPPools.ListByUserID(c.Request.Context(), user.ID); err == nil && len(list) > 0 {
		isDefault = list[0].ID == pool.ID
	}
	slug := models.PoolSlug(username, pool.PHPVersion, isDefault)
	c.Set("audit_target", "php_opcache:"+slug)
	if _, err := h.cfg.Agent.Call(c.Request.Context(), "php.opcache.reset", map[string]any{
		"username": username, "slug": slug,
	}); err != nil {
		respondAgentErr(c, "opcache_restart_failed", err)
		return
	}
	c.JSON(http.StatusOK, h.readOpcache(c, pool))
}

// upsertOverride writes (or replaces) one directive override on a pool.
func (h *phpUserTuningHandler) upsertOverride(c *gin.Context, poolID, directive, value, kind string) error {
	ctx := c.Request.Context()
	rows, err := h.cfg.PHPPoolIniOverrides.ListByPool(ctx, poolID)
	if err != nil {
		return err
	}
	for i := range rows {
		if rows[i].Directive == directive {
			rows[i].Value = value
			rows[i].Kind = kind
			return h.cfg.PHPPoolIniOverrides.Update(ctx, &rows[i])
		}
	}
	return h.cfg.PHPPoolIniOverrides.Create(ctx, &models.PHPPoolIniOverride{
		ID: ids.NewULID(), PoolID: poolID, Directive: directive, Value: value, Kind: kind,
	})
}

// deleteOverride removes a directive override (→ server default). No-op if absent.
func (h *phpUserTuningHandler) deleteOverride(c *gin.Context, poolID, directive string) error {
	ctx := c.Request.Context()
	rows, err := h.cfg.PHPPoolIniOverrides.ListByPool(ctx, poolID)
	if err != nil {
		return err
	}
	for i := range rows {
		if rows[i].Directive == directive {
			return h.cfg.PHPPoolIniOverrides.Delete(ctx, rows[i].ID)
		}
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

// onOff renders a php_admin_flag value. The agent requires lowercase
// "on"/"off" (php_pool_apply.go rejects anything else).
func onOff(b *bool) string {
	if b != nil && *b {
		return "on"
	}
	return "off"
}
func u32str(v *uint32) string {
	if v == nil {
		return "0"
	}
	return strconv.FormatUint(uint64(*v), 10)
}
func uint32PtrFromStr(s string) *uint32 {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return nil
	}
	v := uint32(n)
	return &v
}
