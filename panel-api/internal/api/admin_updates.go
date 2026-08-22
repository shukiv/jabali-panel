package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// AdminUpdatesHandlerConfig holds dependencies for the system-updates endpoints.
//
// M29 shipped this as a thin proxy (Agent only). M53 (ADR-0118) makes it
// stateful: check results are persisted to update_state for instant page
// loads, runs are logged to update_history, and the run reconciler marks
// running rows finished. State/History nil -> falls back to M29 proxy
// behaviour (no persistence, /state + /history return 503).
type AdminUpdatesHandlerConfig struct {
	Agent          agent.AgentInterface
	State          repository.UpdateStateRepository
	History        repository.UpdateHistoryRepository
	ServerSettings repository.ServerSettingsRepository
	Autoupdate     repository.UpdateAutoupdateConfigRepository
}

const (
	jabaliUpdateUnit = "jabali-update-oneshot.service"
	aptUpdateUnit    = "jabali-apt-oneshot.service"
	repairUnit       = "jabali-repair-oneshot.service"
)

// RegisterAdminUpdatesRoutes mounts the update endpoints. M29 proxy routes
// plus M53 /state, /history, /autoupdate.
func RegisterAdminUpdatesRoutes(g *gin.RouterGroup, cfg AdminUpdatesHandlerConfig) {
	if cfg.Agent == nil {
		return
	}
	h := &adminUpdatesHandler{cfg: cfg}
	grp := g.Group("/admin/updates")
	grp.Use(middleware.RequireAdmin())
	grp.GET("/jabali/check", h.jabaliCheck)
	grp.POST("/jabali/run", h.jabaliRun)
	grp.GET("/jabali/status", h.jabaliStatus)
	grp.DELETE("/jabali", h.jabaliStop)
	grp.GET("/apt/check", h.aptCheck)
	grp.POST("/apt/run", h.aptRun)
	grp.GET("/apt/status", h.aptStatus)
	grp.DELETE("/apt", h.aptStop)
	// M53 stateful additions.
	grp.GET("/state", h.state)
	grp.GET("/history", h.history)
	grp.GET("/autoupdate", h.autoupdateGet)
	grp.PUT("/autoupdate", h.autoupdatePut)
	// Repair Center (jabali repair). diagnose is read-only + synchronous;
	// run fires `jabali repair --auto` as a transient unit (a safe fix can
	// restart jabali-panel) and the UI polls /repair/status for output.
	grp.POST("/repair/diagnose", h.repairDiagnose)
	grp.POST("/repair/run", h.repairRun)
	grp.GET("/repair/status", h.repairStatus)
	grp.DELETE("/repair", h.repairStop)
	// GH #576 — domain repair helpers, surfaced from the Repair Center. Both
	// proxy the operator `jabali domain …` CLI via the agent (synchronous;
	// neither restarts the panel). Orphan prune is dry-run unless apply=true.
	grp.POST("/repair/domain/fix-perms", h.repairDomainFixPerms)
	grp.GET("/repair/domain/orphans", h.repairDomainOrphans)
	grp.POST("/repair/domain/orphans/prune", h.repairDomainOrphansPrune)
}

type adminUpdatesHandler struct{ cfg AdminUpdatesHandlerConfig }

// callAgentRaw proxies to the agent and returns the raw bytes so callers can
// decode a typed view for persistence without a second agent round-trip.
func (h *adminUpdatesHandler) callAgentRaw(c *gin.Context, cmd string, params any, timeout time.Duration) (json.RawMessage, bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, cmd, params)
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return nil, false
	}
	return raw, true
}

// writeAgentJSON parses raw agent output and writes it to the client.
func writeAgentJSON(c *gin.Context, raw json.RawMessage) {
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_parse"})
		return
	}
	c.JSON(http.StatusOK, data)
}

func (h *adminUpdatesHandler) callAgent(c *gin.Context, cmd string, params any, timeout time.Duration) {
	raw, ok := h.callAgentRaw(c, cmd, params, timeout)
	if !ok {
		return
	}
	writeAgentJSON(c, raw)
}

// --- jabali ----------------------------------------------------------------

// jabaliCheckResult mirrors the agent's system.update_check wire shape.
type jabaliCheckResult struct {
	CurrentSHA  string `json:"current_sha"`
	BehindCount int    `json:"behind_count"`
}

func (h *adminUpdatesHandler) jabaliCheck(c *gin.Context) {
	// Release channel (GH #445): tell the agent which ref to compare against
	// so the "behind" count reflects stable vs development.
	channel := "development"
	if h.cfg.ServerSettings != nil {
		if s, err := h.cfg.ServerSettings.Get(c.Request.Context()); err == nil && s != nil && s.ReleaseChannel == "stable" {
			channel = "stable"
		}
	}
	raw, ok := h.callAgentRaw(c, "system.update_check", map[string]any{"channel": channel}, 60*time.Second)
	if !ok {
		return
	}
	if h.cfg.State != nil {
		var r jabaliCheckResult
		if json.Unmarshal(raw, &r) == nil {
			// Persistence failures must not fail the check response.
			_ = h.cfg.State.UpsertJabali(c.Request.Context(), r.BehindCount, r.CurrentSHA, time.Now())
		}
	}
	writeAgentJSON(c, raw)
}

