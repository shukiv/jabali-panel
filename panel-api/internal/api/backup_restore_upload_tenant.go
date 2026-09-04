// Tenant self-service Restore-from-Upload (GH #1408, lxsdevcode). A tenant
// uploads a backup archive of THEIR OWN account and restores it — the mirror of
// the admin /admin/backups/restore-upload flow, but:
//
//   - owner-scoped: the target is ALWAYS the caller (never a chosen username);
//   - the uploaded tar is UNTRUSTED (a lower-privilege actor), so the apply
//     passes the caller's OWNED database names + mail domains as allowlists and
//     the agent skips anything else (mode="tenant"); components are limited to
//     files/db/mail (docker/dns are cut — they write manifest-keyed global state);
//   - FAIL-CLOSED against an old agent: the apply first checks the agent reports
//     `allowlist_supported` (via inspect, no extraction) and refuses to run
//     otherwise, so a pre-feature agent can never apply an unrestricted restore;
//   - no panel-row metadata rebuild (that path recreates domains/db rows from the
//     untrusted bundle — a hijack vector; the caller's own rows already exist).
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupmetadata"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func restoreUploadTenantTag(userID string) string {
	sum := sha256.Sum256([]byte("jabali-restore-upload-tenant:" + userID))
	return hex.EncodeToString(sum[:])[:16]
}

func restoreUploadTenantPath(userID, uploadID string) string {
	sum := sha256.Sum256([]byte("jabali-restore-upload-tenant-path:" + userID + ":" + uploadID))
	return filepath.Join(restoreUploadDir, "rupt-"+restoreUploadTenantTag(userID)+"-"+hex.EncodeToString(sum[:])+".tar.zst")
}

func restoreUploadTenantOutcomePath(userID, uploadID string) string {
	sum := sha256.Sum256([]byte("jabali-restore-upload-tenant-outcome:" + userID + ":" + uploadID))
	return filepath.Join(restoreUploadDir, "ruot-"+restoreUploadTenantTag(userID)+"-"+hex.EncodeToString(sum[:])+".json")
}

func evictStaleTenantRestoreUploads(userID string) {
	tag := restoreUploadTenantTag(userID)
	cutoff := time.Now().Add(-restoreUploadTTL)
	for _, pat := range []string{"rupt-" + tag + "-*.tar.zst", "ruot-" + tag + "-*.json"} {
		matches, _ := filepath.Glob(filepath.Join(restoreUploadDir, pat))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.ModTime().Before(cutoff) {
				_ = os.Remove(m)
			}
		}
	}
}

func (cfg MeBackupsHandlerConfig) allUserDomainNames(ctx context.Context, userID string) []string {
	out := []string{} // NEVER nil — an empty (present) list means "enforce, owns none"
	if cfg.Domains == nil {
		return out
	}
	doms, _, err := cfg.Domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: 10000})
	if err != nil {
		return out
	}
	for _, d := range doms {
		out = append(out, d.Name)
	}
	return out
}

func (h *meBackupHandler) restoreUploadUserID(c *gin.Context) (string, bool) {
	claims := ginctx.Claims(c)
	if claims == nil || claims.UserID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return "", false
	}
	return claims.UserID, true
}

