// Public Automation API (M44).
//
// Mounted at /api/v1/automation/* behind the HMAC middleware. Each
// route declares its required scope; AutomationScopes.Has matches
// either an exact "read:domains" or a wildcard "read:*".
//
// Response shapes are intentionally THINNER than the regular
// /api/v1 routes: external callers shouldn't accidentally cache
// listen-IP topology, doc-roots, or per-user infra fields. If a
// downstream automation needs a richer view, mint a Kratos session
// instead.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sync/errgroup"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// AutomationConfig wires read repos for the public automation API.
// Required: AutomationTokens + Key (for the HMAC middleware).
// Optional: per-resource repos — when nil the matching route returns
// 503 instead of 404 so the caller can distinguish "feature off" from
// "no rows".
type AutomationConfig struct {
	AutomationTokens repository.AutomationTokenRepository
	Key              *ssokey.Key
	// Redis is the M44 replay-defense store. Required in production
	// (the M14 dispatcher already needs Redis on every install, so
	// this is a no-cost share). Nil disables the SETNX gate — only
	// for tests / non-prod.
	Redis        *redis.Client
	Domains      repository.DomainRepository
	Users        repository.UserRepository
	Applications repository.ApplicationInstallRepository
	// Agent powers the read:status metrics endpoint (JAB-75) — reuses the
	// M31 system.* collectors so a fleet monitor gets the same data the admin
	// Server Status page shows. Nil → /status stays the bare healthy stub.
	Agent agent.AgentInterface
	// Mail-stack read repos power the read:mail endpoints (JAB-77, extended by
	// JAB-76). Nil → those routes aren't mounted.
	Mailboxes      repository.MailboxRepository
	MailGroups     repository.MailGroupRepository
	Forwarders     repository.EmailForwarderRepository
	Autoresponders repository.EmailAutoresponderRepository
	// JAB-140 write layer. Operations powers async backups + the idempotency
	// ledger; Audits records every write (M49). Backups drives POST /backups.
	// All optional — a nil repo/agent drops the matching write route (mirrors
	// the read routes' feature-off behaviour).
	Operations repository.AutomationOperationRepository
	Audits     repository.AuditEventRepository
	// Backup wiring (JAB-140): reuse the M30 system-backup path so automation
	// goes through the same job model + destination the GUI uses. When either is
	// nil the async backup still enqueues but resolves to an error op (no dest).
	BackupJobs  repository.BackupJobRepository
	BackupDests repository.BackupDestinationRepository
	// Notify (JAB-140) publishes M14 notifications for high-impact writes
	// (suspend/disable). Nil → notifications skipped (audit still records).
	Notify AutomationNotifier

	// ADR-0164 billing endpoints (JAB-190). All optional — nil drops the
	// matching route, and GET /capabilities only advertises what mounted.
	Packages      repository.PackageRepository
	Databases     repository.DatabaseRepository
	DatabaseUsers repository.DatabaseUserRepository
	// FtpAccounts is reaped on the automation delete-cascade (JAB-265).
	FtpAccounts  repository.FtpAccountRepository
	DockerApps   repository.DockerAppRepository
	KratosClient *kratosclient.Client
	BcryptCost   int
	// Narrow reconciler slices (typed-nil-safe: wire via the app's
	// nil-guard, never a bare *reconciler.Reconciler that might be nil).
	LimitsReconciler userops.LimitsReconciler
	// DomainTeardowns persists the JAB-236 tombstones for durable
	// domain teardown on the billing delete cascade.
	DomainTeardowns repository.DomainTeardownRepository
	// PortAllocations frees a domain's reverse-proxy loopback port (GH #1175)
	// during the billing-cancel delete cascade. Optional.
	PortAllocations repository.PortAllocationRepository
	// DomainCreate carries the same deps the GUI domain handler uses, so
	// JAB-233 account-create-with-domain runs the exact createDomainOp
	// orchestration. Nil-safe: when unset (or its repos nil), a create that
	// carries `domain` degrades to a domain_warning rather than panicking.
	DomainCreate DomainHandlerConfig
	// Usage reads (bulk-safe: snapshots + one batched bw_daily query —
	// the automation usage endpoints NEVER call the agent).
	DiskSnapshots repository.DiskUsageSnapshotRepository
	BWDaily       repository.BWDailyRepository
	Log           *slog.Logger
}