func (h *adminUpdatesHandler) jabaliRun(c *gin.Context) {
	raw, ok := h.callAgentRaw(c, "system.update_run", map[string]any{}, 10*time.Second)
	if !ok {
		return
	}
	h.logRun(c.Request.Context(), models.UpdateKindJabali, jabaliUpdateUnit, 0)
	writeAgentJSON(c, raw)
}

func (h *adminUpdatesHandler) jabaliStatus(c *gin.Context) {
	h.callAgent(c, "system.update_status", map[string]any{"since": c.Query("since")}, 15*time.Second)
}

func (h *adminUpdatesHandler) jabaliStop(c *gin.Context) {
	h.callAgent(c, "system.unit_stop", map[string]any{"unit": jabaliUpdateUnit}, 10*time.Second)
}

// --- repair ----------------------------------------------------------------

// repairDiagnose runs `jabali repair --diagnose` (read-only) and returns the
// detector text. Synchronous: it changes nothing, so it can't restart a
// service mid-call. 120s ceiling covers the slowest detector sweep.
func (h *adminUpdatesHandler) repairDiagnose(c *gin.Context) {
	h.callAgent(c, "system.repair_diagnose", map[string]any{}, 120*time.Second)
}

// repairRun fires `jabali repair --auto` as a transient unit and returns the
// unit + started_at; the UI polls repairStatus for progress + output.
func (h *adminUpdatesHandler) repairRun(c *gin.Context) {
	h.callAgent(c, "system.repair_run", map[string]any{}, 10*time.Second)
}

func (h *adminUpdatesHandler) repairStatus(c *gin.Context) {
	h.callAgent(c, "system.repair_status", map[string]any{"since": c.Query("since")}, 15*time.Second)
}

func (h *adminUpdatesHandler) repairStop(c *gin.Context) {
	h.callAgent(c, "system.unit_stop", map[string]any{"unit": repairUnit}, 10*time.Second)
}

// --- GH #576 domain repair -------------------------------------------------

// repairDomainFixPerms runs `jabali domain fix-perms <username>` on the host
// (idempotent docroot group/setgid retrofit). Body: {"username": "..."}.
func (h *adminUpdatesHandler) repairDomainFixPerms(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "details": err.Error()})
		return
	}
	h.callAgent(c, "domain.repair_fix_perms", map[string]any{"username": req.Username}, 120*time.Second)
}

// repairDomainOrphans lists orphan nginx sites (dry-run; nothing deleted).
func (h *adminUpdatesHandler) repairDomainOrphans(c *gin.Context) {
	h.callAgent(c, "domain.repair_orphans", map[string]any{"apply": false}, 30*time.Second)
}

// repairDomainOrphansPrune deletes the orphan sites (irreversible). Guarded by
// the UI confirm; apply is forced true here so this route is never a dry-run.
func (h *adminUpdatesHandler) repairDomainOrphansPrune(c *gin.Context) {
	h.callAgent(c, "domain.repair_orphans", map[string]any{"apply": true}, 120*time.Second)
}

// --- apt -------------------------------------------------------------------

// aptCheckResult mirrors the agent's system.apt_check wire shape. Error is the
// structured diagnostic (JAB-10) present when the check itself failed (apt lock,
// repo unreachable, …); when set, Total/SecurityTotal are meaningless.
type aptCheckResult struct {
	Total         int `json:"total"`
	SecurityTotal int `json:"security_total"`
	Error         *struct {
		Reason   string `json:"reason"`
		Command  string `json:"command"`
		ExitCode int    `json:"exit_code"`
	} `json:"error"`
}

func (h *adminUpdatesHandler) aptCheck(c *gin.Context) {
	raw, ok := h.callAgentRaw(c, "system.apt_check", nil, 120*time.Second)
	if !ok {
		return
	}
	// Persist the counts only on a clean check. On a structured failure
	// (JAB-10) we do NOT UpsertApt — clobbering the last-known-good total
	// with a failed-check 0 would misreport "0 updates" as if healthy.
	var r aptCheckResult
	decoded := json.Unmarshal(raw, &r) == nil
	if h.cfg.State != nil && decoded && r.Error == nil {
		_ = h.cfg.State.UpsertApt(c.Request.Context(), r.Total, r.SecurityTotal, time.Now())
	}
	if decoded && r.Error != nil {
		log.Printf("[updates] system.apt_check failed: reason=%s command=%q exit=%d",
			r.Error.Reason, r.Error.Command, r.Error.ExitCode)
	}
	writeAgentJSON(c, raw)
}

