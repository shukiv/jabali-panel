// Package api - /api/v1/files HTTP handlers (M11 Wave C).
// Handlers are thin: auth → dispatch to panel-agent files.* command → JSON.
// All filesystem safety is enforced inside panel-agent via the filesafe package.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// uploadStagingDir is where panel-api writes incoming uploads before
// handing off to the agent's files.ingest. MUST stay in sync with the
// prefix gate in panel-agent/internal/commands/files_ingest.go.
//
// Why /var/lib/jabali-uploads and not /tmp: both jabali-panel-api and
// jabali-agent run with PrivateTmp=true, so each has its own per-unit
// /tmp namespace. A file panel-api writes to /tmp is invisible to the
// agent, breaking every upload with `open_tmp: no such file or directory`.
// The shared dir lives outside the tmp sandbox so both services see
// the same on-disk path. Created at install time owned jabali:jabali
// 0750 with /var/lib/jabali-uploads in panel-api's ReadWritePaths;
// ensureUploadStagingDir() also creates it on first upload as a
// belt-and-braces (test environments don't run install.sh).
// var (not const) so tests can point it at t.TempDir() — production
// install.sh creates /var/lib/jabali-uploads at install time and
// systemd unit ReadWritePaths grants panel-api access to that exact
// path; rewriting it at runtime would break that grant.
var uploadStagingDir = "/var/lib/jabali-uploads"

func uploadStagingPrefix() string { return uploadStagingDir + "/jabali-upload-" }

// ensureUploadStagingDir is called from upload paths before any
// OpenFile under uploadStagingPrefix(). MkdirAll is idempotent — a
// no-op when install.sh's install -d already created the dir.
func ensureUploadStagingDir() error {
	return os.MkdirAll(uploadStagingDir, 0o750)
}

// maxInFlightUploadsPerUser caps how many chunked staging files one tenant may
// have on disk at once (Gitea #425 — a single account must not be able to fill
// the service partition shared with MariaDB + panel state).
const maxInFlightUploadsPerUser = 5

// userStagingTag is a stable, non-secret per-user basename component that lets
// the concurrency cap glob a single tenant's in-flight staging files without
// any in-memory state. Distinct users -> distinct tags.
func userStagingTag(userID string) string {
	sum := sha256.Sum256([]byte("jabali-upload-user:" + userID))
	return hex.EncodeToString(sum[:6])
}

// chunkStagingPath derives the chunked-upload staging path from the
// AUTHENTICATED user plus the client upload_id. Because the path depends on the
// server-side userID, one tenant can never compute — and therefore never write
// into or read — another tenant's staging file even if they learn the upload_id
// (Gitea #426). The basename stays a flat "jabali-upload-…" with no "/", so it
// still satisfies the agent-side ingest prefix gate.
func chunkStagingPath(userID, uploadID string) string {
	sum := sha256.Sum256([]byte("jabali-upload:" + userID + ":" + uploadID))
	return uploadStagingPrefix() + userStagingTag(userID) + "-" + hex.EncodeToString(sum[:])
}

// globUserStaging returns a tenant's current staging files (both chunked and
// single-shot — both are tag-prefixed).
func globUserStaging(userID string) []string {
	m, _ := filepath.Glob(uploadStagingPrefix() + userStagingTag(userID) + "-*")
	return m
}

// userStagingStats returns how many staging files the tenant currently holds and
// their total bytes — the per-user concurrency + disk-budget basis for #425.
// No shared state, survives restarts.
func userStagingStats(userID string) (count int, bytes int64) {
	for _, m := range globUserStaging(userID) {
		if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() {
			count++
			bytes += fi.Size()
		}
	}
	return
}

// maxUserStagingBytesMultiple: the per-user assembled-staging ceiling is this
// multiple of the configured single-upload cap, so a tenant can't fill the
// service partition (shared with MariaDB + panel state) with many concurrent or
// abandoned uploads (#425, reviewer item 3).
const maxUserStagingBytesMultiple = 2

func (h *filesHandler) userStagingBudget(ctx context.Context) int64 {
	return maxUserStagingBytesMultiple * h.resolveMaxUploadBytes(ctx)
}

// singleStagingPath mints a user-tagged staging path for the single-shot upload
// path so it counts against the same per-user concurrency + byte budget as the
// chunked path (and still satisfies the agent ingest prefix gate).
func singleStagingPath(userID, rnd string) string {
	return uploadStagingPrefix() + userStagingTag(userID) + "-" + rnd
}

// Agent param struct types. JSON tags must match panel-agent/internal/commands/files_*.go
// exactly — see files_wire_test.go for the drift-guard test.
type filesListAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
}

type filesReadAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Limit     int64  `json:"limit,omitempty"`
}

type filesWriteAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Mode      string `json:"mode,omitempty"`
}

type filesDeleteAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

type filesMkdirAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Mode      string `json:"mode,omitempty"`
}

type filesRenameAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
}

type filesMoveAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
}

type filesChmodAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Mode      string `json:"mode"`
}

type filesArchiveAgentParams struct {
	UserID    string   `json:"user_id"`
	Username  string   `json:"username"`
	AdminRoot bool     `json:"admin_root,omitempty"`
	Paths     []string `json:"paths"`
}

type filesArchiveAgentResult struct {
	ArchivePath string `json:"archive_path"`
	Size        int64  `json:"size"`
}

type filesDuAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
}

type filesExtractAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Dest      string `json:"dest,omitempty"`
}

type filesCopyAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	SrcPath   string `json:"src_path"`
	DstPath   string `json:"dst_path"`
}

type filesIngestAgentParams struct {
	Overwrite bool   `json:"overwrite,omitempty"`
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	TmpPath   string `json:"tmp_path"`
	DestPath  string `json:"dest_path"`
}

type filesStatAgentParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
}