func RegisterAutomation(rg *gin.RouterGroup, cfg AutomationConfig) {
	if cfg.AutomationTokens == nil || cfg.Key == nil {
		return
	}
	// Same default the REST user routes apply (ADR-0164 billing writes
	// hash passwords through userops).
	if cfg.BcryptCost == 0 {
		cfg.BcryptCost = bcrypt.DefaultCost
	}
	// LO-4 (security review 2026-08-08): with a nil Redis the SETNX replay
	// gate is silently absent — a captured signed request replays freely
	// inside the skew window. Now that destructive verbs mount here
	// (ADR-0164 delete:users), make the downgraded mode loudly visible.
	// Production always wires Redis (M14); this fires only on dev/test
	// topologies.
	if cfg.Redis == nil {
		log := cfg.Log
		if log == nil {
			log = slog.Default()
		}
		log.Warn("automation API mounted WITHOUT the Redis replay gate — " +
			"signed requests are replayable within the clock-skew window; " +
			"never run this topology in production")
	}
	g := rg.Group("/automation",
		middleware.RequireAutomationHMAC(cfg.AutomationTokens, cfg.Key, cfg.Redis),
	)

	if cfg.Domains != nil {
		g.GET("/domains", middleware.RequireScope("read:domains"), func(c *gin.Context) {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
			defer cancel()
			rows, _, err := cfg.Domains.List(ctx, repository.ListOptions{
				Limit: 200,
			})
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
			out := make([]map[string]any, 0, len(rows))
			for _, d := range rows {
				out = append(out, map[string]any{
					"id":         d.ID,
					"name":       d.Name,
					"user_id":    d.UserID,
					"is_enabled": d.IsEnabled,
				})
			}
			c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
		})
	}

	if cfg.Users != nil {
		g.GET("/users", middleware.RequireScope("read:users"), func(c *gin.Context) {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
			defer cancel()
			// ADR-0164: exact-match lookup filters so billing panels can
			// resolve one account without paging the full list. A filtered
			// miss is data ({data:[], total:0}), not an error.
			if email := c.Query("email"); email != "" {
				u, err := cfg.Users.FindByEmail(ctx, email)
				automationUserLookupResponse(c, u, err)
				return
			}
			if username := c.Query("username"); username != "" {
				u, err := cfg.Users.FindByUsername(ctx, username)
				automationUserLookupResponse(c, u, err)
				return
			}
			rows, _, err := cfg.Users.List(ctx, repository.ListOptions{
				Limit: 200,
			})
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
			out := make([]map[string]any, 0, len(rows))
			for _, u := range rows {
				out = append(out, automationUserRow(&u))
			}
			c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
		})
	}

	if cfg.Applications != nil {
		g.GET("/applications", middleware.RequireScope("read:applications"), func(c *gin.Context) {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
			defer cancel()
			rows, _, err := cfg.Applications.List(ctx, repository.ListOptions{
				Limit: 200,
			})
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
				return
			}
			out := make([]map[string]any, 0, len(rows))
			for _, a := range rows {
				out = append(out, map[string]any{
					"id":        a.ID,
					"app_type":  a.AppType,
					"domain_id": a.DomainID,
					"status":    a.Status,
				})
			}
			c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
		})
	}

	// read:status — fleet-monitor metrics (JAB-75). Reuses the M31 agent
	// collectors (system.info = host/load/mem/swap/disk, service_details =
	// unit health). Cached a few seconds so frequent polling doesn't hammer
	// the agent. Falls back to the bare healthy stub when no Agent is wired.
	metrics := newAutomationMetrics(cfg.Agent)
	g.GET("/status", middleware.RequireScope("read:status"), metrics.handle)

	// read:mail — mail-stack inventory for a fleet manager (JAB-77 + JAB-76).
	registerAutomationMailReads(g, cfg)

	// JAB-140 write layer + capabilities (additive; each route self-guards on
	// its repo/agent being present, mirroring the read routes).
	registerAutomationWrites(g, cfg)
}