func (h *adminUpdatesHandler) aptRun(c *gin.Context) {
	raw, ok := h.callAgentRaw(c, "system.apt_run", map[string]any{}, 10*time.Second)
	if !ok {
		return
	}
	sec := 0
	if h.cfg.State != nil {
		if st, err := h.cfg.State.Get(c.Request.Context()); err == nil {
			sec = st.AptSecurity
		}
	}
	h.logRun(c.Request.Context(), models.UpdateKindApt, aptUpdateUnit, sec)
	writeAgentJSON(c, raw)
}

func (h *adminUpdatesHandler) aptStatus(c *gin.Context) {
	h.callAgent(c, "system.apt_status", map[string]any{"since": c.Query("since")}, 15*time.Second)
}

func (h *adminUpdatesHandler) aptStop(c *gin.Context) {
	h.callAgent(c, "system.unit_stop", map[string]any{"unit": aptUpdateUnit}, 10*time.Second)
}

// logRun records a run-started history row (status=running). The run
// reconciler marks it finished once the transient unit terminates, so the
// row is durable even if no UI is polling. Best-effort.
func (h *adminUpdatesHandler) logRun(ctx context.Context, kind, unit string, securityCount int) {
	if h.cfg.History == nil {
		return
	}
	_ = h.cfg.History.Insert(ctx, &models.UpdateHistory{
		Kind:          kind,
		Action:        models.UpdateActionRun,
		Status:        models.UpdateStatusRunning,
		SecurityCount: securityCount,
		Unit:          unit,
		StartedAt:     time.Now(),
	})
}

// --- M53 state + history ---------------------------------------------------

func (h *adminUpdatesHandler) state(c *gin.Context) {
	if h.cfg.State == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "state_unavailable"})
		return
	}
	st, err := h.cfg.State.EnsureDefault(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "state_read"})
		return
	}
	c.JSON(http.StatusOK, st)
}

func (h *adminUpdatesHandler) history(c *gin.Context) {
	if h.cfg.History == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "history_unavailable"})
		return
	}
	limit := 20
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	rows, err := h.cfg.History.List(c.Request.Context(), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "history_read"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows, "total": len(rows)})
}

// --- M53 auto-update config (DB-as-truth; reconciler converges to host) -----

func (h *adminUpdatesHandler) autoupdateGet(c *gin.Context) {
	if h.cfg.Autoupdate == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "autoupdate_unavailable"})
		return
	}
	cfg, err := h.cfg.Autoupdate.EnsureDefault(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "autoupdate_read"})
		return
	}
	c.JSON(http.StatusOK, cfg)
}

type autoupdatePutBody struct {
	AptEnabled bool `json:"apt_enabled"`
	// AptOptoutAcknowledged must be true to disable OS security auto-updates
	// (JAB-353): turning them off is an intentional, recorded decision, not a
	// silent insecure default.
	AptOptoutAcknowledged bool   `json:"apt_optout_acknowledged"`
	AptTime               string `json:"apt_time"`
	JabaliEnabled         bool   `json:"jabali_enabled"`
	JabaliTime            string `json:"jabali_time"`
}

// timeHHMM validates a 24h HH:MM string. Rejecting non-conforming input at the
// REST boundary keeps a malformed value from ever reaching the systemd
// OnCalendar render in the agent.
func timeHHMM(s string) bool {
	if len(s) != 5 || s[2] != ':' {
		return false
	}
	hh, err1 := strconv.Atoi(s[0:2])
	mm, err2 := strconv.Atoi(s[3:5])
	return err1 == nil && err2 == nil && hh >= 0 && hh <= 23 && mm >= 0 && mm <= 59
}

func (h *adminUpdatesHandler) autoupdatePut(c *gin.Context) {
	if h.cfg.Autoupdate == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "autoupdate_unavailable"})
		return
	}
	var body autoupdatePutBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if !timeHHMM(body.AptTime) || !timeHHMM(body.JabaliTime) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_time", "details": "apt_time/jabali_time must be HH:MM (24h)"})
		return
	}
	// JAB-353: disabling OS security auto-updates is an intentional, recorded
	// decision — reject a disable that doesn't carry the acknowledgement so the
	// UI must surface the risk + a confirm step. Re-enabling clears the opt-out.
	if !body.AptEnabled && !body.AptOptoutAcknowledged {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ack_required", "details": "disabling OS security auto-updates requires explicit acknowledgement (apt_optout_acknowledged)"})
		return
	}
	cfg := &models.UpdateAutoupdateConfig{
		AptEnabled:            body.AptEnabled,
		AptOptoutAcknowledged: !body.AptEnabled && body.AptOptoutAcknowledged,
		AptTime:               body.AptTime,
		JabaliEnabled:         body.JabaliEnabled,
		JabaliTime:            body.JabaliTime,
	}
	if err := h.cfg.Autoupdate.Upsert(c.Request.Context(), cfg); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "autoupdate_save"})
		return
	}
	// The autoupdate reconciler converges this onto the host on its next tick.
	saved, _ := h.cfg.Autoupdate.Get(c.Request.Context())
	c.JSON(http.StatusOK, saved)
}