// restoreUploadChunk: POST /me/backups/restore-upload?upload_id=&offset=[&final=1].
// Owner-scoped mirror of the admin chunk reassembly; body-limit exempt.
func (h *meBackupHandler) restoreUploadChunk(c *gin.Context) {
	userID, ok := h.restoreUploadUserID(c)
	if !ok {
		return
	}
	uploadID := c.Query("upload_id")
	if !uploadIDRE.MatchString(uploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	offset, err := strconv.ParseInt(c.Query("offset"), 10, 64)
	if err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_offset"})
		return
	}
	if err := os.MkdirAll(restoreUploadDir, 0o750); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "staging_unavailable"})
		return
	}
	evictStaleTenantRestoreUploads(userID)

	path := restoreUploadTenantPath(userID, uploadID)
	if offset == 0 {
		if _, statErr := os.Stat(path); statErr != nil {
			matches, _ := filepath.Glob(filepath.Join(restoreUploadDir, "rupt-"+restoreUploadTenantTag(userID)+"-*.tar.zst"))
			if len(matches) >= maxInFlightRestoreTar {
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "too_many_uploads", "detail": "finish or wait for an in-flight restore upload"})
				return
			}
		}
	}
	if offset >= maxRestoreUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "archive_too_large"})
		return
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "open_staging_failed"})
		return
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "seek_failed"})
		return
	}
	remaining := maxRestoreUploadBytes - offset
	written, werr := io.Copy(f, io.LimitReader(c.Request.Body, remaining))
	if werr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "write_failed", "detail": werr.Error()})
		return
	}
	if written == remaining { // exactly at the cap — reject if more bytes remain (silent-truncation guard)
		var probe [1]byte
		if n, _ := c.Request.Body.Read(probe[:]); n > 0 {
			_ = os.Remove(path)
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "archive_too_large"})
			return
		}
	}
	if c.Query("final") == "1" {
		size := offset + written
		if fi, serr := f.Stat(); serr == nil {
			size = fi.Size()
		}
		c.JSON(http.StatusOK, gin.H{"upload_id": uploadID, "size": size})
		return
	}
	c.JSON(http.StatusOK, gin.H{"upload_id": uploadID, "written": written, "offset": offset + written})
}

// restoreUploadInspect returns the backup's user + restorable components (the UI
// filters to files/db/mail on the tenant side).
func (h *meBackupHandler) restoreUploadInspect(c *gin.Context) {
	userID, ok := h.restoreUploadUserID(c)
	if !ok {
		return
	}
	var req restoreUploadInspectRequest
	if err := c.ShouldBindJSON(&req); err != nil || !uploadIDRE.MatchString(req.UploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	path := restoreUploadTenantPath(userID, req.UploadID)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		c.JSON(http.StatusNotFound, gin.H{"error": "upload_not_found"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "backup.inspect_uploaded_tar", map[string]string{"tar_path": path})
	if err != nil {
		respondAgentError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

type tenantRestoreUploadApplyRequest struct {
	UploadID   string   `json:"upload_id"`
	Components []string `json:"components,omitempty"`
}

// restoreUploadApply restores the uploaded archive into the CALLER's own account.
// Detached (minutes-long) + 202 + status poll, like the admin path. Fails closed
// on an agent that doesn't advertise allowlist support (checked before applying).
func (h *meBackupHandler) restoreUploadApply(c *gin.Context) {
	userID, ok := h.restoreUploadUserID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	u, uerr := h.cfg.Users.FindByID(ctx, userID)
	if uerr != nil || u == nil || u.Username == nil || *u.Username == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "user_lookup_failed"})
		return
	}
	username := *u.Username

	var req tenantRestoreUploadApplyRequest
	if err := c.ShouldBindJSON(&req); err != nil || !uploadIDRE.MatchString(req.UploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request"})
		return
	}
	path := restoreUploadTenantPath(userID, req.UploadID)
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		c.JSON(http.StatusNotFound, gin.H{"error": "upload_not_found"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}

	// FAIL-CLOSED capability gate: confirm the agent enforces the ownership
	// allowlists BEFORE we let it touch the live system. inspect reads only the
	// manifest (no extraction), so an old agent is refused with zero live change.
	iraw, ierr := h.cfg.Agent.Call(ctx, "backup.inspect_uploaded_tar", map[string]string{"tar_path": path})
	if ierr != nil {
		respondAgentError(c, ierr)
		return
	}
	var insp struct {
		AllowlistSupported bool `json:"allowlist_supported"`
	}
	if json.Unmarshal(iraw, &insp) != nil || !insp.AllowlistSupported {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "agent_update_required",
			"detail": "the server agent must be updated before self-service restore can run safely",
		})
		return
	}

	// Owned resources → allowlists (ALWAYS non-nil; an empty present list means
	// "enforce, owns none" — the agent skips every db/mail stage). The DB
	// allowlist reuses the shared backup content selection (JAB-324): a lookup
	// failure leaves the allowlist empty (fail-closed — the restore rejects the
	// DB components) and is logged, never silently swallowed.
	sel, warns := backupmetadata.SelectAll(ctx, h.cfg.metadataDeps(), userID, false)
	backupmetadata.LogWarnings(h.cfg.Log, warns)
	allowedDBs := append([]string{}, sel.MariaDB...)
	allowedDBs = append(allowedDBs, sel.Postgres...)
	allowedDomains := h.cfg.allUserDomainNames(ctx, userID)
	components := intersectRestoreComponents(req.Components, []string{"home", "db", "mail"})

	c.Set("audit_target", username)
	c.Set("audit_target_type", "user")
	evictStaleTenantRestoreUploads(userID)

	outcomePath := restoreUploadTenantOutcomePath(userID, req.UploadID)
	writeRestoreUploadOutcome(outcomePath, "restoring", nil, nil, "")
	go h.runTenantUploadRestore(tenantUploadRestoreArgs{
		path:           path,
		outcomePath:    outcomePath,
		username:       username,
		components:     components,
		allowedDBs:     allowedDBs,
		allowedDomains: allowedDomains,
	})
	c.JSON(http.StatusAccepted, gin.H{"status": "restoring", "upload_id": req.UploadID})
}