// FilesHandlerConfig bundles dependencies for /api/v1/files.
type FilesHandlerConfig struct {
	Users          repository.UserRepository
	Domains        repository.DomainRepository
	Agent          agent.AgentInterface
	Log            *slog.Logger
	ServerSettings repository.ServerSettingsRepository // optional; nil → fall back to defaultMaxUploadBytes
	// Audits records admin File Manager mutations (GH #1184). Optional.
	Audits repository.AuditEventRepository
	// KratosClient powers the JAB-380 recent-auth (step-up) check on the
	// admin (/admin/files) mount. Optional: nil disables step-up (tests /
	// non-Kratos deployments) — the mount is still admin + opt-in gated.
	KratosClient *kratosclient.Client
}

const (
	// Compile-time fallback if ServerSettings repo isn't wired or
	// upload_max_size_mb is 0. Matches the historical hardcoded value.
	defaultMaxUploadBytes int64 = 1024 * 1024 * 1024 // 1 GB
	maxPreviewBytes       int64 = 1 * 1024 * 1024
)

// resolveMaxUploadBytes reads the admin-configured upload cap from
// server_settings (cached to a few hundred ns by the underlying GORM
// query plan; the row is tiny). Falls back to defaultMaxUploadBytes
// when the repo isn't wired or the column is unset.
func (h *filesHandler) resolveMaxUploadBytes(ctx context.Context) int64 {
	if h.cfg.ServerSettings == nil {
		return defaultMaxUploadBytes
	}
	s, err := h.cfg.ServerSettings.Get(ctx)
	if err != nil || s == nil || s.UploadMaxSizeMB == 0 {
		return defaultMaxUploadBytes
	}
	return int64(s.UploadMaxSizeMB) * 1024 * 1024
}

// RegisterFilesRoutes mounts /files under the given group (expected /api/v1).
// All routes require an authenticated caller.
func RegisterFilesRoutes(g *gin.RouterGroup, cfg FilesHandlerConfig) {
	h := &filesHandler{cfg: cfg}
	grp := g.Group("/files")
	grp.GET("", h.list)
	grp.DELETE("", h.delete)
	grp.GET("/home", h.home)
	grp.GET("/tree", h.tree)
	grp.GET("/download", h.download)
	grp.GET("/preview", h.preview)
	grp.POST("/upload", filesUploadSizeLimit(func(c *gin.Context) int64 {
		return h.resolveMaxUploadBytes(c.Request.Context())
	}), h.upload)
	grp.POST("/mkdir", h.mkdir)
	grp.POST("/rename", h.rename)
	grp.POST("/move", h.move)
	grp.POST("/chmod", h.chmod)
	grp.POST("/archive", h.archive)
	grp.POST("/extract", h.extract)
	grp.GET("/jobs/:id", h.jobStatus) // GH #1392: async file-op progress
	grp.POST("/copy", h.copy)
	// /write carries whole file contents (Monaco save) — exempt from the
	// global 1 MiB JSON cap (JAB-246), bounded by the upload knob instead.
	grp.POST("/write", filesUploadSizeLimit(func(c *gin.Context) int64 {
		return h.resolveMaxUploadBytes(c.Request.Context())
	}), h.write)
	grp.POST("/upload-chunk", h.uploadChunk)
	// Resume support — the client HEADs this before starting to see how
	// many bytes of the upload-staging file already exist, so a reload
	// mid-upload doesn't restart from zero.
	grp.GET("/upload-chunk-status", h.uploadChunkStatus)

	// GH #1184 admin File Manager: the SAME handlers mounted at /admin/files
	// behind adminGate (admin claims + the default-off admin_file_manager_enabled
	// setting). adminGate sets the context flag → requireClaimsAndUsername
	// returns the admin ident and every agent call carries admin_root=true, so
	// the agent uses the root scope + deny-list. Reusing the handlers keeps the
	// admin File Manager byte-identical to the tenant one.
	adm := g.Group("/admin/files", h.adminGate)
	adm.GET("", h.list)
	adm.DELETE("", h.delete)
	adm.GET("/home", h.home)
	adm.GET("/tree", h.tree)
	adm.GET("/download", h.download)
	adm.GET("/preview", h.preview)
	adm.POST("/upload", filesUploadSizeLimit(func(c *gin.Context) int64 {
		return h.resolveMaxUploadBytes(c.Request.Context())
	}), h.upload)
	adm.POST("/mkdir", h.mkdir)
	adm.POST("/rename", h.rename)
	adm.POST("/move", h.move)
	adm.POST("/chmod", h.chmod)
	adm.POST("/archive", h.archive)
	adm.POST("/extract", h.extract)
	adm.GET("/jobs/:id", h.jobStatus) // GH #1392: async file-op progress
	adm.POST("/copy", h.copy)
	adm.POST("/write", filesUploadSizeLimit(func(c *gin.Context) int64 {
		return h.resolveMaxUploadBytes(c.Request.Context())
	}), h.write)
	adm.POST("/upload-chunk", h.uploadChunk)
	adm.GET("/upload-chunk-status", h.uploadChunkStatus)
	adm.GET("/du", h.du)
}

