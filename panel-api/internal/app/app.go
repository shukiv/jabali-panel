// Package app wires HTTP routes, middleware and lifecycle together.
// Downstream packages (handlers, middleware, repositories) plug in here.
package app

import (
	"log/slog"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/apps"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/eventsources"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/pyframeworks"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/reconciler"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sso"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailsso"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webui"
	panelui "git.jabali-panel.com/shukivaknin/jabali2/panel-ui"
)

// Deps bundles the collaborators NewWithDeps needs so main.go keeps its
// argument list short. Anything a handler family needs — Kratos client,
// repositories — plugs in here.
type Deps struct {
	KratosClient        *kratosclient.Client
	Users               repository.UserRepository
	Packages            repository.PackageRepository
	Domains             repository.DomainRepository
	Databases           repository.DatabaseRepository
	DatabaseUsers       repository.DatabaseUserRepository
	DatabaseUserGrants  repository.DatabaseUserGrantRepository
	DBAdmin             repository.DBAdminRepository
	Mailboxes           repository.MailboxRepository
	MailGroups          repository.MailGroupRepository
	MailboxSSOTokens    repository.MailboxSSOTokenRepository
	PhpMyAdminSSOTokens repository.PhpMyAdminSSOTokenRepository
	// M37 Phase 4: Adminer SSO bridge for both engines.
	AdminerSSOTokens          repository.AdminerSSOTokenRepository
	AdminerSSO                *sso.AdminerService
	LogAccessStreams          repository.LogAccessStreamRepository
	TerminalSessions          repository.TerminalSessionRepository
	Agent                     agent.AgentInterface
	Reconciler                *reconciler.Reconciler
	ServerSettings            repository.ServerSettingsRepository
	PageTemplates             repository.PageTemplateRepository
	AccountSkeleton           repository.AccountSkeletonRepository
	NotificationEventSettings repository.NotificationEventSettingRepository
	DNSZones                  repository.DNSZoneRepository
	DNSRecords                repository.DNSRecordRepository
	SSLCerts                  repository.SSLCertificateRepository
	SharedCerts               repository.SharedCertificateRepository
	// MailRBLStates (M47 Wave 5) backs the curated-RBL eventsource
	// that probes the server's outbound IPv4 against a free RBL
	// baseline and fires mail.rbl.{listed,cleared} on transitions.
	MailRBLStates repository.MailRBLStateRepository
	// M47 Wave 4/6/8 ingest sources.
	StalwartAdmin   eventsources.StalwartQueryClient
	DMARCAggregate  repository.DMARCAggregateRepository
	TLSRPTAggregate repository.TLSRPTAggregateRepository
	ARFReports      repository.ARFReportRepository
	// M47 Wave 3 outbound throttle config.
	MailOutboundPolicies  repository.MailOutboundPolicyRepository
	StalwartAdminThrottle api.ThrottleDispatcher
	BWDaily               repository.BWDailyRepository
	DomainIPACLs          repository.DomainIPACLRepository
	// M6.6 — per-domain mail TLS rows.
	MailCerts              repository.MailCertificateRepository
	DomainDirectoryPrivacy repository.DomainDirectoryPrivacyRepository
	MigrationJobs          repository.MigrationJobRepository
	MigrationSizeCache     repository.MigrationAccountSizeCacheRepository
	AutomationTokens       repository.AutomationTokenRepository
	// UserAPITokens — M51 per-user Bearer token persistence. Used
	// by the auth middleware (token validation) AND the user-
	// facing /me/api-tokens management endpoints.
	UserAPITokens       repository.UserAPITokenRepository
	PHPPools            repository.PHPPoolRepository
	PHPPoolIniOverrides repository.PHPPoolIniOverrideRepository
	PHPPerformanceModes repository.PHPPerformanceModeRepository
	WordPressInstalls   repository.WordPressInstallRepository
	// ManagedIPs is the M24 IP-pool repo. NewWithDeps registers
	// /admin/ips + /user/ips + /internal/agent/managed-ips when set;
	// nil keeps the routes off (lets existing test harnesses pass).
	ManagedIPs repository.ManagedIPRepository
	// Apps is the M19 application registry — descriptors for every
	// installable app (WordPress, future DokuWiki, etc.). Step 3 will
	// hand this to the generic /applications handlers. NewWithDeps
	// builds a default-populated registry when this is nil so the
	// existing test wiring (`Deps{}`) keeps working without each
	// caller knowing about the registry.
	Apps           *apps.Registry
	CronJobs       repository.CronJobRepository
	DockerApps     repository.DockerAppRepository
	PythonApps     repository.PythonAppRepository
	DockerCatalog  *dockerapp.Catalog
	PyFrameworks   *pyframeworks.Catalog
	SSHKeys        repository.SSHKeyRepository
	LimitOverrides repository.UserLimitOverrideRepository
	// M6.5 email feature repositories. Autoresponders/Forwarders/MailboxShares
	// reconcile jabali intent → Stalwart via the phase registry (ADR-0051).
	Autoresponders repository.EmailAutoresponderRepository
	Forwarders     repository.EmailForwarderRepository
	MailboxShares  repository.MailboxShareRepository
	// DNSSECKeys caches DNSSEC public-key metadata (ADR-0076).
	DNSSECKeys repository.DNSSECKeyRepository
	// PanelCerts is the M32 singleton panel_certificate repo. NewWithDeps
	// registers /admin/panel-certificate when set; nil keeps the routes
	// off (lab installs / older test wiring).
	PanelCerts repository.PanelCertificateRepository
	// M53 Updates Center (ADR-0118). UpdateState + UpdateHistory drive the
	// stateful Updates page; UpdateAutoupdate is the auto-update desired
	// state the autoupdate reconciler converges. Nil keeps the M29 thin
	// proxy behaviour (no persistence, no /state /history endpoints).
	UpdateState      repository.UpdateStateRepository
	UpdateHistory    repository.UpdateHistoryRepository
	UpdateAutoupdate repository.UpdateAutoupdateConfigRepository
	// M33 malware detection repos (ADR-0072). All five are wired
	// together — nil on any disables RegisterSecurityMalwareRoutes.
	// M30 backup-restore (ADR-0075). Nil disables the /admin/backups
	// + /me/backups routes; UI surfaces an empty state.
	BackupJobs repository.BackupJobRepository
	// M30.1 backup destinations + schedules (ADR-0078). All three nil
	// disables the /admin/backup-destinations + /admin/backup-schedules
	// routes (UI tabs hidden) and the in-process scheduler / copy
	// worker / finalizer goroutines.
	BackupDestinations repository.BackupDestinationRepository
	BackupSchedules    repository.BackupScheduleRepository
	MalwareQuarantine  repository.MalwareQuarantineRepository
	MalwareEvents      repository.MalwareEventRepository
	YARARules          repository.YARACustomRuleRepository
	MalwareSettings    repository.MalwareSettingsRepository
	// M33.2 mail YARA scanner — async tick walks Stalwart mailboxes via
	// JMAP, scans attachments with yr, quarantines hits. Off by default;
	// enabled via malware_settings.mail_scan_enabled. ADR-0079.
	MailScanState    repository.MailScanStateRepository
	MailScanFailures repository.MailScanFailureRepository
	// MalwareUserScans tracks per-user manual-scan jobs (mig 000097).
	// Upserted at scan start by the panel-api startScan handler;
	// finalised by the post-scan-hook ingest path. Drives the Manual
	// Scan UI's Last scanned + Status columns.
	MalwareUserScans repository.MalwareUserScanRepository
	// M34 per-user PHP-FPM egress firewall. Two repos backing the
	// nftables socket-cgroupv2 reconciler + admin/user request flow
	// (migrations 000100/000101, ADR-0084).
	UserEgressPolicies    repository.UserEgressPolicyRepository
	UserEgressRequests    repository.UserEgressRequestRepository
	UserEgressDropSamples repository.UserEgressDropSampleRepository
	// QuotaMount is the filesystem mount path /home lives on — passed
	// on every M18 user.limits.{apply,clear,report} agent call so the
	// agent can resolve `setquota -u <user> ... <mount>` without ever
	// defaulting to the disruptive `-a` flag. Resolved at startup via
	// internal/limits.QuotaMountFor("/home"); empty string disables
	// the disk-quota half of the limits pipeline (dev boxes).
	QuotaMount string
	SSO        *sso.Service
	SSOKey     *ssokey.Key
	// WebmailSSOMinter signs the JWT consumed by Bulwark's
	// /api/auth/impersonate endpoint (M6.6). Nil disables /sso/webmail
	// — handler surfaces 503 in that case. Loaded by serve.go from
	// /etc/jabali-panel/bulwark-jwt-auth.secret at boot.
	WebmailSSOMinter *webmailsso.Minter
	Log              *slog.Logger
	// Redis is the shared *redis.Client for the notification dispatcher
	// (ADR-0056) and future WordPress object-cache (ADR-0059). Wired in
	// serve.go against cfg.Redis.URL; nil when Redis is disabled (tests,
	// or an operator who set Redis.URL=""). Handlers that need it must
	// guard for nil and return 503 rather than panic.
	Redis *redis.Client
	// M14 notification repos. Wired by serve.go when sharedDB is set;
	// used by both the /admin/notifications admin API (added later) and
	// the dispatcher goroutine that drains the Redis stream.
	NotificationChannels repository.NotificationChannelRepository
	NotificationHistory  repository.NotificationHistoryRepository
	WebhookEndpoints     repository.WebhookEndpointRepository
	WebPushSubs          repository.WebPushSubscriptionRepository
	// NotificationQueue is the dispatcher's publish end — the internal
	// enqueue endpoint (RequireLocalhost) and in-process event sources
	// write to this Queue. Nil when Redis is not configured; handlers
	// must 503 rather than panic.
	NotificationQueue *notifications.Queue

	// NotificationRegistry maps channel kinds to their senders. Built once in
	// serve.go (with the provisioned notify-mailbox creds) and shared by the
	// dispatcher and the channels handler's synchronous "Send test" (GH #322).
	NotificationRegistry *notifications.Registry
	// AuditRecorder is the M49 one-write-path into the unified audit
	// log (ADR-0106): the recorder middleware + domain emitters call
	// Record(). Nil when Redis is not configured (same posture as
	// NotificationQueue); middleware.AuditRecord no-ops on nil.
	AuditRecorder audit.Recorder
	// AuditConsumer is the single-writer hash-chain consumer of
	// jabali:audit:queue. serve.go starts it as a goroutine alongside
	// the reconciler; nil without Redis.
	AuditConsumer *audit.Consumer
	// DB is the raw GORM handle. Most handlers go through typed repos
	// — this is only here for tiny one-off endpoints that need a
	// COUNT(*) across multiple tables (e.g. /admin/counts on the
	// admin Dashboard) where adding a CountAll method to every repo
	// would be more code than the call site.
	// M41 Snuffleupagus PHP hardening — repository + reconciler + rules path
	Snuffleupagus           repository.SnuffleupagusRepository
	SnuffleupagusReconciler *reconciler.SnuffleupagusReconciler
	SnuffleupagusBundleDir  string
	DB                      *gorm.DB
}