type tenantUploadRestoreArgs struct {
	path           string
	outcomePath    string
	username       string
	components     []string
	allowedDBs     []string
	allowedDomains []string
}

func (h *meBackupHandler) runTenantUploadRestore(a tenantUploadRestoreArgs) {
	ctx, cancel := context.WithTimeout(context.Background(), restoreJobTimeout)
	defer cancel()

	// Guarantee non-nil so the wire carries [] (enforce) not null (unrestricted);
	// the agent's mode=tenant guard also rejects a nil list as belt-and-suspenders.
	if a.allowedDBs == nil {
		a.allowedDBs = []string{}
	}
	if a.allowedDomains == nil {
		a.allowedDomains = []string{}
	}
	raw, err := h.cfg.Agent.Call(ctx, "backup.restore_from_tar", map[string]any{
		"job_id":               ids.NewULID(),
		"tar_path":             a.path,
		"target_username":      a.username,
		"components":           a.components,
		"mode":                 "tenant",
		"allowed_db_names":     a.allowedDBs,
		"allowed_mail_domains": a.allowedDomains,
	})
	if err != nil {
		writeRestoreUploadOutcome(a.outcomePath, "failed", nil, nil, restoreFailureDetail(err))
		_ = os.Remove(a.path)
		return
	}
	var result struct {
		Applied               []string `json:"applied"`
		Warnings              []string `json:"warnings"`
		DBAllowlistEnforced   bool     `json:"db_allowlist_enforced"`
		MailAllowlistEnforced bool     `json:"mail_allowlist_enforced"`
	}
	_ = json.Unmarshal(raw, &result)
	// Belt-and-suspenders: the pre-apply inspect gate should have caught an old
	// agent, but if the applied restore didn't report enforcement, treat it as a
	// failure rather than trust an unrestricted run.
	if !result.DBAllowlistEnforced || !result.MailAllowlistEnforced {
		writeRestoreUploadOutcome(a.outcomePath, "failed", result.Applied, result.Warnings,
			"restore did not confirm ownership enforcement — aborted")
		_ = os.Remove(a.path)
		return
	}

	// v1 deliberately does NOT rebuild panel metadata rows from the untrusted
	// bundle (that path recreates domains/db rows from manifest data = a hijack
	// vector). The caller's own rows already exist; the data restore is what a
	// self-service restore needs.
	_ = os.Remove(a.path)
	writeRestoreUploadOutcome(a.outcomePath, "done", result.Applied, result.Warnings, "")
}

func (h *meBackupHandler) restoreUploadStatus(c *gin.Context) {
	userID, ok := h.restoreUploadUserID(c)
	if !ok {
		return
	}
	uploadID := c.Query("upload_id")
	if !uploadIDRE.MatchString(uploadID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_upload_id"})
		return
	}
	o, err := readRestoreUploadOutcome(restoreUploadTenantOutcomePath(userID, uploadID))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	c.JSON(http.StatusOK, o)
}

// intersectRestoreComponents restricts a requested component set to the allowed
// set; an empty request ("all") becomes the allowed set (never empty → can't
// fall through to an unrestricted apply).
func intersectRestoreComponents(requested, allowed []string) []string {
	if len(requested) == 0 {
		return allowed
	}
	allow := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allow[a] = true
	}
	out := make([]string, 0, len(requested))
	for _, r := range requested {
		if allow[r] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return []string{"__none__"} // matches no stage — apply nothing
	}
	return out
}