// du dispatches files.du for the admin File Manager (GH #1184). The tenant du
// lives on /me/disk-usage/files; this admin-only variant carries admin_root so
// it computes sizes anywhere under the root scope. Streams the agent JSON back
// unchanged (same shape the tenant du surface returns).
func (h *filesHandler) du(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	p, ok := requirePath(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 55*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "files.du", filesDuAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: p,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

// adminGate authorises the GH #1184 admin File Manager: admin claims AND the
// default-off admin_file_manager_enabled server setting. It flags the request
// (→ admin_root everywhere) and best-effort audits each mutation.
func (h *filesHandler) adminGate(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil || !claims.IsAdmin {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	if h.cfg.ServerSettings == nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin_file_manager_disabled"})
		return
	}
	s, err := h.cfg.ServerSettings.Get(c.Request.Context())
	if err != nil || s == nil || !s.AdminFileManagerEnabled {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin_file_manager_disabled"})
		return
	}
	// JAB-380 recent-auth (step-up): every admin File Manager request —
	// reads included, because the root scope exposes arbitrary files — needs a
	// recently authenticated interactive session. Checked AFTER the opt-in gate
	// so a disabled FM still reports admin_file_manager_disabled. A nil
	// KratosClient (tests / non-Kratos deploys) leaves the mount admin+opt-in
	// gated only.
	if h.cfg.KratosClient != nil {
		if !requireRecentAuth(c, h.cfg.KratosClient, stepUpWindow) {
			return
		}
	}
	c.Set(adminFilesCtxKey, true)
	if h.cfg.Audits != nil && c.Request.Method != http.MethodGet {
		actor := claims.UserID
		ip := c.ClientIP()
		_ = h.cfg.Audits.Create(c.Request.Context(), &models.AuditEvent{
			ID:          ids.NewULID(),
			TS:          time.Now().UTC(),
			ActorUserID: &actor,
			ActorKind:   models.AuditActorAdmin,
			Action:      "admin.files " + c.Request.Method + " " + c.FullPath(),
			TargetType:  "file",
			TargetID:    c.Query("path"),
			Result:      "ok",
			SourceIP:    &ip,
		})
	}
	c.Next()
}

type filesHandler struct{ cfg FilesHandlerConfig }

type mkdirRequest struct {
	Path string `json:"path" binding:"required"`
}

type extractRequest struct {
	Path string `json:"path" binding:"required"`
	Dest string `json:"dest,omitempty"`
}

type renameRequest struct {
	Path    string `json:"path" binding:"required"`
	NewName string `json:"new_name" binding:"required"`
}

// moveRequest is used by the drag-and-drop flow: dragging a row onto a
// folder sends the original path and the new *parent directory*; the
// handler combines them to form the destination path, preserving the
// basename. This mirrors desktop drag-to-move semantics and avoids
// letting the client smuggle an arbitrary destination name.
type moveRequest struct {
	Path    string `json:"path"     binding:"required"`
	DestDir string `json:"dest_dir" binding:"required"`
}

// chmodRequest powers the bulk-permissions modal. Mode is an octal
// string like "0644"; the agent validates the format and masks it to
// the low 12 bits, so we don't replicate the parse here — sending a
// bogus mode just gets a 400 back from the agent.
type chmodRequest struct {
	Path string `json:"path" binding:"required"`
	Mode string `json:"mode" binding:"required"`
}

// archiveRequest powers the "Download .tar.gz of N selected items"
// flow. Paths are absolute, scoped inside the user's homedir — the
// agent re-validates every entry before building anything.
type archiveRequest struct {
	Paths []string `json:"paths" binding:"required"`
}

// copyRequest powers the Copy/Paste flow. `dest_dir` is the parent
// directory to land into; the basename is preserved from src so the
// client can't name a destination collision it wouldn't have picked
// interactively.
type copyRequest struct {
	Path    string `json:"path"     binding:"required"`
	DestDir string `json:"dest_dir" binding:"required"`
}

// writeRequest powers the Monaco editor's Save action. Content is
// UTF-8; binary-safe writes (base64) are deferred to Phase-3.
type writeRequest struct {
	Path    string `json:"path"    binding:"required"`
	Content string `json:"content"`
}

type filesListEntry struct {
	Name       string `json:"name"`
	IsDir      bool   `json:"is_dir"`
	Size       int64  `json:"size"`
	Mode       string `json:"mode"`
	ModTime    string `json:"mod_time"`
	IsSymlink  bool   `json:"is_symlink"`
	HasSubdirs bool   `json:"has_subdirs,omitempty"`
}

type filesListAgentResult struct {
	Path    string           `json:"path"`
	Entries []filesListEntry `json:"entries"`
}

type filesReadAgentResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	ContentB64 string `json:"content_b64"`
	IsBinary   bool   `json:"is_binary"`
	Size       int64  `json:"size"`
	Truncated  bool   `json:"truncated"`
	MimeType   string `json:"mime_type"`
}

// ---- helpers ----

// adminFilesCtxKey flags a request served through the GH #1184 /admin/files
// mount (set by adminGate). Handlers pass it as admin_root so the agent uses
// the root scope + deny-list instead of the caller's home.
const adminFilesCtxKey = "jabali_admin_files"

func (h *filesHandler) adminRoot(c *gin.Context) bool { return c.GetBool(adminFilesCtxKey) }

// rejectAdminWriteOutOfScope is the API-side, defense-in-depth pre-check for the
// GH #1184 admin File Manager (JAB-367, criterion 4). For an admin_root mutation
// it validates every destination path against the SAME write allow-list the
// agent enforces — filesafe.NewAdminScope(...).WriteScope(), the safe data roots
// (/home, /var/www, …) minus the hard deny-list — and refuses, BEFORE the agent
// call, a write outside the allow-list (read_only) or inside a denied prefix
// (path_denied). The agent remains authoritative: it re-runs this exact check
// AND adds the kernel openat2 RESOLVE_BENEATH resolution that catches a symlink
// escape this lexical layer cannot see. So this is layered enforcement, never
// the sole gate.
//
// Tenant (non-admin_root) requests are scoped to the tenant docroot by the agent
// and are deliberately NOT checked here — the API doesn't know a tenant's
// docroots and must not second-guess the agent's per-tenant scope.
//
// Returns true (and writes the 403) when the request is rejected — the caller
// must stop. Returns false when every path is allowed (or the request isn't
// admin_root). The 403 body matches respondAgentError's envelope for the same
// violation so the SPA renders one message regardless of which layer rejected.
func (h *filesHandler) rejectAdminWriteOutOfScope(c *gin.Context, paths ...string) bool {
	if !h.adminRoot(c) {
		return false
	}
	// The username is irrelevant to a root-scoped admin write check (WriteScope
	// narrows to the fixed mutable roots, not /home/<user>); NewAdminScope only
	// requires it to be non-empty, so a fixed placeholder is correct here.
	scope, err := filesafe.NewAdminScope("", "admin-file-manager",
		filesafe.DefaultAdminDeniedPrefixes, filesafe.DefaultAdminMutableRoots)
	if err != nil {
		// The allow-list is a package constant; a construction failure is a
		// server fault, never caller input. Fail CLOSED — never let an
		// admin_root mutation through on a broken pre-check.
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return true
	}
	wscope := scope.WriteScope()
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, err := wscope.Clean(p); err != nil {
			code := filesafe.ErrCodeReadOnly
			var ve *filesafe.ValidationError
			if errors.As(err, &ve) {
				code = ve.Code
			}
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": code})
			return true
		}
	}
	return false
}