// Default tier: chosen so a reasonable SPA (polling, a few concurrent
// tabs) never notices, but a misbehaving client does. Tunable later via
// config if we hit real-world ceilings.
const (
	rateDefaultPerSec = 10.0
	rateDefaultBurst  = 40

	// Strict tier: credential endpoints. 5 req/min with a burst of 5
	// permits a typo-and-retry session without opening the door to
	// brute force from a single IP.
	rateStrictPerSec = 5.0 / 60.0
	rateStrictBurst  = 5

	// Kratos credential-flow tier: /.ory self-service login + recovery POSTs.
	// 15 req/min sustained with a burst of 10 leaves ample room for a real
	// login (init + submit, optional 2FA + typo retries) while throttling
	// single-IP online guessing / recovery enumeration below Kratos defaults.
	rateKratosPerSec = 15.0 / 60.0
	rateKratosBurst  = 10

	rateLimiterIdleCleanup = 10 * time.Minute
	rateLimiterSweepEvery  = 5 * time.Minute
)

// New returns a Gin engine configured with development defaults — no auth,
// no DB. Used by tests and any caller that doesn't need downstream deps.
// Production callers should use NewWithDeps so auth routes are mounted.
func New() *gin.Engine {
	return NewWithDeps(config.Defaults(), Deps{})
}

