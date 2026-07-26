// me_disk_usage.go — per-user disk-usage breakdown (cPanel-style), for the
// tenant panel. Aggregates the three storage areas a hosting user occupies:
//
//   - Files     — the home directory, via the POSIX quota report
//     (`user.limits.report`). Web content, logs, etc.
//
//   - Email     — sum of the user's mailboxes' last sampled usage
//     (`mailboxes.last_usage_bytes`, kept fresh by the reconciler;
//     no live agent call needed). Mail lives in Stalwart's store,
//     NOT under /home, so it doesn't double-count files.
//
//   - Databases — per-database size via `db.size` (MariaDB). PostgreSQL has no
//     size verb yet, so those report 0 for now.
//
//     GET /api/v1/me/disk-usage -> { total_bytes, quota_bytes, files, email, databases }
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DiskUsageConfig carries the repos + agent the disk-usage aggregator needs.
type DiskUsageConfig struct {
	Users      repository.UserRepository
	Domains    repository.DomainRepository
	Mailboxes  repository.MailboxRepository
	Databases  repository.DatabaseRepository
	Agent      agent.AgentInterface
	QuotaMount string
	// Snapshots persists the last computed breakdown so GET is a cheap read
	// and recompute happens only on an explicit POST refresh (#tenant DU).
	Snapshots repository.DiskUsageSnapshotRepository
}

// RegisterMeDiskUsageRoutes mounts GET /me/disk-usage on a group that already
// carries auth.
func RegisterMeDiskUsageRoutes(g *gin.RouterGroup, cfg DiskUsageConfig) {
	if cfg.Users == nil || cfg.Domains == nil || cfg.Mailboxes == nil || cfg.Databases == nil {
		return
	}
	h := &meDiskUsageHandler{cfg: cfg}
	g.GET("/me/disk-usage", h.get)
	g.POST("/me/disk-usage/refresh", h.refresh)
	g.GET("/me/disk-usage/files", h.filesTree)
}

type meDiskUsageHandler struct{ cfg DiskUsageConfig }

type diskUsageItem struct {
	Name   string `json:"name"`
	Bytes  uint64 `json:"bytes"`
	Engine string `json:"engine,omitempty"`
}

type diskUsageCategory struct {
	Bytes uint64          `json:"bytes"`
	Items []diskUsageItem `json:"items"`
}

type diskUsageResponse struct {
	// ComputedAt is when this breakdown was computed; nil => never computed
	// (the page shows an empty state prompting a first Refresh).
	ComputedAt *time.Time        `json:"computed_at"`
	TotalBytes uint64            `json:"total_bytes"`
	QuotaBytes uint64            `json:"quota_bytes"` // home quota limit; 0 = unlimited/unknown
	Files      diskUsageCategory `json:"files"`
	Email      diskUsageCategory `json:"email"`
	Databases  diskUsageCategory `json:"databases"`
}

// Exported aliases + wrapper so the `jabali disk-usage` CLI (#568) reads the
// SAME snapshot shape and runs the SAME live aggregation as the tenant
// /me/disk-usage endpoint — one implementation, no drift.
type DiskUsageItem = diskUsageItem
type DiskUsageCategory = diskUsageCategory
type DiskUsageResult = diskUsageResponse

// ComputeDiskUsage runs the live per-user breakdown (home quota + cached
// mailbox usage + db.size), the exact path POST /me/disk-usage/refresh uses.
func ComputeDiskUsage(ctx context.Context, cfg DiskUsageConfig, userID string) (DiskUsageResult, error) {
	return (&meDiskUsageHandler{cfg: cfg}).compute(ctx, userID)
}

// diskUsageListLimit bounds the per-category enumeration. A single hosting
// user with thousands of domains/dbs is pathological; this keeps the page
// bounded rather than unbounded-querying.
const diskUsageListLimit = 2000

// get returns the LAST stored snapshot (a cheap DB read — no agent calls). The
// page never auto-recomputes on entry; recompute is the explicit POST refresh.
func (h *meDiskUsageHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	resp := diskUsageResponse{
		Files:     diskUsageCategory{Items: []diskUsageItem{}},
		Email:     diskUsageCategory{Items: []diskUsageItem{}},
		Databases: diskUsageCategory{Items: []diskUsageItem{}},
	}
	if h.cfg.Snapshots != nil {
		if snap, err := h.cfg.Snapshots.Get(ctx, claims.UserID); err == nil && snap != nil {
			if uerr := json.Unmarshal([]byte(snap.Payload), &resp); uerr == nil {
				ca := snap.ComputedAt
				resp.ComputedAt = &ca
			}
		}
	}
	c.JSON(http.StatusOK, resp)
}

// refresh recomputes the breakdown live (quota report + mailbox cache +
// db.size), persists it as the new snapshot, and returns it.
func (h *meDiskUsageHandler) refresh(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	resp, err := h.compute(ctx, claims.UserID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
		return
	}
	now := time.Now()
	resp.ComputedAt = &now
	if h.cfg.Snapshots != nil {
		if payload, merr := json.Marshal(resp); merr == nil {
			_ = h.cfg.Snapshots.Upsert(ctx, claims.UserID, string(payload), now)
		}
	}
	c.JSON(http.StatusOK, resp)
}