func (h *filesHandler) requireClaimsAndUsername(c *gin.Context) (userID, username string, ok bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", "", false
	}
	// GH #1184 admin File Manager: the /admin/files mount (adminGate already
	// verified admin + the default-off setting) uses the root scope, so there
	// is no linux home to resolve — the username is a non-empty label only.
	if h.adminRoot(c) {
		name := claims.Email
		if name == "" {
			name = "admin"
		}
		return claims.UserID, name, true
	}
	name, err := h.linuxUsername(c.Request.Context(), claims.UserID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) || err.Error() == "user has no linux account" {
			c.JSON(http.StatusForbidden, gin.H{"error": "no_linux_account"})
			return "", "", false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return "", "", false
	}
	return claims.UserID, name, true
}

func (h *filesHandler) linuxUsername(ctx context.Context, userID string) (string, error) {
	u, err := h.cfg.Users.FindByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if u == nil || u.Username == nil || *u.Username == "" {
		return "", errors.New("user has no linux account")
	}
	return *u.Username, nil
}

func requirePath(c *gin.Context) (string, bool) {
	p := c.Query("path")
	if p == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "path_required"})
		return "", false
	}
	return p, true
}

// agentErrorStatus maps panel-agent error text to HTTP status. The agent
// returns structured errors via agentwire; match on well-known substrings
// so we return 400/403/404/507 where appropriate rather than blanket 500.
func agentErrorStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "quota_exceeded"),
		strings.Contains(msg, "disk_full"):
		// 507 Insufficient Storage — semantically right for both EDQUOT
		// (per-user quota) and ENOSPC (FS full). Clients key off this.
		return http.StatusInsufficientStorage
	case strings.Contains(msg, "not_in_scope"),
		strings.Contains(msg, "symlink_escape"),
		strings.Contains(msg, "path_traversal"),
		// JAB-367: admin File Manager write allow-list rejections. The agent's
		// WriteScope refuses a mutation outside the safe data roots (read_only)
		// or inside the hard deny-list (path_denied); both are a 403, not the
		// generic 500 they used to fall through to — and they now match the
		// envelope the API-side pre-check emits for the same violation.
		strings.Contains(msg, "read_only"),
		strings.Contains(msg, "path_denied"):
		return http.StatusForbidden
	case strings.Contains(msg, "contains_null"),
		strings.Contains(msg, "bad_characters"),
		strings.Contains(msg, "not_absolute"),
		strings.Contains(msg, "too_long"),
		strings.Contains(msg, "invalid"):
		return http.StatusBadRequest
	case strings.Contains(msg, "already_exists"):
		return http.StatusConflict
	case strings.Contains(msg, "no such file"),
		strings.Contains(msg, "not_found"):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

// agentErrorCode returns a short machine-readable error key for the JSON
// response body, so clients can render a localized message without
// parsing the detail string.
func agentErrorCode(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "quota_exceeded"):
		return "quota_exceeded"
	case strings.Contains(msg, "disk_full"):
		return "disk_full"
	case strings.Contains(msg, "already_exists"):
		return "already_exists"
	// JAB-367: surface the admin FM write-allow-list codes distinctly (was
	// "agent_error") so the SPA renders the right message AND the agent path
	// matches the API-side pre-check's envelope for the same violation.
	case strings.Contains(msg, "read_only"):
		return "read_only"
	case strings.Contains(msg, "path_denied"):
		return "path_denied"
	default:
		return "agent_error"
	}
}

func respondAgentError(c *gin.Context, err error) {
	// JAB-114: derive a stable status + client code from the error, but never
	// echo the raw agent error string (paths / stderr / driver internals) to the
	// client — log it server-side instead.
	code := agentErrorCode(err)
	logAgentError(c, code, err)
	c.JSON(agentErrorStatus(err), gin.H{"error": code})
}

// ---- handlers ----

// home returns the authenticated user's starting directory. The UI calls
// this on mount to avoid needing client-side knowledge of unix username
// mapping.
func (h *filesHandler) home(c *gin.Context) {
	_, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": "/home/" + username})
}

func (h *filesHandler) list(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	p, ok := requirePath(c)
	if !ok {
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.list", filesListAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: p,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	var result filesListAgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": "bad agent response"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// tree: same as list but filtered to non-symlink directories (lazy expansion).
func (h *filesHandler) tree(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	p, ok := requirePath(c)
	if !ok {
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.list", filesListAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: p,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	var result filesListAgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": "bad agent response"})
		return
	}
	dirs := make([]filesListEntry, 0, len(result.Entries))
	for _, e := range result.Entries {
		if e.IsDir && !e.IsSymlink {
			dirs = append(dirs, e)
		}
	}
	c.JSON(http.StatusOK, filesListAgentResult{Path: result.Path, Entries: dirs})
}