// registerAutomationMailReads mounts the read:mail inventory endpoints a fleet
// manager (GH #308) reads to populate its Mail tab. Thin + secrets-free: never
// password hashes, SSO tokens, or message bodies — reuses the same server-wide
// repos the admin pages use so there is one source of truth. Split out as its
// own function (like registerAutomationWrites) so the handlers are unit
// testable behind the scope gate. JAB-77 shipped mailboxes/domains/summary;
// JAB-76 adds forwarders, domain-forwarders, groups, autoresponders.
func registerAutomationMailReads(g *gin.RouterGroup, cfg AutomationConfig) {
	if cfg.Mailboxes == nil {
		return
	}
	mg := g.Group("/mail", middleware.RequireScope("read:mail"))

	// domainLookup builds an id → {name, owner} map once per request so the
	// forwarder/group rows can carry a human domain + owner without a per-row
	// query. Best-effort: a missing Domains repo just yields blank enrichment.
	domainLookup := func(ctx context.Context) map[string]struct{ Name, Owner string } {
		m := map[string]struct{ Name, Owner string }{}
		if cfg.Domains == nil {
			return m
		}
		doms, _, err := cfg.Domains.List(ctx, repository.ListOptions{Limit: 5000})
		if err != nil {
			return m
		}
		for _, d := range doms {
			m[d.ID] = struct{ Name, Owner string }{Name: d.Name, Owner: d.UserID}
		}
		return m
	}

	mg.GET("/mailboxes", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		rows, err := cfg.Mailboxes.ListAllWithDomain(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, m := range rows {
			out = append(out, map[string]any{
				"email":            m.EmailCached,
				"domain":           m.DomainName,
				"owner":            m.UserUsername,
				"quota_bytes":      m.QuotaBytes,
				"last_usage_bytes": m.LastUsageBytes,
				"disabled":         m.IsDisabled,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
	})

	mg.GET("/domains", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		rows, _, err := cfg.Domains.List(ctx, repository.ListOptions{Limit: 500})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		out := make([]map[string]any, 0)
		for _, d := range rows {
			if !d.EmailEnabled {
				continue
			}
			out = append(out, map[string]any{"id": d.ID, "name": d.Name, "user_id": d.UserID})
		}
		c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
	})

	mg.GET("/summary", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		// COUNT, not a full-table read. This endpoint is polled by fleet
		// monitors; loading every mailbox row (two JOINs, every column
		// including password material) just to len() it was the single
		// most expensive thing here.
		mailboxCount := 0
		if n, err := cfg.Mailboxes.CountAll(ctx); err == nil {
			mailboxCount = int(n)
		}
		mailDomains := 0
		if cfg.Domains != nil {
			if doms, _, err := cfg.Domains.List(ctx, repository.ListOptions{Limit: 500}); err == nil {
				for _, d := range doms {
					if d.EmailEnabled {
						mailDomains++
					}
				}
			}
		}
		groups := 0
		if cfg.MailGroups != nil {
			if gr, err := cfg.MailGroups.ListAllWithDomain(ctx); err == nil {
				groups = len(gr)
			}
		}
		forwarders := 0
		if cfg.Forwarders != nil {
			if _, n, err := cfg.Forwarders.ListAll(ctx, repository.ListOptions{Limit: 1}); err == nil {
				forwarders = int(n)
			}
		}
		c.JSON(http.StatusOK, gin.H{
			"mail_domains": mailDomains,
			"mailboxes":    mailboxCount,
			"forwarders":   forwarders,
			"mail_groups":  groups,
		})
	})

	// JAB-76: mailbox-level forwarders (MailboxID set). Domain-scoped rows
	// (MailboxID nil) are served by /domain-forwarders below, so the two views
	// stay distinct — matching the panel's own split.
	mg.GET("/forwarders", func(c *gin.Context) {
		listForwarders(c, cfg, domainLookup, false)
	})

	// JAB-76: domain-scoped forwarders (MailboxID nil — catch-alls / domain
	// aliases, not tied to a single mailbox).
	mg.GET("/domain-forwarders", func(c *gin.Context) {
		listForwarders(c, cfg, domainLookup, true)
	})

	// JAB-76: mail groups with member counts + per-service flags.
	mg.GET("/groups", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		if cfg.MailGroups == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "mail groups unavailable"})
			return
		}
		rows, err := cfg.MailGroups.ListAllWithDomain(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, gr := range rows {
			out = append(out, map[string]any{
				"id":              gr.ID,
				"email":           gr.EmailCached,
				"domain":          gr.DomainName,
				"owner":           gr.UserUsername,
				"display_name":    gr.DisplayName,
				"group_kind":      gr.GroupKind,
				"member_count":    gr.MemberCount,
				"internal_only":   gr.InternalOnly,
				"has_mailbox":     gr.HasMailbox,
				"has_calendar":    gr.HasCalendar,
				"has_addressbook": gr.HasAddressbook,
				"has_files":       gr.HasFiles,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
	})

	// JAB-76: autoresponder status + schedule metadata. Deliberately
	// metadata-only — the subject line, text, and HTML bodies are tenant
	// content and are never exposed to an automation token.
	mg.GET("/autoresponders", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		if cfg.Autoresponders == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "autoresponders unavailable"})
			return
		}
		rows, err := cfg.Autoresponders.ListAll(ctx)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		// mailbox_id → email, so the manager sees an address, not a ULID.
		emailByMailbox := map[string]string{}
		if mbs, err := cfg.Mailboxes.ListAllWithDomain(ctx); err == nil {
			for _, m := range mbs {
				emailByMailbox[m.ID] = m.EmailCached
			}
		}
		out := make([]map[string]any, 0, len(rows))
		for _, ar := range rows {
			out = append(out, map[string]any{
				"mailbox_id": ar.MailboxID,
				"email":      emailByMailbox[ar.MailboxID],
				"enabled":    ar.Enabled,
				"from_date":  ar.FromDate,
				"to_date":    ar.ToDate,
				"updated_at": ar.UpdatedAt,
			})
		}
		c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
	})
}