// compute runs the live aggregation. Extracted from the old GET handler.
func (h *meDiskUsageHandler) compute(ctx context.Context, userID string) (diskUsageResponse, error) {
	user, err := h.cfg.Users.FindByID(ctx, userID)
	if err != nil || user == nil {
		return diskUsageResponse{}, err
	}
	opts := repository.ListOptions{Limit: diskUsageListLimit}

	resp := diskUsageResponse{
		Files:     diskUsageCategory{Items: []diskUsageItem{}},
		Email:     diskUsageCategory{Items: []diskUsageItem{}},
		Databases: diskUsageCategory{Items: []diskUsageItem{}},
	}

	// --- Files: home-directory quota report (admins have no Linux account) ---
	if user.Username != nil && *user.Username != "" && h.cfg.Agent != nil && h.cfg.QuotaMount != "" {
		used, limit := h.homeUsage(ctx, *user.Username)
		resp.Files.Bytes = used
		resp.QuotaBytes = limit
		resp.Files.Items = append(resp.Files.Items, diskUsageItem{Name: "Home directory", Bytes: used})
	}

	// --- Email: sum cached mailbox usage across the user's domains ---
	if domains, _, derr := h.cfg.Domains.ListByUserID(ctx, userID, opts); derr == nil {
		for i := range domains {
			mbs, _, merr := h.cfg.Mailboxes.ListByDomainID(ctx, domains[i].ID, opts)
			if merr != nil {
				continue
			}
			for j := range mbs {
				resp.Email.Bytes += mbs[j].LastUsageBytes
				resp.Email.Items = append(resp.Email.Items, diskUsageItem{
					Name:  mbs[j].EmailCached,
					Bytes: mbs[j].LastUsageBytes,
				})
			}
		}
	}

	// --- Databases: per-db size (MariaDB; PostgreSQL has no size verb yet) ---
	if dbs, _, derr := h.cfg.Databases.ListByUserID(ctx, userID, opts); derr == nil {
		for i := range dbs {
			var sz uint64
			if dbs[i].Engine == "mariadb" && h.cfg.Agent != nil {
				sz = h.dbSize(ctx, dbs[i].Name)
			}
			resp.Databases.Bytes += sz
			resp.Databases.Items = append(resp.Databases.Items, diskUsageItem{
				Name:   dbs[i].Name,
				Bytes:  sz,
				Engine: dbs[i].Engine,
			})
		}
	}

	resp.TotalBytes = resp.Files.Bytes + resp.Email.Bytes + resp.Databases.Bytes
	return resp, nil
}

// homeUsage returns (usedBytes, limitBytes) from the agent's quota report.
// Best-effort: any failure yields (0, 0).
func (h *meDiskUsageHandler) filesTree(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	user, err := h.cfg.Users.FindByID(ctx, claims.UserID)
	if err != nil || user == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
		return
	}
	if user.Username == nil || *user.Username == "" || h.cfg.Agent == nil {
		c.JSON(http.StatusOK, gin.H{"path": "", "total": 0, "entries": []any{}})
		return
	}
	username := *user.Username
	path := c.Query("path")
	if path == "" {
		path = "/home/" + username
	}
	cctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	raw, derr := h.cfg.Agent.Call(cctx, "files.du", map[string]any{
		"user_id":  claims.UserID,
		"username": username,
		"path":     path,
	})
	if derr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "files_du_failed", "detail": firstLineString(derr.Error())})
		return
	}
	var v struct {
		Path    string `json:"path"`
		Total   int64  `json:"total"`
		Entries []struct {
			Name       string `json:"name"`
			IsDir      bool   `json:"is_dir"`
			Size       int64  `json:"size"`
			HasSubdirs bool   `json:"has_subdirs"`
		} `json:"entries"`
	}
	if json.Unmarshal(raw, &v) != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "files_du_decode"})
		return
	}
	c.JSON(http.StatusOK, v)
}

func (h *meDiskUsageHandler) homeUsage(ctx context.Context, username string) (uint64, uint64) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(cctx, "user.limits.report", map[string]any{
		"username":    username,
		"quota_mount": h.cfg.QuotaMount,
	})
	if err != nil {
		return 0, 0
	}
	var v struct {
		Disk struct {
			UsedKB  uint64 `json:"used_kb"`
			LimitKB uint64 `json:"limit_kb"`
		} `json:"disk"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return 0, 0
	}
	return v.Disk.UsedKB * 1024, v.Disk.LimitKB * 1024
}

// dbSize returns a MariaDB database's size in bytes (0 on any error).
func (h *meDiskUsageHandler) dbSize(ctx context.Context, name string) uint64 {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(cctx, "db.size", map[string]any{"db_name": name})
	if err != nil {
		return 0
	}
	var v struct {
		SizeBytes int64 `json:"size_bytes"`
	}
	if json.Unmarshal(raw, &v) != nil || v.SizeBytes < 0 {
		return 0
	}
	return uint64(v.SizeBytes)
}
