// me_disk_usage.go — per-user disk-usage breakdown (cPanel-style), for the
// tenant panel. Aggregates the three storage areas a hosting user occupies:
//
//   - Files     — the home directory, via an actual `du` of /home/<user>
//     (`files.du`), the same figure the FilesTree shows. Falls back to the
//     POSIX quota report (`user.limits.report`) if du can't run. Web content,
//     logs, etc. (The quota "used" counts every block the UID owns across the
//     whole device, so it over-reports the home dir — GH #1439 — hence du.)
//
//   - Email     — sum of the user's mailboxes' last sampled usage
//     (`mailboxes.last_usage_bytes`, kept fresh by the reconciler;
//     no live agent call needed). Mail lives in Stalwart's store,
//     NOT under /home, so it doesn't double-count files.
//
//   - Databases — per-database size via `db.size`, for both engines. The
//     row's engine is forwarded so Postgres is sized with
//     pg_database_size() rather than a MariaDB information_schema sum that
//     has no row for it (GH #1005).
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

// ComputeDiskUsage runs the live per-user breakdown (home du + cached mailbox
// usage + db.size), the exact path POST /me/disk-usage/refresh uses.
func ComputeDiskUsage(ctx context.Context, cfg DiskUsageConfig, userID string) (DiskUsageResult, error) {
	res, _, err := (&meDiskUsageHandler{cfg: cfg}).compute(ctx, userID)
	return res, err
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
	resp, homeMeasured, err := h.compute(ctx, claims.UserID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user_not_found"})
		return
	}
	// Never overwrite a real Files figure with 0 because BOTH the du and the
	// quota calls failed this pass (agent hiccup). Keep the last known Files
	// (and its Total contribution). A legitimately-empty home reports du=0 with
	// homeMeasured=true, so it is NOT preserved — only the un-measured case is.
	if !homeMeasured && h.cfg.Snapshots != nil {
		if prev, perr := h.cfg.Snapshots.Get(ctx, claims.UserID); perr == nil && prev != nil {
			var pr diskUsageResponse
			if json.Unmarshal([]byte(prev.Payload), &pr) == nil && pr.Files.Bytes > 0 {
				resp.Files = pr.Files
				resp.TotalBytes = resp.Files.Bytes + resp.Email.Bytes + resp.Databases.Bytes
			}
		}
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

// compute runs the live aggregation. Extracted from the old GET handler. The
// second return reports whether the home-directory figure was actually
// measured this pass (either du or the quota report answered) — false means
// both agent calls failed, and the caller must NOT persist a 0 over a real
// prior value.
func (h *meDiskUsageHandler) compute(ctx context.Context, userID string) (diskUsageResponse, bool, error) {
	user, err := h.cfg.Users.FindByID(ctx, userID)
	if err != nil || user == nil {
		return diskUsageResponse{}, false, err
	}
	opts := repository.ListOptions{Limit: diskUsageListLimit}

	resp := diskUsageResponse{
		Files:     diskUsageCategory{Items: []diskUsageItem{}},
		Email:     diskUsageCategory{Items: []diskUsageItem{}},
		Databases: diskUsageCategory{Items: []diskUsageItem{}},
	}

	// --- Files: ACTUAL home-directory usage via du (admins have no Linux
	// account, so the whole block is skipped for them → homeMeasured stays
	// true, correctly reporting an empty Files). du is scoped to /home/<user>
	// and matches both the "Home directory" label and the FilesTree beside it.
	// The POSIX quota report counts every block the UID owns across the whole
	// device — which over-reports the home dir (GH #1439: quota 484 MB vs du
	// 354 MB on a real account) — so it is now only the FALLBACK when du can't
	// run. QuotaBytes still comes from the quota report: it is the limit, the
	// only source for it.
	homeMeasured := true
	if user.Username != nil && *user.Username != "" && h.cfg.Agent != nil {
		quotaUsed, limit, quotaOK := h.homeUsage(ctx, *user.Username)
		resp.QuotaBytes = limit
		du, duOK := h.homeDuBytes(ctx, userID, *user.Username)
		var filesBytes uint64
		switch {
		case duOK:
			filesBytes = du // primary: accurate home-subtree usage
		case quotaOK:
			filesBytes = quotaUsed // fallback: du timed out on a huge home
		default:
			homeMeasured = false // both failed — don't clobber the last value
		}
		resp.Files.Bytes = filesBytes
		resp.Files.Items = append(resp.Files.Items, diskUsageItem{Name: "Home directory", Bytes: filesBytes})
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

	// --- Databases: per-db size, both engines ---
	//
	// This used to gate on engine == "mariadb", so every Postgres database
	// reported 0 B here even after #1012 taught db.size to read
	// pg_database_size(). The Databases page was fixed; this page kept
	// skipping them, which is exactly what the reporter saw under
	// Disk Usage → Databases (GH #1005).
	if dbs, _, derr := h.cfg.Databases.ListByUserID(ctx, userID, opts); derr == nil {
		for i := range dbs {
			var sz uint64
			if h.cfg.Agent != nil {
				sz = h.dbSize(ctx, dbs[i].Name, dbs[i].Engine)
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
	return resp, homeMeasured, nil
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

// homeUsage returns (usedBytes, limitBytes, ok) from the agent's quota report.
// ok is false on any agent/parse failure (or when QuotaMount is unset). The
// used figure is now only the fallback for Files; limitBytes is the quota
// limit (the sole source for QuotaBytes).
func (h *meDiskUsageHandler) homeUsage(ctx context.Context, username string) (uint64, uint64, bool) {
	if h.cfg.QuotaMount == "" {
		return 0, 0, false
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(cctx, "user.limits.report", map[string]any{
		"username":    username,
		"quota_mount": h.cfg.QuotaMount,
	})
	if err != nil {
		return 0, 0, false
	}
	var v struct {
		Disk struct {
			UsedKB  uint64 `json:"used_kb"`
			LimitKB uint64 `json:"limit_kb"`
		} `json:"disk"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return 0, 0, false
	}
	return v.Disk.UsedKB * 1024, v.Disk.LimitKB * 1024, true
}

// homeDuBytes returns (bytes, ok): the ACTUAL recursive disk usage of
// /home/<user> via the same files.du (`du -B1 --max-depth=1`, GH #657-bounded)
// the FilesTree uses, so the summary and the tree agree by construction. A
// larger timeout than homeUsage (a full-home du can be slow); the refresh
// endpoint is explicit and the CLI grants 90s, so 45s fits inside the 60s
// axios ceiling (#1410). ok=false on timeout/error → the caller falls back to
// the quota report.
func (h *meDiskUsageHandler) homeDuBytes(ctx context.Context, userID, username string) (uint64, bool) {
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(cctx, "files.du", map[string]any{
		"user_id":  userID,
		"username": username,
		"path":     "/home/" + username,
	})
	if err != nil {
		return 0, false
	}
	var v struct {
		Total int64 `json:"total"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Total < 0 {
		return 0, false
	}
	return uint64(v.Total), true
}

// dbSize returns a database's size in bytes (0 on any error).
//
// engine MUST be forwarded: db.size picks pg_database_size() for postgres
// and the MariaDB information_schema sum otherwise, and information_schema
// has no row for a Postgres database — it sums to 0 with no error, so the
// zero looks like an empty database rather than a wrong query (GH #1005).
func (h *meDiskUsageHandler) dbSize(ctx context.Context, name, engine string) uint64 {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(cctx, "db.size", map[string]any{"db_name": name, "engine": engine})
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