// download streams file bytes back as an attachment. MVP limitation: the
// agent returns content as a JSON string, so invalid-UTF-8 binary bytes are
// lossy — acceptable for text files; binary is Phase 2.
func (h *filesHandler) download(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	p, ok := requirePath(c)
	if !ok {
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.read", filesReadAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: p, Limit: h.resolveMaxUploadBytes(c.Request.Context()),
	})
	if err != nil {
		// GH #756: downloading a folder used to 500 (files.read can't read a
		// directory). The agent now reports it as a precondition; transparently
		// serve the folder as <name>.tar.gz — the expected "download folder" UX.
		if strings.Contains(err.Error(), "path is a directory") {
			c.Set("audit_target", p+" (archive)")
			h.streamArchive(c, userID, username, []string{p}, filepath.Base(p)+".tar.gz")
			return
		}
		respondAgentError(c, err)
		return
	}
	var result filesReadAgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": "bad agent response"})
		return
	}

	var body []byte
	if result.IsBinary {
		decoded, err := base64.StdEncoding.DecodeString(result.ContentB64)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": "bad agent response"})
			return
		}
		body = decoded
	} else {
		body = []byte(result.Content)
	}

	// GH #657: the agent hard-caps reads (100 MiB); never return a partial file
	// as a successful download — it looks complete but is corrupt.
	if result.Truncated {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error":  "file_too_large",
			"detail": "file exceeds the download size limit; fetch large files over SFTP/SSH",
		})
		return
	}

	filename := filepath.Base(p)
	ct := result.MimeType
	if ct == "" {
		if byExt := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename))); byExt != "" {
			ct = byExt
		} else {
			ct = "application/octet-stream"
		}
	}

	// GH #649: only inline content a browser renders passively — raster images
	// + pdf. NEVER inline SVG/HTML/XML/JSON/text: they can carry active content
	// that would execute under the authenticated panel origin. Everything else
	// downloads as an attachment.
	inline := (strings.HasPrefix(ct, "image/") && !strings.Contains(ct, "svg")) ||
		ct == "application/pdf"
	// JAB-108: filename is user-controlled (tenants can create files named
	// with `"` / `;` / control chars over SFTP). Emit an RFC 6266-escaped
	// header so the name can't break out of the quoted value and spoof the
	// browser's save-as name.
	dispType := "attachment"
	if inline {
		dispType = "inline"
	}
	disposition := contentDisposition(dispType, filename)

	c.Header("Content-Type", ct)
	c.Header("Content-Disposition", disposition)
	c.Header("X-Content-Type-Options", "nosniff")
	// Defense-in-depth: sandbox anything served, so even a mis-typed inline
	// response can't run scripts / same-origin against the panel.
	c.Header("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'")
	c.Data(http.StatusOK, ct, body)
}

// preview returns text content capped at 1 MiB as a JSON envelope.
func (h *filesHandler) preview(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	p, ok := requirePath(c)
	if !ok {
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.read", filesReadAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: p, Limit: maxPreviewBytes,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	var result filesReadAgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": "bad agent response"})
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", "inline")
	// JAB-191: is_binary now also covers "text that is not valid UTF-8" (latin1
	// PHP/HTML and friends). Content is empty for those -- the bytes came back
	// base64 -- so the editor MUST refuse rather than open a blank buffer and
	// write that blank back over the user's file on Save.
	c.JSON(http.StatusOK, gin.H{
		"path":      result.Path,
		"size":      result.Size,
		"content":   result.Content,
		"mime_type": result.MimeType,
		"is_binary": result.IsBinary,
	})
}

// upload accepts a single multipart file under the "file" field; directory
// is taken from ?path=. Cap enforced at middleware (filesUploadSizeLimit).
//
// Path: stream the multipart part directly to /var/lib/jabali-uploads/
// jabali-upload-<uuid>, then call files.ingest. The earlier implementation
// buffered the whole file into memory and JSON-encoded it as a Content
// string into files.write — for any non-trivial size (>~10 MB) the
// resulting JSON envelope blew past the agentwire UDS write window and
// the call died with "broken pipe". The staging-dir + ingest path keeps
// the binary on disk and only sends paths over the wire, matching what
// the chunked uploader does.
func (h *filesHandler) upload(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	dirPath, ok := requirePath(c)
	if !ok {
		return
	}
	fileHeader, err := c.FormFile("file")
	if err == nil && fileHeader != nil {
		c.Set("audit_target", dirPath+"/"+filepath.Base(fileHeader.Filename)+" (upload)") // GH #658
	}
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "file_required"})
			return
		}
		if strings.Contains(err.Error(), "request body too large") {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file_too_large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	maxBytes := h.resolveMaxUploadBytes(c.Request.Context())
	if fileHeader.Size > maxBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file_too_large"})
		return
	}
	if strings.ContainsAny(fileHeader.Filename, "/\\") || fileHeader.Filename == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_filename"})
		return
	}
	src, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	defer src.Close()

	// Mint a tmp path that matches the ingest tmpUploadPrefix gate.
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	if err := ensureUploadStagingDir(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": err.Error()})
		return
	}
	// #425: single-shot staging is user-tagged so it counts against the same
	// per-user concurrency + byte budget as the chunked path.
	if cnt, total := userStagingStats(userID); cnt >= maxInFlightUploadsPerUser {
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_uploads"})
		return
	} else if total >= h.userStagingBudget(c.Request.Context()) {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "staging_budget_exceeded"})
		return
	}
	tmpPath := singleStagingPath(userID, hex.EncodeToString(idBytes))
	tmpFile, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": err.Error()})
		return
	}
	written, copyErr := io.Copy(tmpFile, io.LimitReader(src, maxBytes+1))
	if cerr := tmpFile.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": copyErr.Error()})
		return
	}
	if written > maxBytes {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file_too_large"})
		return
	}
	// #425: per-user staging byte budget across all in-flight uploads.
	if _, total := userStagingStats(userID); total > h.userStagingBudget(c.Request.Context()) {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "staging_budget_exceeded"})
		return
	}

	// Destination filename: the multipart filename, or a "name" override the
	// upload UI sends for "keep both" (auto-renamed) on a collision (GH #188).
	destName := fileHeader.Filename
	if override := c.Query("name"); override != "" {
		if strings.ContainsAny(override, "/\\") || override == "." || override == ".." {
			_ = os.Remove(tmpPath)
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_filename"})
			return
		}
		destName = override
	}
	destPath := filepath.Join(dirPath, destName)
	// GH #658: record the FINAL resolved path (after any name override /
	// collision handling) so the mutation audit reflects what was actually
	// written — supersedes any earlier pre-override audit_target on this path.
	c.Set("audit_target", destPath+" (upload)")
	c.Set("audit_target_type", "file")
	if h.rejectAdminWriteOutOfScope(c, destPath) {
		_ = os.Remove(tmpPath) // don't leak the panel staging file on a rejected admin upload
		return
	}
	_, err = h.cfg.Agent.Call(c.Request.Context(), "files.ingest", filesIngestAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), TmpPath: tmpPath, DestPath: destPath,
		Overwrite: c.Query("overwrite") == "true",
	})
	if err != nil {
		_ = os.Remove(tmpPath)
		respondAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": destPath, "bytes_written": written})
}