// listForwarders serves both the mailbox-level (/forwarders) and domain-scoped
// (/domain-forwarders) views: they read the same table and differ only by
// whether MailboxID is set. domainScoped=true selects the nil-MailboxID rows.
func listForwarders(c *gin.Context, cfg AutomationConfig, domainLookup func(context.Context) map[string]struct{ Name, Owner string }, domainScoped bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if cfg.Forwarders == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "forwarders unavailable"})
		return
	}
	rows, _, err := cfg.Forwarders.ListAll(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	doms := domainLookup(ctx)
	out := make([]map[string]any, 0, len(rows))
	for _, f := range rows {
		isDomainScoped := f.MailboxID == nil
		if isDomainScoped != domainScoped {
			continue
		}
		d := doms[f.DomainID]
		row := map[string]any{
			"id":            f.ID,
			"domain":        d.Name,
			"owner_user_id": d.Owner,
			"type":          f.Type,
			"local_part":    f.LocalPart,
			"target":        f.Target,
			"enabled":       f.Enabled,
			"keep_copy":     f.KeepCopy,
		}
		if !domainScoped {
			row["mailbox_id"] = f.MailboxID
		}
		out = append(out, row)
	}
	c.JSON(http.StatusOK, gin.H{"data": out, "total": len(out)})
}

// automationMetrics caches the aggregated server metrics for a short TTL so a
// fleet monitor polling every few seconds doesn't issue a fresh agent fan-out
// per request. Concurrency-safe.
type automationMetrics struct {
	agent    agent.AgentInterface
	mu       sync.Mutex
	cached   gin.H
	cachedAt time.Time
	ttl      time.Duration
}

func newAutomationMetrics(a agent.AgentInterface) *automationMetrics {
	return &automationMetrics{agent: a, ttl: 5 * time.Second}
}

func (m *automationMetrics) handle(c *gin.Context) {
	// No agent wired → keep the original healthy stub (never fail closed).
	if m.agent == nil {
		c.JSON(http.StatusOK, gin.H{"healthy": true, "time": time.Now().UTC()})
		return
	}

	m.mu.Lock()
	if m.cached != nil && time.Since(m.cachedAt) < m.ttl {
		out := m.cached
		m.mu.Unlock()
		c.JSON(http.StatusOK, out)
		return
	}
	m.mu.Unlock()

	ctx := c.Request.Context()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)
	var (
		mu       sync.Mutex
		info     json.RawMessage
		services json.RawMessage
		cpu      json.RawMessage
		netRaw   json.RawMessage
		errs     = map[string]string{}
	)
	fetch := func(name, cmd string, dst *json.RawMessage) {
		g.Go(func() error {
			sub, cancel := context.WithTimeout(gctx, 8*time.Second)
			defer cancel()
			raw, err := m.agent.Call(sub, cmd, nil)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs[name] = err.Error()
				return nil
			}
			*dst = raw
			return nil
		})
	}
	fetch("info", "system.info", &info)
	fetch("services", "system.service_details", &services)
	fetch("cpu", "system.cpu_usage", &cpu)
	// JAB-150: WAN throughput + packet loss for the Sounder Monitor. The
	// collector samples over a short window, so this leg is the slow one;
	// the 8s per-fetch timeout above bounds it.
	fetch("net", "system.net_telemetry", &netRaw)
	_ = g.Wait()

	out := gin.H{
		"healthy": len(errs) == 0,
		"time":    time.Now().UTC(),
		"version": Version,
		// JAB-141: Sounder needs an ORDERABLE release identifier, not just the
		// short SHA (which can't be compared/sorted). build_time is the RFC3339
		// link time — lexically sortable, so the monitor can tell which managed
		// panel is running a newer build. commit is the full SHA for provenance.
		"commit":     Commit,
		"build_time": BuildTime,
	}
	if info != nil {
		out["system"] = info
	}
	if services != nil {
		out["services"] = services
		// Normalized per-service health for the Sounder Monitor (JAB-150).
		// Additive: existing consumers keep reading the raw "services".
		if health := normalizeServiceHealth(services); len(health) > 0 {
			out["service_health"] = health
		}
	}
	if cpu != nil {
		out["cpu"] = cpu
	}
	if netRaw != nil {
		out["net"] = netRaw
	}
	if len(errs) > 0 {
		out["errors"] = errs
	}

	m.mu.Lock()
	m.cached, m.cachedAt = out, time.Now()
	m.mu.Unlock()
	c.JSON(http.StatusOK, out)
}