// NewWithDeps is the canonical constructor for the server. Handlers that
// depend on external collaborators (auth service, JWT issuer) are mounted
// only when their dep is non-nil, so early-phase deployments can still boot
// with a partial Deps.
func NewWithDeps(cfg *config.Config, deps Deps) *gin.Engine {
	if cfg.Server.Env == config.EnvProduction {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	// Apps registry. Built once per server. RegisterDefaults panics on
	// programmer error (duplicate name, bad ParamSpec) — those are
	// startup conditions we want surfaced loudly, not eaten by a
	// silent boot.
	if deps.Apps == nil {
		deps.Apps = apps.New()
		if err := apps.RegisterDefaults(deps.Apps); err != nil {
			panic("apps.RegisterDefaults: " + err.Error())
		}
	}

	r := gin.New()
	r.HandleMethodNotAllowed = true

	// Global middleware. Order matters:
	//   1. Recovery so a panic never leaves a connection open.
	//   2. RequestID so all subsequent log lines (including panics) carry it.
	//   3. CORS so preflights short-circuit before rate limit consumes tokens.
	r.Use(gin.Recovery())
	r.Use(middleware.RequestID())
	r.Use(middleware.CORS(cfg.CORS.AllowedOrigins))

	// Rate limiter — shared across handler mounts so both tiers count against
	// the same per-IP state. A background goroutine reaps idle buckets.
	rl := middleware.NewRateLimiter(middleware.RateLimiterConfig{
		DefaultRate:  rate.Limit(rateDefaultPerSec),
		DefaultBurst: rateDefaultBurst,
		StrictRate:   rate.Limit(rateStrictPerSec),
		StrictBurst:  rateStrictBurst,
		KratosRate:   rate.Limit(rateKratosPerSec),
		KratosBurst:  rateKratosBurst,
	})
	startRateLimiterSweeper(rl)
	r.Use(rl.Default())

	// Tag every /api/v1/* response with Cache-Control: no-store. Without
	// this, Firefox's Opaque-Response-Blocking decision cache can replay
	// a prior 401 with status=0ms, leaving the SPA stuck on a blank page
	// after token expiry.
	r.Use(middleware.NoCacheAPI())
	r.Use(middleware.RequireContentType())

	api.RegisterServiceInfoRoute(r)
	api.RegisterHealthRoutes(r)
	if deps.Agent != nil {
		api.RegisterAgentHealthRoute(r, deps.Agent)
	}

	// M14 internal enqueue — /api/v1/internal/notifications/enqueue
	// bypasses the Kratos authMiddleware because the caller is the
	// agent (or an in-process event source) talking to us over the M25
	// loopback socket. RequireLocalhost inside the route is the sole
	// gate. Registered off the root router (not v1) so it doesn't pick
	// up RequireKratosSession.
	api.RegisterNotificationsInternalRoutes(r.Group("/api/v1"), deps.NotificationQueue)

	// M33 internal malware ingest — /api/v1/admin/security/malware/event.
	// Mounted off the root router (not v1) so it bypasses the Kratos
	// authMiddleware. Caller is the agent over the M25 unix socket;
	// RequireLocalhost inside the route is the sole gate. Matches the
	// RegisterNotificationsInternalRoutes pattern above.
	if deps.MalwareQuarantine != nil && deps.MalwareEvents != nil &&
		deps.MalwareSettings != nil && deps.YARARules != nil &&
		deps.Agent != nil {
		api.RegisterSecurityMalwareInternalRoutes(r.Group("/api/v1"), api.SecurityMalwareHandlerConfig{
			Agent:      deps.Agent,
			Quarantine: deps.MalwareQuarantine,
			Events:     deps.MalwareEvents,
			Settings:   deps.MalwareSettings,
			YARARules:  deps.YARARules,
			Users:      deps.Users,
			UserScans:  deps.MalwareUserScans,
			Log:        deps.Log,
		})
	}

	// M28 — public branding endpoints (logo file + brand text). Lives
	// off the root API group with NO auth so the pre-login page can
	// render the operator's logo + brand text.
	if deps.ServerSettings != nil {
		api.RegisterPublicBrandingRoutes(r.Group("/api/v1"), api.BrandingHandlerConfig{
			Repo: deps.ServerSettings,
			Log:  deps.Log,
		})
	}

	// Webmail SSO landing (M6 Step 8 Phase B). Lives at /sso/webmail on
	// the engine root, not /api/v1, because it's served from
	// mail.<domain> (via nginx location /sso) where the SPA + Kratos
	// prefix doesn't apply. Handler does its own token auth (consume +
	// hash match) so no Kratos cookie is required.
	if deps.Mailboxes != nil && deps.Domains != nil && deps.SSOKey != nil && deps.MailboxSSOTokens != nil {
		api.RegisterWebmailSSORoutes(r, api.WebmailSSOHandlerConfig{
			Mailboxes: deps.Mailboxes,
			Domains:   deps.Domains,
			SSOKey:    deps.SSOKey,
			SSOTokens: deps.MailboxSSOTokens,
			Users:     deps.Users,
			Minter:    deps.WebmailSSOMinter,
			Log:       deps.Log,
		})
	}

	// Instantiate Kratos client + same-origin reverse proxy for /.ory/*.
	// The SPA fetches relative Kratos self-service endpoints and panel-api
	// binds :8443 directly (no nginx in front on that port), so we proxy
	// in-process. See kratos_proxy.go for the full rationale.
	if cfg.Auth.Kratos.PublicURL != "" {
		deps.KratosClient = kratosclient.NewClient(cfg.Auth.Kratos.PublicURL, cfg.Auth.Kratos.AdminURL)

		if err := RegisterKratosProxy(r, cfg.Auth.Kratos.PublicURL, rl, deps.Log); err != nil {
			deps.Log.Error("registering Kratos reverse proxy failed; login will be broken",
				"err", err, "public_url", cfg.Auth.Kratos.PublicURL)
		}
	}

	if deps.KratosClient != nil {
		// Protected API group — everything under /api/v1/* flows through
		// RequireKratosSession, which resolves Kratos identity UUIDs →
		// panel user rows so claims.UserID carries the ULID every
		// ownership check in the API expects.
		authMiddleware := middleware.RequireKratosSession(deps.KratosClient, deps.Users)

		// M44 Automation API public routes mount OUTSIDE the Kratos
		// session middleware — HMAC is the only auth on
		// /api/v1/automation/*. Mount BEFORE the auth-protected v1
		// group so the path matcher hits the unauth group first.
		if deps.AutomationTokens != nil && deps.SSOKey != nil {
			autoGroup := r.Group("/api/v1")
			api.RegisterAutomation(autoGroup, api.AutomationConfig{
				AutomationTokens: deps.AutomationTokens,
				Key:              deps.SSOKey,
				// M44 replay defense — Redis SETNX gate against
				// captured-request replay. Required in production.
				Redis:        deps.Redis,
				Domains:      deps.Domains,
				Users:        deps.Users,
				Applications: deps.WordPressInstalls,
				Agent:        deps.Agent, // JAB-75: powers read:status metrics
				// JAB-77: read:mail mail-stack inventory.
				Mailboxes:  deps.Mailboxes,
				MailGroups: deps.MailGroups,
				Forwarders: deps.Forwarders,
				// JAB-140 write layer: async-op tracker + write audit + M30 backup path.
				Operations:  automationOps(deps.DB),
				Audits:      automationAudits(deps.DB),
				BackupJobs:  automationBackupJobs(deps.DB),
				BackupDests: automationBackupDests(deps.DB),
				Notify:      automationNotifier(deps.NotificationQueue),
			})
		}

		// M51: chain bearer-token auth in front of the Kratos cookie
		// path. Token-authed callers populate claims the same way Kratos
		// does, so every ownership check downstream Just Works.
		combinedAuth := middleware.RequireUserAuth(deps.UserAPITokens, deps.Users, authMiddleware)
		// GH #245: scope-restricted API tokens are fail-closed here; full
		// tokens + browser sessions pass through.
		// CSRF: same-origin guard for cookie-authenticated mutations (GH #460),
		// before scope enforcement. Bearer/automation auth is exempt inside it.
		v1 := r.Group("/api/v1", combinedAuth, api.EnforceSameOriginForCookieMutations(), api.EnforceUserTokenScopes())
		// Register the user-facing token management endpoints. These
		// reject token-auth callers themselves (you can't mint new
		// tokens from a token), so even though v1 accepts both auth
		// modes, the /me/api-tokens routes are Kratos-only at the
		// handler level.
		api.RegisterMeAPITokensRoutes(v1, api.MeAPITokensConfig{Tokens: deps.UserAPITokens})

		// API docs (OpenAPI spec). Mounts at /api/v1/_meta/openapi.{json,yaml}
		// under the engine root, NOT inside the auth-protected v1 group —
		// the spec describes the public API surface, contains no secrets,
		// and is served to the docs UI page without requiring login. Per-
		// endpoint auth requirements are declared inside the spec itself.
		api.RegisterAPIDocsRoutes(r.Group("/api/v1"))

		// DDNS-protocol shim (GH #123). Mounts /nic/update at the
		// engine root (NOT under /api/v1 — every router firmware
		// hardcodes that path). Basic-auth wraps the same user API
		// token; the shim parses out the password and reuses the
		// hash lookup the bearer middleware does. Open path, no
		// auth middleware in front — Basic auth is the only gate.
		api.RegisterDDNSRoutes(r, api.DDNSConfig{
			Tokens:     deps.UserAPITokens,
			Domains:    deps.Domains,
			Zones:      deps.DNSZones,
			Records:    deps.DNSRecords,
			Reconciler: deps.Reconciler,
		})
		// ADR-0128 — admin act-as override. Runs after RequireKratosSession
		// (claims populated) and BEFORE AuditRecord so the audit records the
		// REAL admin as actor. No-op unless the X-Jabali-Act-As header is set
		// by a real admin presenting a valid grant.
		if deps.Users != nil && deps.DB != nil {
			v1.Use(middleware.ResolveImpersonation(
				repository.NewImpersonationGrantRepository(deps.DB), deps.Users, nil))
		}
		// M14 — fire one admin.login envelope per Kratos session.
		// Redis SETNX dedupes; downgrades to no-op without Redis/queue.
		v1.Use(middleware.TrackAdminLogin(deps.Redis, deps.NotificationQueue, deps.Log))
		// Auto-whitelist a logged-in user's IP in CrowdSec (time-boxed, all
		// users) so active sessions are never bounced from their own IP.
		if deps.Agent != nil {
			v1.Use(middleware.WhitelistLoginIP(deps.Redis, deps.Agent, deps.ServerSettings, deps.Log))
		}
		// M49 — record one audit event per mutating request (never
		// the body). No-op when AuditRecorder is nil (no Redis).
		v1.Use(middleware.AuditRecord(deps.AuditRecorder))

		// M353 Phase 1 (GH #353): module guards. A disabled module's routes must
		// 409, not just be hidden in the UI. Build one guard factory (shared TTL
		// cache) and mount each module's routes on a guarded sub-group. Fails
		// open (module on) when settings can't be read, so a DB blip never locks
		// a live feature out. Nil ServerSettings (tests) → unguarded plain v1.
		mailGroup, dnsGroup, securityGroup := v1, v1, v1
		if deps.ServerSettings != nil {
			guard := middleware.NewModuleGuard(deps.ServerSettings)
			mailGroup = v1.Group("", guard(middleware.ModuleMail, "mail"))
			dnsGroup = v1.Group("", guard(middleware.ModuleDNS, "dns"))
			securityGroup = v1.Group("", guard(middleware.ModuleSecurity, "security"))
		}
		api.RegisterMeRoutes(v1, api.MeHandlerConfig{
			Users:          deps.Users,
			Packages:       deps.Packages,
			ServerSettings: deps.ServerSettings,
			UIPrefs:        repository.NewUserUIPrefRepository(deps.DB),
		})
		// GH #256: per-user CLI default PHP version (self-service).
		api.RegisterMePhpCliRoutes(v1, api.MePhpCliConfig{
			Users: deps.Users,
			Agent: deps.Agent,
		})
		// Per-user disk-usage breakdown (files + email + databases).
		api.RegisterMeDiskUsageRoutes(v1, api.DiskUsageConfig{
			Users:      deps.Users,
			Domains:    deps.Domains,
			Mailboxes:  deps.Mailboxes,
			Databases:  deps.Databases,
			Agent:      deps.Agent,
			QuotaMount: deps.QuotaMount,
			Snapshots:  repository.NewDiskUsageSnapshotRepository(deps.DB),
		})

		// ADR-0128 — admin act-as grant management (GH #183). Mounted on v1;
		// ResolveImpersonation skips this path so it always runs as the real
		// admin (an admin can exit even while a header is set).
		if deps.Users != nil && deps.DB != nil {
			api.RegisterImpersonationRoutes(v1, api.ImpersonationHandlerConfig{
				Grants: repository.NewImpersonationGrantRepository(deps.DB),
				Users:  deps.Users,
			})
		}

		// M49 — unified audit log read surface (ADR-0106):
		// /admin/audit (RequireAdmin) + /me/activity (subject-scoped).
		if deps.DB != nil {
			api.RegisterAuditRoutes(v1, api.AuditHandlerConfig{
				Repo:  repository.NewAuditEventRepository(deps.DB),
				Users: deps.Users, // optional: resolves actor/subject IDs → name
				Log:   deps.Log,
			})
			// GH #575 — admin SSO maintenance (prune expired tokens).
			api.RegisterSSOAdminRoutes(v1, api.SSOAdminHandlerConfig{
				Tokens: repository.NewPhpMyAdminSSOTokenRepository(deps.DB),
			})
		}

		if deps.Users != nil {
			api.RegisterUserRoutes(v1, api.UserHandlerConfig{
				Repo:            deps.Users,
				Agent:           deps.Agent,
				StrictRateLimit: rl.Strict(),
				Domains:         deps.Domains,
				Databases:       deps.Databases,
				DatabaseUsers:   deps.DatabaseUsers,
				DockerApps:      deps.DockerApps,
				Mailboxes:       deps.Mailboxes,
				Packages:        deps.Packages,
				Reconciler:      deps.Reconciler,
				Log:             deps.Log,
				Redis:           deps.Redis,
				// M20: atomic Kratos identity creation on POST /users.
				KratosClient: deps.KratosClient,
			})
		}
		if deps.Packages != nil {
			api.RegisterPackageRoutes(v1, api.PackageHandlerConfig{
				Repo:       deps.Packages,
				Users:      deps.Users,
				Reconciler: deps.Reconciler,
				Log:        deps.Log,
			})
		}
		// M18 user-limits endpoints. Mount only when all deps are
		// present — a pre-M18 deployment with no LimitOverrides repo
		// simply won't expose the endpoints.
		if deps.Users != nil && deps.Packages != nil && deps.LimitOverrides != nil {
			api.RegisterUserLimitsRoutes(v1, api.UserLimitsHandlerConfig{
				Users:          deps.Users,
				Packages:       deps.Packages,
				LimitOverrides: deps.LimitOverrides,
				Agent:          deps.Agent,
				QuotaMount:     deps.QuotaMount,
			})
		}
		if deps.Domains != nil {
			api.RegisterDomainRoutes(v1, api.DomainHandlerConfig{
				Domains:     deps.Domains,
				Users:       deps.Users,
				Packages:    deps.Packages,
				Agent:       deps.Agent,
				Reconciler:  deps.Reconciler,
				SSLCerts:    deps.SSLCerts,
				SharedCerts: deps.SharedCerts,
				// DNS repos feed the auto-enable-email path in create.
				// Panel profiles without PowerDNS leave these nil and
				// create still works — auto-enable is skipped cleanly.
				DNSZones:   deps.DNSZones,
				DNSRecords: deps.DNSRecords,
				BWDaily:    deps.BWDaily,
				// M24: lets PATCH listen_ipv*_id resolve FK + family + the
				// is_user_selectable check, and lets GET denormalize
				// listen_ipv4 / listen_ipv6 onto each row. Optional —
				// when unset, the listen-IP fields are 503 on PATCH and
				// dropped from GET.
				ManagedIPs:     deps.ManagedIPs,
				ServerSettings: deps.ServerSettings,
			})
		}
		// M36 — per-domain IP allow/deny lists.
		if deps.Domains != nil && deps.DomainIPACLs != nil {
			var schedule func(string)
			if deps.Reconciler != nil {
				rec := deps.Reconciler
				schedule = func(id string) { rec.Schedule(id) }
			}
			api.RegisterDomainIPACLRoutes(v1, api.DomainIPACLHandlerConfig{
				Domains:   deps.Domains,
				ACLs:      deps.DomainIPACLs,
				Reconcile: schedule,
			})
		}
		// M6.6 — per-domain mail TLS opt-in + status.
		if deps.Domains != nil && deps.MailCerts != nil {
			var schedule func(string)
			if deps.Reconciler != nil {
				rec := deps.Reconciler
				schedule = func(id string) { rec.Schedule(id) }
			}
			api.RegisterDomainMailCertificateRoutes(v1, api.DomainMailCertHandlerConfig{
				Domains:   deps.Domains,
				MailCerts: deps.MailCerts,
				Reconcile: schedule,
			})
		}
		// M50 — per-directory password protection.
		if deps.Domains != nil && deps.DomainDirectoryPrivacy != nil {
			var schedule func(string)
			if deps.Reconciler != nil {
				rec := deps.Reconciler
				schedule = func(id string) { rec.Schedule(id) }
			}
			api.RegisterDomainDirectoryPrivacyRoutes(v1, api.DomainDirectoryPrivacyHandlerConfig{
				Domains:   deps.Domains,
				Privacy:   deps.DomainDirectoryPrivacy,
				Reconcile: schedule,
			})
		}
		// Per-domain docroot folder browser — powers the Directory
		// Privacy path picker. Reuses the M11 files.list agent call,
		// scoped to the domain owner + docroot.
		if deps.Domains != nil && deps.Users != nil && deps.Agent != nil {
			api.RegisterDomainPathBrowseRoutes(v1, api.DomainPathBrowseConfig{
				Domains: deps.Domains,
				Users:   deps.Users,
				Agent:   deps.Agent,
			})
		}
		if deps.Databases != nil && deps.DatabaseUsers != nil {
			api.RegisterDatabaseRoutes(v1, api.DatabaseHandlerConfig{
				Databases:         deps.Databases,
				DatabaseUsers:     deps.DatabaseUsers,
				DatabaseGrants:    deps.DatabaseUserGrants,
				WordPressInstalls: deps.WordPressInstalls,
				Users:             deps.Users,
				Packages:          deps.Packages,
				ServerSettings:    deps.ServerSettings,
				Agent:             deps.Agent,
			})
		}
		if deps.DatabaseUsers != nil && deps.DatabaseUserGrants != nil {
			api.RegisterDatabaseUserRoutes(v1, api.DatabaseUserHandlerConfig{
				Databases:      deps.Databases,
				DatabaseUsers:  deps.DatabaseUsers,
				DatabaseGrants: deps.DatabaseUserGrants,
				Users:          deps.Users,
				Packages:       deps.Packages,
				ServerSettings: deps.ServerSettings,
				Agent:          deps.Agent,
			})
		}
		if deps.Mailboxes != nil && deps.Domains != nil {
			api.RegisterMailboxRoutes(mailGroup, api.MailboxHandlerConfig{
				Mailboxes: deps.Mailboxes,
				Domains:   deps.Domains,
				Agent:     deps.Agent,
				SSOKey:    deps.SSOKey,
				SSOTokens: deps.MailboxSSOTokens,
			})
		}
		if deps.MailGroups != nil && deps.Mailboxes != nil && deps.Domains != nil {
			api.RegisterMailGroupRoutes(mailGroup, api.MailGroupHandlerConfig{
				Groups:    deps.MailGroups,
				Mailboxes: deps.Mailboxes,
				Domains:   deps.Domains,
				Agent:     deps.Agent,
			})
		}
		if deps.Domains != nil {
			api.RegisterDomainEmailRoutes(mailGroup, api.DomainEmailHandlerConfig{
				Domains:        deps.Domains,
				Agent:          deps.Agent,
				DNSZones:       deps.DNSZones,
				DNSRecords:     deps.DNSRecords,
				ServerSettings: deps.ServerSettings,
				SSLCerts:       deps.SSLCerts,
				SSLReconciler:  deps.Reconciler,
			})
		}
		// M6.5 Email features: forwarders, autoresponders, catch-all, disclaimer,
		// shared folders, logs. All sub-routes live in routes_m65.go and are
		// filled in by Wave B/C steps (ADR-0051).
		var srRepo repository.SharedResourceRepository
		if deps.DB != nil {
			srRepo = repository.NewSharedResourceRepository(deps.DB)
		}
		var sendDelegRepo repository.MailboxSendDelegationRepository
		if deps.DB != nil {
			sendDelegRepo = repository.NewMailboxSendDelegationRepository(deps.DB)
		}
		api.RegisterM65Routes(mailGroup, api.M65RouteDeps{
			Agent:           deps.Agent,
			Domains:         deps.Domains,
			Mailboxes:       deps.Mailboxes,
			Autoresponders:  deps.Autoresponders,
			Forwarders:      deps.Forwarders,
			MailboxShares:   deps.MailboxShares,
			SharedResources: srRepo,
			SendDelegations: sendDelegRepo,
		})
		// DNSSEC per-domain (ADR-0076). Standalone mount; not part of M6.5.
		api.RegisterDomainDNSSECRoutes(v1, api.DomainDNSSECHandlerConfig{
			Agent:   deps.Agent,
			Domains: deps.Domains,
			Keys:    deps.DNSSECKeys,
		})
		// Domain WHOIS lookup backing the admin "Information" modal (GH #254).
		api.RegisterDomainWhoisRoutes(v1, api.DomainWhoisHandlerConfig{
			Agent:   deps.Agent,
			Domains: deps.Domains,
		})
		// Per-domain nginx FastCGI micro-cache toggle (ADR-0108).
		api.RegisterDomainCacheRoutes(v1, api.DomainCacheHandlerConfig{
			Agent:      deps.Agent,
			Domains:    deps.Domains,
			Reconciler: deps.Reconciler,
		})
		// Per-domain SAN auto-add opt-out (M50 SSL ergonomics).
		api.RegisterDomainSkipAutoSANRoutes(v1, api.DomainSkipAutoSANConfig{
			Domains:    deps.Domains,
			Reconciler: deps.Reconciler,
		})
		// Per-domain MTA-STS toggle (M47 Wave 7b, ADR-0109).
		api.RegisterDomainMTAStsRoutes(v1, api.DomainMTAStsHandlerConfig{
			Agent:          deps.Agent,
			Domains:        deps.Domains,
			DNSZones:       deps.DNSZones,
			DNSRecords:     deps.DNSRecords,
			ServerSettings: deps.ServerSettings,
			Reconciler:     deps.Reconciler,
		})
		// Admin: System Updates (M29). Thin proxy to agent's system.* commands.
		api.RegisterAdminUpdatesRoutes(v1, api.AdminUpdatesHandlerConfig{
			Agent:          deps.Agent,
			State:          deps.UpdateState,
			History:        deps.UpdateHistory,
			Autoupdate:     deps.UpdateAutoupdate,
			ServerSettings: deps.ServerSettings,
		})
		// Tasks indicator: running update/backup/malware tasks for the top bar.
		api.RegisterAdminActiveTasksRoutes(v1, api.AdminActiveTasksConfig{
			Updates:    deps.UpdateHistory,
			BackupJobs: deps.BackupJobs,
			Agent:      deps.Agent,
		})
		// Admin: Support diagnostic report (M29, ADR-0064).
		api.RegisterAdminSupportRoutes(v1, api.AdminSupportHandlerConfig{
			Agent: deps.Agent,
		})
		// Admin: Server Status aggregator (M31, ADR-0065).
		api.RegisterAdminServerStatusRoutes(v1, api.AdminServerStatusHandlerConfig{
			Agent:          deps.Agent,
			Redis:          deps.Redis,
			ServerSettings: deps.ServerSettings,
		})
		// Admin: connection Speed Test endpoints backing the Server Status
		// card (GH #252). No deps — pure HTTP, RequireAdmin-gated.
		api.RegisterAdminSpeedtestRoutes(v1)
		// Admin: Service controls (M31). Mounts POST /admin/services/:name/:action.
		api.RegisterAdminServicesRoutes(v1, api.AdminServicesHandlerConfig{
			Agent: deps.Agent,
		})
		// M47 Wave 9 — admin Mail deliverability score card.
		api.RegisterAdminMailDeliverabilityRoutes(v1, api.AdminMailDeliverabilityHandlerConfig{
			MailRBLStates:   deps.MailRBLStates,
			DMARCAggregate:  deps.DMARCAggregate,
			TLSRPTAggregate: deps.TLSRPTAggregate,
			ARFReports:      deps.ARFReports,
			ServerSettings:  deps.ServerSettings,
		})
		// M47 Wave 3 — admin outbound-throttle CRUD.
		api.RegisterAdminMailThrottlesRoutes(v1, api.AdminMailThrottlesHandlerConfig{
			Policies:       deps.MailOutboundPolicies,
			ThrottleClient: deps.StalwartAdminThrottle,
		})
		// M47: admin mail-queue (ADR-0103) over the Wave-1 agent.
		api.RegisterAdminMailQueueRoutes(v1, deps.Agent)
		// Admin: process kill (Server Status page). POST /admin/processes/:pid/kill.
		api.RegisterAdminProcessesRoutes(v1, api.AdminProcessesHandlerConfig{
			Agent: deps.Agent,
		})
		// Admin: dashboard counts (one-shot users/domains/mailboxes totals).
		api.RegisterAdminCountsRoutes(v1, api.AdminCountsHandlerConfig{
			DB: deps.DB,
		})
		// Admin: Panel SSL certificate (M32, ADR-0066). Skipped when
		// PanelCerts isn't wired (legacy test fixtures).
		if deps.PanelCerts != nil && deps.ServerSettings != nil {
			api.RegisterAdminPanelCertificateRoutes(v1, api.AdminPanelCertificateHandlerConfig{
				PanelCerts:     deps.PanelCerts,
				ServerSettings: deps.ServerSettings,
				Agent:          deps.Agent,
			})
		}
		if deps.Domains != nil && deps.DNSZones != nil && deps.DNSRecords != nil {
			api.RegisterDNSRoutes(dnsGroup, api.DNSHandlerConfig{
				Domains:        deps.Domains,
				Zones:          deps.DNSZones,
				Records:        deps.DNSRecords,
				ServerSettings: deps.ServerSettings,
				Reconciler:     deps.Reconciler,
			})
		}
		if deps.Domains != nil && deps.SSLCerts != nil {
			api.RegisterSSLRoutes(v1, api.SSLHandlerConfig{
				Domains:        deps.Domains,
				SSLCerts:       deps.SSLCerts,
				SharedCerts:    deps.SharedCerts,
				Agent:          deps.Agent,
				PanelCerts:     deps.PanelCerts,
				MailCerts:      deps.MailCerts,
				ServerSettings: deps.ServerSettings,
				Reconciler:     deps.Reconciler,
				Config:         cfg,
			})
		}
		if deps.ServerSettings != nil {
			api.RegisterServerSettingsRoutes(v1, api.ServerSettingsHandlerConfig{
				Repo:  deps.ServerSettings,
				Agent: deps.Agent,
				Log:   deps.Log,
			})
			// M28 — admin logo upload/delete. Public GET lives on the
			// root router above so it's reachable pre-auth.
			api.RegisterBrandingRoutes(v1, api.BrandingHandlerConfig{
				Repo:  deps.ServerSettings,
				Log:   deps.Log,
				Agent: deps.Agent,
			})
		}
		// M37 Phase 4: Postgres lifecycle admin endpoints (status +
		// destructive uninstall). The toggle itself rides PATCH
		// /admin/settings; these endpoints expose what the toggle
		// alone can't reveal.
		if deps.Agent != nil {
			api.RegisterPostgresAdminRoutes(v1, api.PostgresAdminHandlerConfig{
				Agent: deps.Agent,
				Log:   deps.Log,
			})
		}
		// M46: database server admin ops (root pw, config, maintenance,
		// processes, admin SSO). Lives in the existing Databases tab.
		if deps.DBAdmin != nil {
			api.RegisterDatabaseAdminOpsRoutes(v1, api.DatabaseAdminOpsHandlerConfig{
				Agent:      deps.Agent,
				DBAdmin:    deps.DBAdmin,
				Log:        deps.Log,
				Queue:      deps.NotificationQueue,
				SSO:        deps.SSO,
				AdminerSSO: deps.AdminerSSO,
				Recorder:   deps.AuditRecorder,
			})
		}
		if deps.PageTemplates != nil {
			api.RegisterPageTemplatesRoutes(v1, api.PageTemplatesHandlerConfig{
				Repo: deps.PageTemplates,
				Log:  deps.Log,
			})
		}
		if deps.AccountSkeleton != nil {
			api.RegisterAccountSkeletonRoutes(v1, api.AccountSkeletonHandlerConfig{
				Repo: deps.AccountSkeleton,
				Log:  deps.Log,
			})
		}
		if deps.NotificationEventSettings != nil {
			api.RegisterNotificationEventSettingsRoutes(v1, api.NotificationEventSettingsHandlerConfig{
				Repo: deps.NotificationEventSettings,
				Log:  deps.Log,
			})
		}
		// M6.4 Settings → Email: read-only panel-primary domain card.
		if deps.Domains != nil {
			api.RegisterSettingsEmailRoutes(v1, api.SettingsEmailHandlerConfig{
				Domains: deps.Domains,
				Log:     deps.Log,
			})
		}
		if deps.ManagedIPs != nil {
			// Step 4 flips AgentIPCommandsEnabled on: CRUD round-trips
			// through the agent's ip.bind / ip.unbind commands, and the
			// reconciler's managed-ip pass rebinds any DB-bound rows
			// the kernel has lost (host reboot between missing netplan
			// persistence and agent recovery).
			api.RegisterIPRoutes(v1, api.IPHandlerConfig{
				Repo:                   deps.ManagedIPs,
				Domains:                deps.Domains,
				Agent:                  deps.Agent,
				AgentIPCommandsEnabled: deps.Agent != nil,
				Log:                    deps.Log,
			})
		}

		// M14 Step 5 — admin channel CRUD + broadcast/test.
		if deps.NotificationChannels != nil {
			api.RegisterNotificationsChannelsRoutes(v1, api.NotificationsChannelsHandlerConfig{
				Channels:        deps.NotificationChannels,
				Webhooks:        deps.WebhookEndpoints,
				Queue:           deps.NotificationQueue,
				Registry:        deps.NotificationRegistry,
				Log:             deps.Log,
				StrictRateLimit: rl.Strict(),
			})
		}
		// M31.1 follow-up — DLQ inspector (list / replay / drop / clear).
		api.RegisterNotificationsDLQRoutes(v1, api.NotificationsDLQHandlerConfig{
			Redis: deps.Redis,
			Log:   deps.Log,
		})
		// M14 Step 5 — authenticated-user bell dropdown.
		if deps.NotificationHistory != nil {
			api.RegisterNotificationsInboxRoutes(v1, api.NotificationsInboxHandlerConfig{
				History: deps.NotificationHistory,
				Redis:   deps.Redis,
				Log:     deps.Log,
			})
		}
		// M14 Step 5 — Web Push enrolment.
		if deps.WebPushSubs != nil && deps.ServerSettings != nil {
			api.RegisterNotificationsWebPushRoutes(v1, api.NotificationsWebPushHandlerConfig{
				ServerSettings: deps.ServerSettings,
				Subs:           deps.WebPushSubs,
				Channels:       deps.NotificationChannels,
				Log:            deps.Log,
			})
		}
		if deps.Agent != nil {
			api.RegisterSystemRoutes(v1, deps.Agent)
			api.RegisterPHPVersionRoutes(v1, deps.Agent)
			// M26 Step 4 — admin Security tab. CrowdSec + UFW are pure
			// agent passthroughs. ModSecurity removed 2026-04-26 (ADR-0055
			// SUPERSEDED) — CrowdSec AppSec covers the WAF role.
			api.RegisterSecurityCrowdSecRoutes(securityGroup, deps.Agent, deps.ServerSettings)
			api.RegisterSecurityAppSecRoutes(securityGroup, deps.Agent, deps.ServerSettings)
			api.RegisterSecurityUFWRoutes(securityGroup, deps.Agent)
			// M40 (ADR-0086) AppArmor admin status + per-profile mode flip.
			api.RegisterSecurityAppArmorRoutes(securityGroup, deps.Agent)
			// M42 (ADR-0087) AIDE FIM read-only status + manual check trigger.
			api.RegisterSecurityAideRoutes(securityGroup, deps.Agent)
			// M43 (ADR-0089) Trust test bench — "would this IP be blocked?" admin endpoint.
			api.RegisterSecurityTrustRoutes(securityGroup, deps.Agent)
			// M41 (ADR-0088) Snuffleupagus PHP hardening — status/mode/rules/incidents.
			if deps.Snuffleupagus != nil {
				api.RegisterSecuritySnuffleupagusRoutes(securityGroup, api.SecuritySnuffleupagusConfig{
					Agent:      deps.Agent,
					Repo:       deps.Snuffleupagus,
					Reconciler: deps.SnuffleupagusReconciler,
					BundleDir:  deps.SnuffleupagusBundleDir,
				})
			}
			// M33 malware tab — ADR-0072. All five malware repos must be
			// wired or RegisterSecurityMalwareRoutes is skipped (older test
			// wiring without the M33 graph). Tab still renders empty state.
			if deps.MalwareQuarantine != nil && deps.MalwareEvents != nil &&
				deps.MalwareSettings != nil && deps.YARARules != nil {
				api.RegisterSecurityMalwareRoutes(v1, api.SecurityMalwareHandlerConfig{
					Agent:      deps.Agent,
					Quarantine: deps.MalwareQuarantine,
					Events:     deps.MalwareEvents,
					Settings:   deps.MalwareSettings,
					YARARules:  deps.YARARules,
					Users:      deps.Users,
					UserScans:  deps.MalwareUserScans,
					Log:        deps.Log,
				})
			}
		}
		if deps.BackupJobs != nil && deps.Users != nil {
			api.RegisterBackupRoutes(v1, api.BackupHandlerConfig{
				Agent:          deps.Agent,
				Jobs:           deps.BackupJobs,
				Destinations:   deps.BackupDestinations,
				Users:          deps.Users,
				Databases:      deps.Databases,
				DatabaseUsers:  deps.DatabaseUsers,
				DatabaseGrants: deps.DatabaseUserGrants,
				Domains:        deps.Domains,
				Mailboxes:      deps.Mailboxes,
				AppInstalls:    deps.WordPressInstalls,
				DNSZones:       deps.DNSZones,
				DNSRecords:     deps.DNSRecords,
				Log:            deps.Log,
				SSOKey:         deps.SSOKey,
			})
			api.RegisterMeBackupRoutes(v1, api.MeBackupsHandlerConfig{
				Agent:          deps.Agent,
				Jobs:           deps.BackupJobs,
				Destinations:   deps.BackupDestinations,
				Users:          deps.Users,
				Packages:       deps.Packages,
				Databases:      deps.Databases,
				DatabaseUsers:  deps.DatabaseUsers,
				DatabaseGrants: deps.DatabaseUserGrants,
				Domains:        deps.Domains,
				Mailboxes:      deps.Mailboxes,
				AppInstalls:    deps.WordPressInstalls,
				DNSZones:       deps.DNSZones,
				DNSRecords:     deps.DNSRecords,
				// GH #454 Step 4: tenant scheduled-backup card. Schedules +
				// Settings (the admin-owned cron time) gate the /me/backup-schedule
				// routes; absent, the card's endpoints simply aren't mounted.
				Schedules: deps.BackupSchedules,
				Settings:  deps.ServerSettings,
				Notify:    deps.NotificationQueue,
				Log:       deps.Log,
			})
		}
		// M30.1 (ADR-0078): backup destinations + schedules.
		if deps.BackupDestinations != nil {
			api.RegisterBackupDestinationRoutes(v1, api.BackupDestinationsConfig{
				Repo:   deps.BackupDestinations,
				Agent:  deps.Agent,
				SSOKey: deps.SSOKey,
			})
			// SSH-key listing/generate endpoints feed the SFTP key
			// dropdown on the destinations drawer. Both ops run as
			// root via the agent (panel-api can't read /root/.ssh
			// or write /etc/jabali-panel/restic-remotes/).
			api.RegisterSystemSSHKeysRoutes(v1, api.SystemSSHKeysConfig{
				Agent: deps.Agent,
			})
			// Master restic password reveal — admin-only. Per
			// ADR-0075 operators must back the password up
			// out-of-band; losing it loses every snapshot.
			api.RegisterBackupEncryptionKeyRoutes(v1, api.BackupEncryptionKeyConfig{
				Agent: deps.Agent,
			})
		}
		if deps.BackupSchedules != nil && deps.BackupDestinations != nil {
			api.RegisterBackupScheduleRoutes(v1, api.BackupSchedulesConfig{
				Schedules:    deps.BackupSchedules,
				Destinations: deps.BackupDestinations,
				Users:        deps.Users,
				Jobs:         deps.BackupJobs,
			})
		}
		if deps.SSO != nil && deps.Databases != nil && deps.PhpMyAdminSSOTokens != nil {
			api.RegisterSSOPhpMyAdminRoutes(v1, api.SSOPhpMyAdminHandlerConfig{
				Databases: deps.Databases,
				SSO:       deps.SSO,
				Log:       deps.Log,
			})
		}
		// M37 Phase 4: Adminer SSO mint route — engine-aware.
		if deps.SSO != nil && deps.AdminerSSO != nil && deps.Databases != nil && deps.AdminerSSOTokens != nil {
			api.RegisterSSOAdminerRoutes(v1, api.SSOAdminerHandlerConfig{
				Databases: deps.Databases,
				SSO:       deps.SSO,
				Adminer:   deps.AdminerSSO,
				Log:       deps.Log,
			})
		}
		if deps.PHPPools != nil && deps.PHPPoolIniOverrides != nil {
			// Pool tuning (pm.*, ini overrides) is admin-only: tenants must not
			// resize the shared worker pool. Per-domain PHP *version* binding
			// stays user-facing via RegisterDomainPHPPoolRoutes below.
			api.RegisterPHPPoolRoutes(v1.Group("", middleware.RequireAdmin()), api.PHPPoolHandlerConfig{
				PHPPools:            deps.PHPPools,
				PHPPoolIniOverrides: deps.PHPPoolIniOverrides,
				Domains:             deps.Domains,
				Users:               deps.Users,
				Agent:               deps.Agent,
			})
		}
		if deps.PHPPerformanceModes != nil {
			api.RegisterPHPPerformanceModeRoutes(v1, api.PHPPerformanceModeHandlerConfig{
				Modes: deps.PHPPerformanceModes,
			})
		}
		if deps.PHPPools != nil && deps.Packages != nil && deps.PHPPerformanceModes != nil {
			api.RegisterPHPUserTuningRoutes(v1, api.PHPUserTuningHandlerConfig{
				Agent:               deps.Agent,
				Users:               deps.Users,
				Packages:            deps.Packages,
				PHPPools:            deps.PHPPools,
				PHPPoolIniOverrides: deps.PHPPoolIniOverrides,
				Modes:               deps.PHPPerformanceModes,
			})
		}
		if deps.Domains != nil && deps.PHPPools != nil {
			api.RegisterDomainPHPPoolRoutes(v1, api.DomainPHPPoolHandlerConfig{
				Domains:             deps.Domains,
				PHPPools:            deps.PHPPools,
				PHPPoolIniOverrides: deps.PHPPoolIniOverrides,
				Users:               deps.Users,
				Agent:               deps.Agent,
			})
		}
		if deps.Domains != nil {
			api.RegisterDomainPHPSettingsRoutes(v1, api.DomainPHPSettingsHandlerConfig{
				Domains:  deps.Domains,
				PHPPools: deps.PHPPools,
			})
		}
		if deps.Domains != nil {
			api.RegisterDomainHtaccessRoutes(v1, api.DomainHtaccessHandlerConfig{
				Domains: deps.Domains,
			})
		}
		if deps.WordPressInstalls != nil && deps.Databases != nil && deps.DatabaseUsers != nil &&
			deps.DatabaseUserGrants != nil && deps.Domains != nil && deps.Users != nil && deps.Agent != nil {
			appCfg := api.ApplicationHandlerConfig{
				ApplicationInstalls: deps.WordPressInstalls,
				Databases:           deps.Databases,
				DatabaseUsers:       deps.DatabaseUsers,
				DatabaseGrants:      deps.DatabaseUserGrants,
				Domains:             deps.Domains,
				Users:               deps.Users,
				Packages:            deps.Packages,
				Agent:               deps.Agent,
				Apps:                deps.Apps,
				CronJobs:            deps.CronJobs,
				Redis:               deps.Redis,
				Reconciler:          deps.Reconciler,
				CacheTokenSecret:    cacheHMACSecret(),
				CacheTokenSalts:     repository.NewCacheTokenSaltRepository(deps.DB),
				CacheWarmupRuns:     repository.NewCacheWarmupRunRepository(deps.DB),
				CacheDoctorRuns:     repository.NewCacheDoctorRunRepository(deps.DB),
			}
			api.RegisterApplicationRoutes(v1, appCfg)

			// Log access routes (M13)
			if deps.LogAccessStreams != nil && deps.Domains != nil && deps.Users != nil {
				api.RegisterLogRoutes(v1, api.LogHandlerConfig{
					LogAccessStreams: deps.LogAccessStreams,
					Domains:          deps.Domains,
					Users:            deps.Users,
				})
				// WebSocket log streaming routes
				api.RegisterLogStreamRoutes(v1, api.LogStreamHandlerConfig{
					LogAccessStreams: deps.LogAccessStreams,
					Domains:          deps.Domains,
					Log:              deps.Log,
				})
			}
		}

		// Magic-link admin login (ADR-0040): mint-only. The agent-written
		// PHP file is its own validator, so no validate endpoint lives here.
		if deps.WordPressInstalls != nil && deps.Domains != nil &&
			deps.Users != nil && deps.Agent != nil {
			api.RegisterMagicLinkRoutes(v1, api.MagicLinkHandlerConfig{
				ApplicationInstalls: deps.WordPressInstalls,
				Domains:             deps.Domains,
				Users:               deps.Users,
				Agent:               deps.Agent,
			})
		}

		// Cron jobs routes (M8)
		if deps.CronJobs != nil && deps.Users != nil && deps.Domains != nil && deps.Agent != nil {
			api.RegisterCronRoutes(v1, api.CronHandlerConfig{
				CronJobs: deps.CronJobs,
				Users:    deps.Users,
				Domains:  deps.Domains,
				Agent:    deps.Agent,
				Log:      deps.Log,
			})
		}

		// Docker app marketplace routes (M48). Optional; when the
		// docker_apps repo + catalog are not wired the route group
		// stays unmounted and any /admin/docker-apps/* request 404s.
		if deps.DockerApps != nil && deps.Agent != nil {
			api.RegisterDockerAppRoutes(v1, api.DockerAppHandlerConfig{
				Repo:           deps.DockerApps,
				Catalog:        deps.DockerCatalog,
				ServerSettings: deps.ServerSettings,
				Domains:        deps.Domains,
				Agent:          deps.Agent,
				Users:          deps.Users,
				Log:            deps.Log,
			})
		}
		// M49 (GH #170): tenant docker apps. Mounts /docker-apps on the
		// authed base group; gated by the /etc/jabali/docker-tenant-enabled
		// host flag (written by `jabali docker enable-tenant`).
		if deps.DockerApps != nil && deps.DockerCatalog != nil && deps.Agent != nil {
			api.RegisterUserDockerAppRoutes(v1, api.UserDockerAppHandlerConfig{
				Repo:           deps.DockerApps,
				Catalog:        deps.DockerCatalog,
				Domains:        deps.Domains,
				Agent:          deps.Agent,
				Users:          deps.Users,
				Packages:       deps.Packages,
				ServerSettings: deps.ServerSettings,
				Log:            deps.Log,
			})
		}
		if deps.PythonApps != nil && deps.Agent != nil && deps.Domains != nil {
			api.RegisterPythonAppRoutes(v1, api.PythonAppHandlerConfig{
				Apps:       deps.PythonApps,
				Domains:    deps.Domains,
				Users:      deps.Users,
				Packages:   deps.Packages,
				Settings:   deps.ServerSettings,
				Agent:      deps.Agent,
				Reconciler: deps.Reconciler,
				Catalog:    deps.PyFrameworks,
			})
		}

		// File manager routes (M11)
		if deps.Users != nil && deps.Agent != nil {
			api.RegisterFilesRoutes(v1, api.FilesHandlerConfig{
				Users:          deps.Users,
				Domains:        deps.Domains,
				Agent:          deps.Agent,
				Log:            deps.Log,
				ServerSettings: deps.ServerSettings,
			})
		}

		// SSH keys routes
		if deps.SSHKeys != nil && deps.Reconciler != nil {
			api.RegisterSSHKeysRoutes(v1, api.SSHKeysHandlerConfig{
				SSHKeys:    deps.SSHKeys,
				Reconciler: deps.Reconciler,
				Logger:     deps.Log,
			})
		}

		// Admin routes
		if deps.Reconciler != nil {
			admin := v1.Group("/admin", middleware.RequireAdmin())
			api.RegisterReconcileRoutes(admin, &api.ReconcileHandlerConfig{
				Reconciler: deps.Reconciler,
				Log:        deps.Log,
			})
		}
		// M44: Automation API token management. Admin mints + revokes
		// HMAC-signed tokens; the matching public read-only routes
		// at /api/v1/automation/* are wired below outside the auth
		// middleware (HMAC is the auth there, no Kratos session).
		if deps.AutomationTokens != nil && deps.SSOKey != nil {
			api.RegisterAdminAutomationTokens(v1, api.AdminAutomationTokensConfig{
				Repo:  deps.AutomationTokens,
				Key:   deps.SSOKey,
				Audit: deps.AuditRecorder,
			})
		}
		if deps.MigrationJobs != nil {
			// M35 Step 8 read-only admin REST. Mutation
			// endpoints (create/resume) land alongside the SPA
			// once the JMAP-push + CreateUser orchestrator
			// stabilise.
			api.RegisterAdminMigrationRoutes(v1, api.AdminMigrationsHandlerConfig{
				Jobs:      deps.MigrationJobs,
				SizeCache: deps.MigrationSizeCache,
				Settings:  deps.ServerSettings,
				Agent:     deps.Agent,
			})
			// GH #647/#648: tenant owner-scoped WordPress self-migration.
			if deps.Domains != nil && deps.Users != nil && deps.Agent != nil {
				api.RegisterTenantMigrationRoutes(v1, api.TenantMigrationsConfig{
					Jobs:     deps.MigrationJobs,
					Domains:  deps.Domains,
					Users:    deps.Users,
					Agent:    deps.Agent,
					Settings: deps.ServerSettings,
				})
			}
		}
		if deps.Agent != nil {
			admin := v1.Group("/admin", middleware.RequireAdmin())
			api.RegisterPHPVersionAdminRoutes(admin, deps.Agent, deps.ServerSettings)
			api.RegisterPHPExtensionAdminRoutes(admin, deps.Agent)
		}

		// M45 root web terminal (ADR-0096). Off by default; the handler
		// re-checks server_settings.root_terminal_enabled on every mint
		// + WS upgrade. Mounted under the admin RequireAdmin group.
		if deps.TerminalSessions != nil && deps.ServerSettings != nil {
			tadmin := v1.Group("/admin", middleware.RequireAdmin())
			api.RegisterTerminalRoutes(tadmin, api.TerminalHandlerConfig{
				Sessions:       deps.TerminalSessions,
				ServerSettings: deps.ServerSettings,
				Notifications:  deps.NotificationQueue,
				Log:            deps.Log,
			})
		}
		// M34 per-user PHP-FPM egress firewall — admin + user routes share
		// the same handler config; admin half mounts under /admin
		// (RequireAdmin) and user half under the auth-only base group so
		// /me/* resolves to the caller via JWT.
		if deps.Users != nil && deps.UserEgressPolicies != nil && deps.UserEgressRequests != nil {
			adminEgress := v1.Group("/admin", middleware.RequireAdmin())
			cfg := api.UserEgressHandlerConfig{
				Users:       deps.Users,
				Policies:    deps.UserEgressPolicies,
				Requests:    deps.UserEgressRequests,
				DropSamples: deps.UserEgressDropSamples,
				Agent:       deps.Agent,
			}
			api.RegisterAdminUserEgressRoutes(adminEgress, cfg)
			api.RegisterMeEgressRoutes(v1, cfg)
		}
	}

	// Static SPA: owns r.NoRoute — serves embedded panel-ui/dist for
	// unknown paths with client-side routing fallback, JSON 404 for
	// API-ish prefixes.
	webui.RegisterStatic(r, panelui.Assets())
	// Method-not-allowed handler stays on the api side for JSON symmetry.
	api.RegisterMethodNotAllowedHandler(r)
	return r
}

// startRateLimiterSweeper launches a background goroutine that drops idle
// per-IP buckets so the limiter's memory stays bounded. Safe to call once
// per process; the goroutine lives for the program's lifetime.
func startRateLimiterSweeper(rl *middleware.RateLimiter) {
	ticker := time.NewTicker(rateLimiterSweepEvery)
	go func() {
		for range ticker.C {
			rl.Cleanup(rateLimiterIdleCleanup)
		}
	}()
}

// cacheHMACSecret returns the dedicated WP-cache token-derivation secret used to
// derive per-tenant Redis ACL tokens (GH #407). It is deliberately SEPARATE from
// JABALI_REDIS_PANEL_TOKEN so the panel's master Redis credential can be rotated
// without stranding every tenant's wp-config cache token. Falls back to the
// master token (with a one-time warning) for installs predating the dedicated
// secret, so caching keeps working until install.sh provisions it.
func cacheHMACSecret() string {
	if s := os.Getenv("JABALI_WP_CACHE_HMAC_SECRET"); s != "" {
		return s
	}
	if s := os.Getenv("JABALI_REDIS_PANEL_TOKEN"); s != "" {
		slog.Warn("JABALI_WP_CACHE_HMAC_SECRET unset — deriving WP-cache tokens from JABALI_REDIS_PANEL_TOKEN (GH #407); set the dedicated secret so rotating the master token does not strand tenant caches")
		return s
	}
	return ""
}

// automationOps builds the JAB-140 async-operation repo (nil when no DB).
func automationOps(db *gorm.DB) repository.AutomationOperationRepository {
	if db == nil {
		return nil
	}
	return repository.NewAutomationOperationRepository(db)
}

// automationAudits builds the write-audit repo for the automation write layer.
func automationAudits(db *gorm.DB) repository.AuditEventRepository {
	if db == nil {
		return nil
	}
	return repository.NewAuditEventRepository(db)
}

// automationBackupJobs / automationBackupDests wire the M30 system-backup path
// into the automation write layer (JAB-140).
func automationBackupJobs(db *gorm.DB) repository.BackupJobRepository {
	if db == nil {
		return nil
	}
	return repository.NewBackupJobRepository(db)
}

func automationBackupDests(db *gorm.DB) repository.BackupDestinationRepository {
	if db == nil {
		return nil
	}
	return repository.NewBackupDestinationRepository(db)
}

// automationNotifier returns the M14 publisher for the automation write layer, or
// a genuine nil interface when the queue is absent (a typed-nil *Queue would make
// the interface non-nil and defeat the nil-check in notifyWrite).
func automationNotifier(q *notifications.Queue) api.AutomationNotifier {
	if q == nil {
		return nil
	}
	return q
}