func (h *filesHandler) mkdir(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	var req mkdirRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if h.rejectAdminWriteOutOfScope(c, req.Path) {
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.mkdir", filesMkdirAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: req.Path,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": req.Path})
}

// extract handles POST /files/extract: unpack a .zip/.tar/.tar.gz/.tgz/
// .tar.bz2/.gz archive into a directory inside the user's scope. The agent
// enforces zip-slip, symlink, and decompression-bomb defenses.
func (h *filesHandler) extract(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	var req extractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	c.Set("audit_target", req.Path) // GH #658: audit the concrete file path
	c.Set("audit_target_type", "file")
	// Only the extraction DESTINATION is a mutation (the archive itself is read
	// from the broad scope). Mirror the agent: explicit dest, else the archive's
	// parent directory.
	extractDest := req.Dest
	if strings.TrimSpace(extractDest) == "" {
		extractDest = filepath.Dir(req.Path)
	}
	if h.rejectAdminWriteOutOfScope(c, extractDest) {
		return
	}
	// GH #1392: ?async=1 runs the extraction as a background job and returns a
	// job id immediately (202), so a large archive doesn't block the request past
	// a proxy timeout and the UI can poll GET /files/jobs/:id for a progress bar.
	if c.Query("async") == "1" {
		raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.extract.start", filesExtractAgentParams{
			UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: req.Path, Dest: req.Dest,
		})
		if err != nil {
			respondAgentError(c, err)
			return
		}
		c.Data(http.StatusAccepted, "application/json", raw)
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.extract", filesExtractAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: req.Path, Dest: req.Dest,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	var res json.RawMessage = raw
	c.Data(http.StatusOK, "application/json", res)
}

// jobStatus handles GET /files/jobs/:id — the progress of an async File Manager
// operation (GH #1392). The agent returns the job ONLY when it was started for
// this caller's username (sent from verified claims), so one tenant can never
// poll another's job; an unknown id or wrong owner is a 404.
func (h *filesHandler) jobStatus(c *gin.Context) {
	_, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.job.status", map[string]string{
		"job_id":   c.Param("id"),
		"username": username,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

func (h *filesHandler) rename(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	var req renameRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	// new_name is a single path segment — no separators, no .. escape.
	if strings.ContainsAny(req.NewName, "/\\") || req.NewName == "." || req.NewName == ".." {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_new_name"})
		return
	}
	newPath := filepath.Join(filepath.Dir(req.Path), req.NewName)
	// The agent WriteScopes both the old and new paths; mirror that.
	if h.rejectAdminWriteOutOfScope(c, req.Path, newPath) {
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.rename", filesRenameAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), OldPath: req.Path, NewPath: newPath,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"old_path": req.Path, "new_path": newPath})
}

// move handles POST /files/move: relocate a file or directory into a
// different parent directory. Semantically distinct from rename, which
// is same-parent only. Used by the drag-and-drop flow.
func (h *filesHandler) move(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	var req moveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	c.Set("audit_target", req.Path) // GH #658: audit the concrete file path
	c.Set("audit_target_type", "file")
	// Dest must look like an absolute path (validated inside the agent
	// against the user's scope), never bare "..". Client should not be
	// able to coerce us into moving into the docroot's parent by sending
	// just "..".
	if strings.Contains(req.DestDir, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_dest_dir"})
		return
	}
	newPath := filepath.Join(req.DestDir, filepath.Base(req.Path))
	// The agent WriteScopes both the source and destination; mirror that.
	if h.rejectAdminWriteOutOfScope(c, req.Path, newPath) {
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.move", filesMoveAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), OldPath: req.Path, NewPath: newPath,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"old_path": req.Path, "new_path": newPath})
}

// chmod handles POST /files/chmod: set permission bits on a single file
// or directory. Bulk chmod from the UI loops this endpoint per entry —
// keeps the backend contract small (one path per call) and lets the
// frontend surface per-item failures without an all-or-nothing atomic
// guarantee we can't actually deliver on a real filesystem.
func (h *filesHandler) chmod(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	var req chmodRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	c.Set("audit_target", req.Path) // GH #658: audit the concrete file path
	c.Set("audit_target_type", "file")
	if h.rejectAdminWriteOutOfScope(c, req.Path) {
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.chmod", filesChmodAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: req.Path, Mode: req.Mode,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": req.Path, "mode": req.Mode})
}

// archive handles POST /files/archive: the agent builds a tar.gz of
// every requested path into a tmp file owned by the panel process, we
// stream that file back to the client with the right headers, then
// unlink. One HTTP request per download — no cleanup token dance.
func (h *filesHandler) archive(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	var req archiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	if len(req.Paths) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "paths_required"})
		return
	}
	c.Set("audit_target", strings.Join(req.Paths, ", ")+" (archive)") // GH #658
	h.streamArchive(c, userID, username, req.Paths, "archive.tar.gz")
}