// agentServiceDetail is the subset of the agent's system.service_details
// ServiceDetail that the normalized health view needs. Declared locally so
// panel-api doesn't import the agent's command package.
type agentServiceDetail struct {
	Unit          string `json:"unit"`
	Active        string `json:"active"`
	Sub           string `json:"sub"`
	LoadState     string `json:"load_state"`
	UnitFileState string `json:"unit_file_state"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

// automationServiceHealth is the normalized, monitor-friendly health row
// the Sounder Monitor consumes (JAB-150). Status is one of healthy |
// degraded | failed | stopped; reason carries the systemd sub-state for
// context (e.g. "running", "dead", "failed").
type automationServiceHealth struct {
	Name          string `json:"name"`
	Unit          string `json:"unit"`
	Status        string `json:"status"`
	Reason        string `json:"reason,omitempty"`
	UptimeSeconds int64  `json:"uptime_seconds,omitempty"`
	LastChecked   string `json:"last_checked"`
}

// automationLogicalServices maps a stable logical service name (what the
// monitor keys on, forward-compatible across host layout changes) to the
// systemd unit that backs it. Only units the agent actually reports get a
// row — the list is intentionally a superset. Units with no clean single
// systemd unit on jabali hosts (php-fpm runs as per-user jabali-fpm@<user>
// slices per ADR-0025; backups run via agent backup.run + per-user timers,
// not a daemon; crowdsec is not in the agent probe allowlist) are omitted
// rather than reported as perpetually "stopped".
var automationLogicalServices = []struct{ Name, Unit string }{
	{"web", "nginx.service"},
	{"database", "mariadb.service"},
	{"cache", "redis-server.service"},
	{"mail", "jabali-stalwart.service"},
	{"webmail", "jabali-webmail.service"},
	{"identity", "jabali-kratos.service"},
	{"dns", "pdns.service"},
	{"docker", "docker.service"},
	{"panel", "jabali-panel.service"},
	{"agent", "jabali-agent.service"},
	{"ssh", "ssh.service"},
}

// normalizeServiceHealth folds the raw system.service_details payload into
// the normalized health view. Capability-aware: a logical service whose
// unit is absent, not-found, or masked on this host is omitted so the
// monitor only ever sees actions/services that exist here. Returns nil on
// a malformed payload (the raw "services" field is still emitted).
func normalizeServiceHealth(raw json.RawMessage) []automationServiceHealth {
	var payload struct {
		Services []agentServiceDetail `json:"services"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	byUnit := make(map[string]agentServiceDetail, len(payload.Services))
	for _, d := range payload.Services {
		byUnit[d.Unit] = d
	}
	now := time.Now().UTC().Format(time.RFC3339)
	out := make([]automationServiceHealth, 0, len(automationLogicalServices))
	for _, ls := range automationLogicalServices {
		d, ok := byUnit[ls.Unit]
		if !ok {
			continue // agent didn't report it → not installed here
		}
		if d.LoadState == "not-found" || d.LoadState == "masked" {
			continue // capability-aware omit
		}
		out = append(out, automationServiceHealth{
			Name:          ls.Name,
			Unit:          ls.Unit,
			Status:        serviceHealthStatus(d.Active),
			Reason:        d.Sub,
			UptimeSeconds: d.UptimeSeconds,
			LastChecked:   now,
		})
	}
	return out
}

// serviceHealthStatus maps a systemd ActiveState to the normalized status
// vocabulary. activating/deactivating are transient → degraded so a
// mid-restart blip doesn't page the monitor as a hard failure.
func serviceHealthStatus(active string) string {
	switch active {
	case "active", "reloading":
		return "healthy"
	case "failed":
		return "failed"
	case "activating", "deactivating":
		return "degraded"
	default: // inactive, dead, unknown
		return "stopped"
	}
}
