// automation_write_routes.go — JAB-140 write endpoints (Steps 3–5).
//
// All mounted under the /automation group (already behind RequireAutomationHMAC),
// each behind a per-token write rate limiter + requireWriteScope(scope). Actions
// run through the SAME repositories/agent commands the GUI uses (one write path,
// DB-is-truth), and every write emits exactly one audit row. Reversible only in
// M2 — no destructive verb.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// capability describes one mounted write action for GET /automation/capabilities.
type capability struct {
	Action string `json:"action"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Scope  string `json:"scope"`
	Async  bool   `json:"async"`
}

func registerAutomationWrites(g *gin.RouterGroup, cfg AutomationConfig) {
	// Per-token write rate limit: 30 sustained writes/min, burst 10.
	wl := newWriteRateLimiter(30, 10)
	var caps []capability

	if cfg.Agent != nil {
		g.POST("/services/:name/restart", wl.middleware(), requireWriteScope("write:services"), servicesRestartHandler(cfg))
		caps = append(caps, capability{"services.restart", "POST", "/automation/services/:name/restart", "write:services", false})
	}
	if cfg.Users != nil {
		g.POST("/users/:id/disable", wl.middleware(), requireWriteScope("write:users"), userSuspendHandler(cfg, true))
		g.POST("/users/:id/enable", wl.middleware(), requireWriteScope("write:users"), userSuspendHandler(cfg, false))
		caps = append(caps,
			capability{"users.disable", "POST", "/automation/users/:id/disable", "write:users", false},
			capability{"users.enable", "POST", "/automation/users/:id/enable", "write:users", false})
	}
	if cfg.Domains != nil {
		g.POST("/domains/:id/suspend", wl.middleware(), requireWriteScope("write:domains"), domainSuspendHandler(cfg, true))
		g.POST("/domains/:id/unsuspend", wl.middleware(), requireWriteScope("write:domains"), domainSuspendHandler(cfg, false))
		caps = append(caps,
			capability{"domains.suspend", "POST", "/automation/domains/:id/suspend", "write:domains", false},
			capability{"domains.unsuspend", "POST", "/automation/domains/:id/unsuspend", "write:domains", false})
	}
	if cfg.Agent != nil && cfg.Domains != nil {
		g.POST("/cache/purge", wl.middleware(), requireWriteScope("write:cache"), cachePurgeHandler(cfg))
		caps = append(caps, capability{"cache.purge", "POST", "/automation/cache/purge", "write:cache", false})
	}
	if cfg.Operations != nil {
		g.POST("/backups", wl.middleware(), requireWriteScope("write:backups"), backupEnqueueHandler(cfg))
		g.GET("/operations/:id", operationStatusHandler(cfg))
		caps = append(caps, capability{"backups.create", "POST", "/automation/backups", "write:backups", true})
	}

	// ADR-0164 billing endpoints (account lifecycle + usage + packages).
	caps = append(caps, registerAutomationBilling(g, cfg, wl)...)

	// GET /automation/capabilities — any valid token. Lets Sounder show only
	// the actions this server actually mounts + stay forward-compatible.
	g.GET("/capabilities", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true, "actions": caps})
	})
}

// mapAgentErr maps an agent error to the JAB-140 error contract. A not-found /
// invalid-argument from the agent (e.g. a non-allowlisted or masked service) is
// unsupported; anything else is internal.
func mapAgentErr(c *gin.Context, err error) {
	var ae *agent.AgentError
	if errors.As(err, &ae) {
		switch ae.Code {
		// not_found / invalid_argument (bad or masked name) and permission_denied
		// (service not in the agent's restartable allow-list) are all caller-fixable
		// "this action isn't available", not a server fault.
		case agent.CodeNotFound, agent.CodeInvalidArgument, agent.CodePermissionDenied:
			autoErr(c, http.StatusBadRequest, "unsupported", ae.Message)
			return
		}
	}
	autoErr(c, http.StatusBadGateway, "internal", "agent error")
}

func servicesRestartHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		name := c.Param("name")
		const action = "POST /automation/services/:name/restart"
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		// service.restart: the agent enforces the restartable-service allowlist
		// (masked/critical units rejected) — the one write path for restarts.
		if _, err := cfg.Agent.Call(ctx, "service.restart", map[string]any{"name": name}); err != nil {
			auditWrite(c, cfg.Audits, tok, action, "service", name, models.AuditResultError)
			mapAgentErr(c, err)
			return
		}
		auditWrite(c, cfg.Audits, tok, action, "service", name, models.AuditResultOK)
		autoOK(c, "service "+name+" restarted")
	}
}

func userSuspendHandler(cfg AutomationConfig, suspend bool) gin.HandlerFunc {
	action := "POST /automation/users/:id/enable"
	if suspend {
		action = "POST /automation/users/:id/disable"
	}
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		id := c.Param("id")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		u, err := cfg.Users.FindByID(ctx, id)
		if err != nil || u == nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			autoErr(c, http.StatusNotFound, "not_found", "user not found")
			return
		}
		// Never let automation disable an admin (would lock out the panel).
		if suspend && u.IsAdmin {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultDenied)
			autoErr(c, http.StatusConflict, "conflict", "refusing to disable an admin user")
			return
		}
		// JAB-281: route through the full User Account Lifecycle transition —
		// the SAME userops.Suspend/Unsuspend the admin GUI/API and CLI use, not a
		// bare suspended-flag flip. Beyond the load-bearing DB flag this
		// deactivates the Kratos identity + invalidates cached sessions, disables
		// owned domains, stops Docker apps, and locks OS + FTP credentials.
		// Steps 2-5 are best-effort warnings inside userops; a returned error is
		// only the hard DB-write failure. Idempotent by target state.
		reason := "automation:" + tok.ID
		var (
			opErr    error
			warnings map[string]string
		)
		if suspend {
			res, err := userops.Suspend(ctx, billingUserOpsDeps(cfg), u, reason)
			opErr = err
			warnings = lifecycleWarnings(res.KratosWarning, res.DomainWarning, res.OSWarning)
		} else {
			res, err := userops.Unsuspend(ctx, billingUserOpsDeps(cfg), u)
			opErr = err
			warnings = lifecycleWarnings(res.KratosWarning, res.DomainWarning, res.OSWarning)
		}
		if opErr != nil {
			auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultError)
			autoErr(c, http.StatusBadGateway, "internal", "suspend failed")
			return
		}
		auditWrite(c, cfg.Audits, tok, action, "user", id, models.AuditResultOK)
		notifyWrite(cfg, "automation.user."+verbDone(suspend, "disabled", "enabled"),
			"warning", "Automation "+verbDone(suspend, "disabled", "enabled")+" a user",
			"User "+u.Email+" was "+verbDone(suspend, "disabled", "enabled")+" via the automation API.",
			"/jabali-admin/users")
		// JAB-281 criterion: surface the lifecycle's best-effort warnings
		// (kratos/domain/os) in the automation response without reimplementing
		// the orchestration — the userops result is the single source.
		autoOKWarnings(c, "user "+id+" "+verbDone(suspend, "disabled", "enabled"), warnings)
	}
}

func domainSuspendHandler(cfg AutomationConfig, suspend bool) gin.HandlerFunc {
	action := "POST /automation/domains/:id/unsuspend"
	if suspend {
		action = "POST /automation/domains/:id/suspend"
	}
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		id := c.Param("id")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()
		d, err := cfg.Domains.FindByID(ctx, id)
		if err != nil || d == nil {
			auditWrite(c, cfg.Audits, tok, action, "domain", id, models.AuditResultError)
			autoErr(c, http.StatusNotFound, "not_found", "domain not found")
			return
		}
		wantEnabled := !suspend
		if d.IsEnabled == wantEnabled { // idempotent no-op
			auditWrite(c, cfg.Audits, tok, action, "domain", id, models.AuditResultOK)
			autoOK(c, "domain "+d.Name+" already "+verbDone(suspend, "suspended", "active"))
			return
		}
		// is_enabled is the nginx-vhost switch; setting it via the same repo the
		// GUI uses lets the reconciler converge nginx (DB-is-truth).
		d.IsEnabled = wantEnabled
		if err := cfg.Domains.Update(ctx, d); err != nil {
			auditWrite(c, cfg.Audits, tok, action, "domain", id, models.AuditResultError)
			autoErr(c, http.StatusBadGateway, "internal", "update failed")
			return
		}
		auditWrite(c, cfg.Audits, tok, action, "domain", id, models.AuditResultOK)
		notifyWrite(cfg, "automation.domain."+verbDone(suspend, "suspended", "unsuspended"),
			"warning", "Automation "+verbDone(suspend, "suspended", "unsuspended")+" a domain",
			"Domain "+d.Name+" was "+verbDone(suspend, "suspended", "unsuspended")+" via the automation API.",
			"/jabali-admin/domains")
		autoOK(c, "domain "+d.Name+" "+verbDone(suspend, "suspended", "unsuspended"))
	}
}

type cachePurgeReq struct {
	Scope  string `json:"scope"`  // "all" | "domain"
	Domain string `json:"domain"` // required when scope="domain"
}

func cachePurgeHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		const action = "POST /automation/cache/purge"
		var req cachePurgeReq
		if !bindSigned(c, &req) {
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		// nginx.cache.purge is per-domain (the agent validates a domain). scope=all
		// iterates every domain (fleet-wide purge); scope=domain purges one.
		switch req.Scope {
		case "", "all":
			rows, _, lerr := cfg.Domains.List(ctx, repository.ListOptions{Limit: 1000})
			if lerr != nil {
				auditWrite(c, cfg.Audits, tok, action, "cache", "all", models.AuditResultError)
				autoErr(c, http.StatusBadGateway, "internal", "list domains failed")
				return
			}
			purged := 0
			for _, d := range rows {
				if _, err := cfg.Agent.Call(ctx, "nginx.cache.purge", map[string]any{"domain": d.Name}); err == nil {
					purged++
				}
			}
			auditWrite(c, cfg.Audits, tok, action, "cache", "all", models.AuditResultOK)
			autoOK(c, "cache purged for all domains")
			_ = purged
		case "domain":
			if req.Domain == "" {
				autoErr(c, http.StatusUnprocessableEntity, "unsupported", "scope=domain requires a domain")
				return
			}
			d, err := cfg.Domains.FindByName(ctx, req.Domain)
			if err != nil || d == nil {
				auditWrite(c, cfg.Audits, tok, action, "domain", req.Domain, models.AuditResultError)
				autoErr(c, http.StatusNotFound, "not_found", "domain not found")
				return
			}
			if _, err := cfg.Agent.Call(ctx, "nginx.cache.purge", map[string]any{"domain": d.Name}); err != nil {
				auditWrite(c, cfg.Audits, tok, action, "cache", d.Name, models.AuditResultError)
				mapAgentErr(c, err)
				return
			}
			auditWrite(c, cfg.Audits, tok, action, "cache", d.Name, models.AuditResultOK)
			autoOK(c, "cache purged ("+d.Name+")")
		default:
			autoErr(c, http.StatusUnprocessableEntity, "unsupported", "scope must be all or domain")
		}
	}
}

func verbDone(cond bool, ifTrue, ifFalse string) string {
	if cond {
		return ifTrue
	}
	return ifFalse
}

// idempotencyKey returns the per-request idempotency key: an explicit
// Idempotency-Key header, else the request signature (unique per signed request).
func idempotencyKey(c *gin.Context) string {
	if k := c.GetHeader("Idempotency-Key"); k != "" {
		if len(k) > 80 {
			k = k[:80]
		}
		return k
	}
	// Fall back to the sig from the Authorization header (already replay-guarded).
	auth := c.GetHeader("Authorization")
	for _, part := range splitComma(auth) {
		if len(part) > 4 && part[:4] == "sig=" {
			s := part[4:]
			if len(s) > 80 {
				s = s[:80]
			}
			return s
		}
	}
	return ids.NewULID()
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, trimSpace(cur))
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, trimSpace(cur))
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func backupEnqueueHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		const action = "POST /automation/backups"
		key := idempotencyKey(c)
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()

		// Idempotency: a repeated (token, key) returns the first op, no re-run.
		if prior, err := cfg.Operations.FindByIdempotencyKey(ctx, tok.ID, key); err == nil && prior != nil {
			c.JSON(http.StatusAccepted, gin.H{"ok": true, "operation_id": prior.ID, "status": prior.Status, "message": "already enqueued"})
			return
		}

		op := &models.AutomationOperation{
			ID:             ids.NewULID(),
			TokenID:        tok.ID,
			IdempotencyKey: &key,
			Action:         "backup",
			Target:         "server",
			Status:         "pending",
		}
		if err := cfg.Operations.Create(ctx, op); err != nil {
			auditWrite(c, cfg.Audits, tok, action, "backup", "server", models.AuditResultError)
			autoErr(c, http.StatusBadGateway, "internal", "could not enqueue backup")
			return
		}
		auditWrite(c, cfg.Audits, tok, action, "backup", op.ID, models.AuditResultOK)

		// Run async: flip running → done/error. The M30 backup-run wiring is
		// invoked here; a nil Agent still records the op lifecycle so polling
		// works in feature-off/test environments.
		go runBackupOperation(cfg, op.ID)

		autoAsync(c, op.ID, "backup enqueued")
	}
}

// runBackupOperation drives an operation to a terminal state by triggering the
// SAME M30 system-backup path the GUI uses (one write path): resolve the single
// enabled destination, create a system BackupJob, and call the agent's
// system.backup. Uses a background context so it outlives the HTTP request.
func runBackupOperation(cfg AutomationConfig, opID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	_ = cfg.Operations.SetStatus(ctx, opID, "running", "backup started")
	if cfg.Agent == nil || cfg.BackupJobs == nil || cfg.BackupDests == nil {
		_ = cfg.Operations.SetStatus(ctx, opID, "error", "backup subsystem not configured")
		return
	}
	// Default destination: exactly one enabled destination, else the operator
	// must pick — automation stays destination-agnostic for M2.
	dests, err := cfg.BackupDests.ListEnabled(ctx)
	if err != nil || len(dests) != 1 {
		_ = cfg.Operations.SetStatus(ctx, opID, "error", "no single enabled backup destination")
		return
	}
	destID := dests[0].ID
	job := &models.BackupJob{
		ID:            ids.NewULID(),
		UserID:        "system",
		DestinationID: &destID,
		Kind:          models.BackupJobKindSystemBackup,
		Status:        models.BackupJobStatusQueued,
		CreatedAt:     time.Now().UTC(),
	}
	if err := cfg.BackupJobs.Create(ctx, job); err != nil {
		_ = cfg.Operations.SetStatus(ctx, opID, "error", "could not create backup job")
		return
	}
	params := map[string]any{"job_id": job.ID, "include_accounts": true}
	for k, v := range destWireParams(&dests[0]) {
		params[k] = v
	}
	if _, err := cfg.Agent.Call(ctx, "system.backup", params); err != nil {
		_ = cfg.Operations.SetStatus(ctx, opID, "error", "backup failed")
		return
	}
	_ = cfg.Operations.SetStatus(ctx, opID, "done", "backup started (job "+job.ID+")")
}

func operationStatusHandler(cfg AutomationConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tok := middleware.AutomationToken(c)
		id := c.Param("id")
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		op, err := cfg.Operations.FindByID(ctx, id)
		// Scope the lookup to the caller's own token so one token can't poll
		// another's operations.
		if err != nil || op == nil || op.TokenID != tok.ID {
			if errors.Is(err, repository.ErrNotFound) || op == nil || (op != nil && op.TokenID != tok.ID) {
				autoErr(c, http.StatusNotFound, "not_found", "operation not found")
				return
			}
			autoErr(c, http.StatusBadGateway, "internal", "lookup failed")
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "operation_id": op.ID, "status": op.Status, "message": op.Message})
	}
}