// streamArchive asks the agent to build a tar.gz of the given scoped paths
// into a panel-owned tmp file, streams it back with the given download name,
// then unlinks the scratch file. Shared by the /files/archive endpoint and the
// download handler's folder fallback (GH #756).
func (h *filesHandler) streamArchive(c *gin.Context, userID, username string, paths []string, downloadName string) {
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "files.archive", filesArchiveAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Paths: paths,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	var result filesArchiveAgentResult
	if err := json.Unmarshal(raw, &result); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": "bad agent response"})
		return
	}
	// Always remove the scratch file after we're done streaming —
	// success, client disconnect, or stat failure. A leftover staging
	// file would accumulate per-download otherwise.
	defer func() { _ = os.Remove(result.ArchivePath) }()

	f, err := os.Open(result.ArchivePath)
	if err != nil {
		// JAB-192: this branch used to return a bare 500 carrying the raw
		// os error — which is how the GH #756 namespace bug stayed opaque
		// for so long. The agent reported success, the panel could not see
		// the file it had just been handed, and the operator got
		// "internal_error" with a path and no cause.
		//
		// A missing file here is specifically a HANDOFF failure: the agent
		// wrote the archive somewhere this process cannot see, or a reaper
		// removed it before we opened it. That is a server-side
		// misconfiguration, never anything the caller did, so it gets its
		// own code and an operator-actionable log line rather than being
		// folded in with genuine internal errors.
		if os.IsNotExist(err) {
			if h.cfg.Log != nil {
				h.cfg.Log.Error("archive handoff failed: agent reported an archive this process cannot see",
					"path", result.ArchivePath,
					"hint", "the agent must stage archives somewhere visible to panel-api; "+
						"a PrivateTmp path is per-unit and will never be readable here (GH #756)")
			}
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "archive_unavailable",
				"detail": "the archive was created but could not be read back by the panel. " +
					"This is a server configuration problem, not a problem with the files being downloaded.",
			})
			return
		}
		if h.cfg.Log != nil {
			h.cfg.Log.Error("archive open failed", "err", err, "path", result.ArchivePath)
		}
		// Path deliberately not echoed to the client (JAB-114: handlers must
		// not leak internal filesystem detail).
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  "internal_error",
			"detail": "the archive could not be read back by the panel",
		})
		return
	}
	defer f.Close()

	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", contentDisposition("attachment", downloadName))
	c.Header("Content-Length", fmt.Sprintf("%d", result.Size))
	if _, err := io.Copy(c.Writer, f); err != nil {
		// Response already started — nothing useful to do, just log
		// via the request logger.
		if h.cfg.Log != nil {
			h.cfg.Log.Warn("archive stream failed", "err", err, "path", result.ArchivePath)
		}
	}
}

// copy handles POST /files/copy: recursively copies a scoped path
// into a different parent directory. Distinct from move (which
// relinks) and rename (same-parent-only).
func (h *filesHandler) copy(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	var req copyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	c.Set("audit_target", req.Path) // GH #658: audit the concrete file path
	c.Set("audit_target_type", "file")
	if strings.Contains(req.DestDir, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_dest_dir"})
		return
	}
	dst := filepath.Join(req.DestDir, filepath.Base(req.Path))
	// The agent WriteScopes both the source and destination of a copy; mirror
	// that (an admin FM copy stays within the safe data roots on both ends).
	if h.rejectAdminWriteOutOfScope(c, req.Path, dst) {
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.copy", filesCopyAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), SrcPath: req.Path, DstPath: dst,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"src_path": req.Path, "dst_path": dst})
}

// write handles POST /files/write: overwrites / creates a file with
// the given UTF-8 content. Used by the Monaco editor's Save action.
func (h *filesHandler) write(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	var req writeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}
	c.Set("audit_target", req.Path) // GH #658: audit the concrete file path
	c.Set("audit_target_type", "file")
	if h.rejectAdminWriteOutOfScope(c, req.Path) {
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.write", filesWriteAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: req.Path, Content: req.Content,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": req.Path, "size": len(req.Content)})
}

// uploadChunk handles POST /files/upload-chunk: streams a binary chunk
// to /var/lib/jabali-uploads/jabali-upload-<id>, appending at `offset`.
// When `final=1` is set on the last chunk, the accumulated staging file
// is moved into the user's scope at `path/name` via the agent's
// files.ingest command.
//
// No auth other than the usual user check — upload_ids are random UUIDs
// kept only in the client's memory, so there's no cross-user collision
// surface unless two separate clients both guess the same 128-bit id.
func (h *filesHandler) uploadChunk(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	uploadID := c.Query("upload_id")
	offsetStr := c.Query("offset")
	destDir := c.Query("path")
	filename := c.Query("name")
	isFinal := c.Query("final") == "1"
	if uploadID == "" || offsetStr == "" || destDir == "" || filename == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing_upload_params"})
		return
	}
	// Keep uploadID safe as a filesystem basename — no path separators,
	// no ".." — since we string-interpolate it into the staging prefix.
	if strings.ContainsAny(uploadID, "/\\.") || uploadID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	if strings.ContainsAny(filename, "/\\") || filename == "." || filename == ".." {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_filename"})
		return
	}
	var offset int64
	if _, err := fmt.Sscanf(offsetStr, "%d", &offset); err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_offset"})
		return
	}
	if err := ensureUploadStagingDir(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": err.Error()})
		return
	}
	// #426: the staging path is derived from the authenticated user, so a
	// different tenant who learns this upload_id cannot target this file.
	tmpPath := chunkStagingPath(userID, uploadID)

	// #425: per-user concurrency + disk-budget cap, checked when a NEW upload
	// begins (offset 0 and the staging file doesn't exist yet).
	if offset == 0 {
		if _, statErr := os.Stat(tmpPath); errors.Is(statErr, os.ErrNotExist) {
			if cnt, total := userStagingStats(userID); cnt >= maxInFlightUploadsPerUser {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_uploads"})
				return
			} else if total >= h.userStagingBudget(c.Request.Context()) {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "staging_budget_exceeded"})
				return
			}
		}
	}

	// #426: first chunk creates O_EXCL (a fresh session); later chunks require
	// the staging file to already exist. The offset must equal the current file
	// size — no holes, no mid-file overwrite, no cross-session content injection.
	openFlags := os.O_WRONLY
	if offset == 0 {
		openFlags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(tmpPath, openFlags, 0o600)
	if err != nil {
		if offset == 0 && errors.Is(err, os.ErrExist) {
			// Idempotent retry of the first chunk after a blip: reopen and let
			// the offset/size check below decide.
			f, err = os.OpenFile(tmpPath, os.O_WRONLY, 0o600)
		}
		if err != nil {
			if offset > 0 && errors.Is(err, os.ErrNotExist) {
				c.JSON(http.StatusConflict, gin.H{"error": "upload_not_found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": err.Error()})
			return
		}
	}
	fi, statErr := f.Stat()
	if statErr != nil {
		f.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": statErr.Error()})
		return
	}
	if offset != fi.Size() {
		// Reject holes / mid-file overwrites; client must resume from the actual
		// size (query upload-chunk-status).
		f.Close()
		c.JSON(http.StatusConflict, gin.H{"error": "bad_offset", "expected": fi.Size()})
		return
	}
	if _, err := f.Seek(offset, 0); err != nil {
		f.Close()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": err.Error()})
		return
	}
	// Cap the assembled upload at the admin-configured limit
	// (server_settings.upload_max_size_mb; #211). Was a hardcoded 1 GB
	// that silently overrode the setting for the chunked path (every
	// file > 100 MB), so raising Upload max size never took effect for
	// large uploads. Check via stat AFTER the write to keep the
	// per-chunk hot path simple.
	maxUploadSize := h.resolveMaxUploadBytes(c.Request.Context())
	written, copyErr := io.Copy(f, io.LimitReader(c.Request.Body, maxUploadSize-offset+1))
	if cerr := f.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error", "detail": copyErr.Error()})
		return
	}
	if offset+written > maxUploadSize {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file_too_large"})
		return
	}
	// #425: enforce the per-user staging byte budget as the upload grows.
	if _, total := userStagingStats(userID); total > h.userStagingBudget(c.Request.Context()) {
		_ = os.Remove(tmpPath)
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "staging_budget_exceeded"})
		return
	}

	if !isFinal {
		c.JSON(http.StatusOK, gin.H{"upload_id": uploadID, "written": written})
		return
	}

	// Final chunk: hand off to the agent to ingest the staging file
	// into the user's homedir scope at the requested path/name.
	destPath := filepath.Join(destDir, filename)
	// GH #658: the chunked-upload finalize must record the mutation target too.
	c.Set("audit_target", destPath+" (chunked upload)")
	c.Set("audit_target_type", "file")
	if h.rejectAdminWriteOutOfScope(c, destPath) {
		_ = os.Remove(tmpPath) // don't leak the panel staging file on a rejected admin upload
		return
	}
	_, err = h.cfg.Agent.Call(c.Request.Context(), "files.ingest", filesIngestAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), TmpPath: tmpPath, DestPath: destPath,
		Overwrite: c.Query("overwrite") == "true",
	})
	if err != nil {
		_ = os.Remove(tmpPath)
		respondAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": destPath})
}

// uploadChunkStatus returns the current byte count of a pending upload
// so the client can resume from the right offset after a reload or a
// network blip. Only reports staging files — there's no cross-user
// isolation on the scratch path, but since upload_ids are 128-bit
// random UUIDs the risk is limited to the client leaking its own id,
// which would already be in their localStorage anyway.
func (h *filesHandler) uploadChunkStatus(c *gin.Context) {
	userID, _, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	uploadID := c.Query("upload_id")
	if uploadID == "" || strings.ContainsAny(uploadID, "/\\.") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	// Per-user path (#426): a tenant can only stat their OWN staging files.
	tmpPath := chunkStagingPath(userID, uploadID)
	info, err := os.Stat(tmpPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"upload_id": uploadID, "written": info.Size()})
}

func (h *filesHandler) delete(c *gin.Context) {
	userID, username, ok := h.requireClaimsAndUsername(c)
	if !ok {
		return
	}
	p, ok := requirePath(c)
	if !ok {
		return
	}
	recursive := c.Query("recursive") == "true"
	c.Set("audit_target", p) // GH #658: audit which path was deleted
	c.Set("audit_target_type", "file")
	if h.rejectAdminWriteOutOfScope(c, p) {
		return
	}
	_, err := h.cfg.Agent.Call(c.Request.Context(), "files.delete", filesDeleteAgentParams{
		UserID: userID, Username: username, AdminRoot: h.adminRoot(c), Path: p, Recursive: recursive,
	})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"path": p})
}
