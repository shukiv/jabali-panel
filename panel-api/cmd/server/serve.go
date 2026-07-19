package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/app"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupfinalizer"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupscheduler"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/db"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/eventsources"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailscan"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications/senders"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/pyframeworks"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/reconciler"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/reconciler/phases"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sso"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
	stalwartadmin "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/stalwartadmin"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/webmailsso"

	// M35 migration importer registry — blank imports run each
	// importer's init() so the source-kind → Discoverer factory
	// is available before the first admin REST call. Adding a
	// new source kind = adding one line here.
	_ "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	_ "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/directadmin"
	_ "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/hestiacp"
	_ "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/plesk"
)

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 90 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Jabali Panel HTTP(S) server",
		RunE:  runServe,
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if err := initConfig(); err != nil {
				return err
			}
			return nil
		},
	}
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg := sharedCfg
	log := sharedLog

	log.Info("starting jabali-panel",
		"addr", cfg.Server.Addr,
		"env", cfg.Server.Env,
	)

	// ---- DB ----
	if cfg.Database.URL != "" && cfg.Database.URL != "placeholder-until-phase-3" {
		if os.Getenv("SKIP_MIGRATIONS") != "true" {
			if err := db.Migrate(cfg.Database.URL); err != nil {
				return err
			}
			log.Info("migrations up-to-date")
		}
		if err := initDB(); err != nil {
			return err
		}
		log.Info("db connected")
	} else {
		log.Warn("DATABASE_URL not set; running without DB")
	}

	// ---- agent ----
	if err := initAgent(); err != nil {
		return err
	}
	log.Info("agent client configured", "socket", cfg.Agent.SocketPath)

	// ---- SSO key ----
	// Deps.SSOKey stays nil when the key file is absent; the SSO handler
	// refuses requests in that state so the feature is opt-in without
	// blocking startup on a missing file.
	// Use a pointer to allow hot-reloading on SIGHUP.
	ssoKeyPtr := loadSSOKey(cfg.SSO.KeyPath, log)

	// ---- Redis ----
	// ADR-0056 dispatcher + ADR-0059 shared cache client. Parse the URL
	// (unix:// or redis://), open a single connection pool, ping with a
	// short timeout to fail-fast if Redis is unreachable. Missing Redis
	// is fatal in production — the notification dispatcher must have
	// its queue — but configurable-out so tests can run without Redis.
	var redisClient *redis.Client
	if cfg.Redis.URL != "" {
		opts, err := redis.ParseURL(cfg.Redis.URL)
		if err != nil {
			return fmt.Errorf("parse redis url %q: %w", cfg.Redis.URL, err)
		}
		// ADR-0148 (#406): once Redis' default user is locked for the WP-cache
		// multi-tenant ACL model, panel-api must authenticate. It uses a single
		// client across ALL its keyspaces — jabali:* (notifications, audit,
		// login/session, secret) AND automation:replay:* — and also runs the
		// tenant ACL lifecycle, so the jabali_panel user is scoped to those key
		// patterns with +acl (not the notifications-only scope first drafted).
		// Token seeded by install.sh into panel.env BEFORE default is locked;
		// absent on a pre-ACL host → no auth, default still nopass → unchanged.
		if tok := os.Getenv("JABALI_REDIS_PANEL_TOKEN"); tok != "" {
			opts.Username = "jabali_panel"
			opts.Password = tok
		}
		redisClient = redis.NewClient(opts)
		pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := redisClient.Ping(pingCtx).Err(); err != nil {
			return fmt.Errorf("redis ping %q: %w (is redis-server up? install_redis() sets up /run/redis/redis.sock)", cfg.Redis.URL, err)
		}
		log.Info("redis connected", "url", cfg.Redis.URL)
	} else {
		log.Warn("redis URL not set; notification dispatcher will be disabled")
	}

	// ---- auth + deps ----
	var deps app.Deps
	deps.Agent = sharedAgent
	deps.Log = log
	deps.SSOKey = ssoKeyPtr
	deps.Redis = redisClient

	// M6.6 — load Bulwark impersonate JWT secret + construct minter.
	// File is rendered by install.sh _install_bulwark_impersonate_secrets
	// at 0640 root:jabali-webmail; panel-api runs as jabali so reads via
	// fs (no group needed — file is 0640 with group jabali-webmail, but
	// jabali user must be a supplementary member). On panels without
	// the secret file (older installs, dev fixtures) we leave the
	// minter nil so /sso/webmail returns 503 cleanly.
	if secretBytes, sErr := os.ReadFile("/etc/jabali-panel/bulwark-jwt-auth.secret"); sErr == nil {
		// Use the first whitespace-delimited token, not just TrimSpace.
		// A pre-fix install (GH #193) could embed openssl's line-wrap
		// newline mid-secret; Bulwark's line-based env parser keeps only
		// the pre-newline portion, so we must sign with the same token or
		// every JWT fails verification ("Invalid signature"). On a clean
		// secret this is identical to TrimSpace.
		secret := ""
		if f := strings.Fields(string(secretBytes)); len(f) > 0 {
			secret = f[0]
		}
		if m, mErr := webmailsso.New([]byte(secret), ""); mErr == nil {
			deps.WebmailSSOMinter = m
		} else {
			log.Warn("webmail SSO minter init failed", "err", mErr)
		}
	} else {
		log.Warn("bulwark-jwt-auth.secret unreadable; webmail SSO disabled", "err", sErr)
	}
	if sharedDB != nil {
		deps.DB = sharedDB
		userRepo := repository.NewUserRepository(sharedDB)
		packageRepo := repository.NewPackageRepository(sharedDB)
		domainRepo := repository.NewDomainRepository(sharedDB)
		dnsZoneRepo := repository.NewDNSZoneRepository(sharedDB)
		dnsRecordRepo := repository.NewDNSRecordRepository(sharedDB)
		sslCertRepo := repository.NewSSLCertificateRepository(sharedDB)
		mailRBLStateRepo := repository.NewMailRBLStateRepository(sharedDB)
		dmarcAggregateRepo := repository.NewDMARCAggregateRepository(sharedDB)
		tlsRptAggregateRepo := repository.NewTLSRPTAggregateRepository(sharedDB)
		arfReportRepo := repository.NewARFReportRepository(sharedDB)
		mailOutboundPolicyRepo := repository.NewMailOutboundPolicyRepository(sharedDB)
		databaseRepo := repository.NewDatabaseRepository(sharedDB)
		databaseUserRepo := repository.NewDatabaseUserRepository(sharedDB)
		databaseUserGrantRepo := repository.NewDatabaseUserGrantRepository(sharedDB)
		dbAdminRepo := repository.NewDBAdminRepository(sharedDB)
		mailboxRepo := repository.NewMailboxRepository(sharedDB)
		mailGroupRepo := repository.NewMailGroupRepository(sharedDB)
		mailboxSSOTokenRepo := repository.NewMailboxSSOTokenRepository(sharedDB)

		phpMyAdminSSOTokenRepo := repository.NewPhpMyAdminSSOTokenRepository(sharedDB)
		adminerSSOTokenRepo := repository.NewAdminerSSOTokenRepository(sharedDB)
		logAccessStreamRepo := repository.NewLogAccessStreamRepository(sharedDB)
		phpPoolRepo := repository.NewPHPPoolRepository(sharedDB)
		phpPerfModeRepo := repository.NewPHPPerformanceModeRepository(sharedDB)
		phpPoolIniOverrideRepo := repository.NewPHPPoolIniOverrideRepository(sharedDB)
		wordpressInstallRepo := repository.NewWordPressInstallRepository(sharedDB)
		cronJobsRepo := repository.NewCronJobRepository(sharedDB)
		dockerAppRepo := repository.NewDockerAppRepository(sharedDB)
		pythonAppRepo := repository.NewPythonAppRepository(sharedDB)
		// M48: load the docker-app catalog. Failures are logged + skipped,
		// not fatal -- the panel boots without M48 routes when the catalog
		// is unavailable.
		dockerCatalog, dockerCatalogErrs := dockerapp.LoadDir("/usr/local/share/jabali/docker-apps")
		if dockerCatalog.Len() == 0 {
			// Dev fallback: catalog might still be in the repo tree.
			if dc2, _ := dockerapp.LoadDir("install/docker-apps"); dc2.Len() > 0 {
				dockerCatalog = dc2
			}
		}
		for _, e := range dockerCatalogErrs {
			slog.Default().Warn("docker-app catalog entry failed to load", "err", e.Error())
		}
		// JAB-164: load the Python framework marketplace catalog (same
		// deployed-path + dev-fallback pattern as docker-apps). Non-fatal.
		pyCatalog, pyCatalogErrs := pyframeworks.LoadDir("/usr/local/share/jabali/py-frameworks")
		if pyCatalog.Len() == 0 {
			if pc2, _ := pyframeworks.LoadDir("install/py-frameworks"); pc2.Len() > 0 {
				pyCatalog = pc2
			}
		}
		for _, e := range pyCatalogErrs {
			slog.Default().Warn("py-framework catalog entry failed to load", "err", e.Error())
		}
		limitOverridesRepo := repository.NewUserLimitOverrideRepository(sharedDB)

		serverSettingsRepo := repository.NewServerSettingsRepository(sharedDB)
		pageTemplateRepo := repository.NewPageTemplateRepository(sharedDB)
		notificationEventSettingRepo := repository.NewNotificationEventSettingRepository(sharedDB)
		// M53 Updates Center singletons + history (ADR-0118).
		updateStateRepo := repository.NewUpdateStateRepository(sharedDB)
		updateHistoryRepo := repository.NewUpdateHistoryRepository(sharedDB)
		updateAutoupdateRepo := repository.NewUpdateAutoupdateConfigRepository(sharedDB)
		deps.UpdateState = updateStateRepo
		deps.UpdateHistory = updateHistoryRepo
		deps.UpdateAutoupdate = updateAutoupdateRepo

		// SSO service for phpMyAdmin
		ssoService := sso.NewService(
			sharedDB,
			userRepo,
			phpMyAdminSSOTokenRepo,
			sharedAgent,
			ssoKeyPtr,
			log,
		)
		deps.Users = userRepo
		deps.Packages = packageRepo
		deps.Domains = domainRepo
		deps.SSO = ssoService
		// M37 Phase 4: Adminer SSO bridge — engine-aware mint + PG shadow.
		deps.AdminerSSO = sso.NewAdminerService(ssoService, adminerSSOTokenRepo)
		deps.AdminerSSOTokens = adminerSSOTokenRepo

		deps.ServerSettings = serverSettingsRepo
		deps.PageTemplates = pageTemplateRepo
		deps.AccountSkeleton = repository.NewAccountSkeletonRepository(sharedDB)
		deps.NotificationEventSettings = notificationEventSettingRepo
		deps.DBAdmin = dbAdminRepo
		sshKeyRepo := repository.NewSSHKeyRepository(sharedDB)
		deps.SSHKeys = sshKeyRepo

		// M14 notification repos. Populated whenever sharedDB is up;
		// the admin API (later) and dispatcher goroutine both read them
		// from deps. Dispatcher only starts when cfg.Redis.URL resolved
		// and at least one ChannelSender is registered (Step 3 adds the
		// concrete senders — slack, ntfy, webhook, webpush, email).
		deps.NotificationChannels = repository.NewNotificationChannelRepository(sharedDB)
		deps.NotificationHistory = repository.NewNotificationHistoryRepository(sharedDB)
		deps.WebhookEndpoints = repository.NewWebhookEndpointRepository(sharedDB)
		deps.WebPushSubs = repository.NewWebPushSubscriptionRepository(sharedDB)
		if redisClient != nil {
			// NotificationQueue is the publish end. RegisterNotificationsInternalRoutes
			// takes this same pointer so the agent's notifications.send
			// command and in-process event sources (Step 4 below) write to
			// the identical Redis stream the dispatcher drains.
			deps.NotificationQueue = notifications.NewQueue(redisClient)

			// M49 — unified audit log (ADR-0106). Recorder is the one
			// write path (recorder middleware + domain emitters);
			// Consumer is the single-writer hash-chain sealer of
			// jabali:audit:queue. Same nil-without-Redis posture as
			// NotificationQueue. sharedDB/sharedLog are in scope here
			// (used by the repo wiring just above).
			auditRepo := repository.NewAuditEventRepository(sharedDB)
			aq := audit.NewAuditQueue(redisClient)
			deps.AuditRecorder = audit.NewRecorder(aq, auditRepo, sharedLog)
			deps.AuditConsumer = audit.NewConsumer(aq, auditRepo, sharedLog)
		}

		// Reconciler: database as source of truth, agent state as derived state.
		rec := reconciler.New(
			domainRepo,
			userRepo,
			sharedAgent,
			sharedLog,
			reconciler.Config{
				Interval: cfg.Agent.ReconcilerInterval,
				QueueLen: 100,
			},
		)
		// GH #233: register the disclaimer reconcile phase so the Stalwart
		// MTA hook + per-domain disclaimer state self-heal every tick. The
		// handler path alone left the hook unregistered after a fresh update
		// until the operator re-saved the disclaimer.
		phases.RegisterPhase(phases.NewDisclaimerPhase(sharedAgent))
		rec.WithDNSRepos(dnsZoneRepo, dnsRecordRepo, serverSettingsRepo)
		rec.WithSSLCerts(sslCertRepo)
		rec.WithPHPPools(phpPoolRepo)
		// GH #601/#616: the reconciler needs the installs repo to gather a
		// domain's cache-enabled install paths + URL exclusions for the nginx
		// gate. Was never wired — this also revives the WordPress install-health
		// reconcile pass (reconcileWordPressInstalls), which no-oped on nil.
		rec.WithWordPressInstalls(wordpressInstallRepo)
		rec.WithConfig(cfg)
		rec.WithSSO(ssoService)
		rec.WithSSHKeys(sshKeyRepo)
		rec.WithCronJobs(cronJobsRepo)
		rec.WithDockerApps(dockerAppRepo)
		rec.WithPythonApps(pythonAppRepo)
		rec.WithPyFrameworks(pyCatalog)
		rec.WithDockerCatalog(dockerCatalog)
		// M18 wiring — packages + overrides + /home mount path so
		// ReconcileUserLimits and ReconcileNginxRateLimits have every
		// dep they need. Mount path resolved below after deps are set.
		rec.WithPackages(packageRepo)
		rec.WithLimitOverrides(limitOverridesRepo)
		rec.WithDBAdmin(dbAdminRepo)
		managedIPRepo := repository.NewManagedIPRepository(sharedDB)
		rec.WithManagedIPs(managedIPRepo)
		rec.WithPageTemplates(pageTemplateRepo)
		rec.WithAccountSkeleton(deps.AccountSkeleton)
		// M30.2.x: backup destinations repo for the legacy
		// shared-password purge pass.
		rec.WithBackupDestinations(repository.NewBackupDestinationRepository(sharedDB))
		// M13.1.1: bandwidth quota auto-suspend. Wires bw_daily repo +
		// notifications queue so reconcileBandwidthQuotaEnforce can run.
		rec.WithBandwidthQuotaEnforce(repository.NewBWDailyRepository(sharedDB), deps.NotificationQueue)
		// M47 Wave 3 throttle reconcile — needs both repo + Stalwart CUD client.
		if sc, ok := deps.StalwartAdmin.(*stalwartadmin.Client); ok {
			rec.WithMailThrottles(mailOutboundPolicyRepo, sc)
		}
		// M52 (ADR-0133) — shared resources convergence (host principals +
		// per-collection shareWith). Grant grantees resolve via the mailbox +
		// mail-group repos.
		rec.WithSharedResources(repository.NewSharedResourceRepository(sharedDB), mailboxRepo, mailGroupRepo)
		deps.ManagedIPs = managedIPRepo
		deps.Reconciler = rec
		deps.DNSZones = dnsZoneRepo
		deps.DNSRecords = dnsRecordRepo
		deps.SSLCerts = sslCertRepo
		deps.MailRBLStates = mailRBLStateRepo
		deps.DMARCAggregate = dmarcAggregateRepo
		deps.TLSRPTAggregate = tlsRptAggregateRepo
		deps.ARFReports = arfReportRepo
		deps.MailOutboundPolicies = mailOutboundPolicyRepo
		// Same *stalwartadmin.Client satisfies the inline-delete dispatcher.
		if sc, ok := deps.StalwartAdmin.(*stalwartadmin.Client); ok {
			deps.StalwartAdminThrottle = sc
		}
		// M47 Wave 4/6/8 ingest — stalwart-cli subprocess client.
		// Auth via the same recovery-admin secret panel-agent uses.
		if stalwartUser, stalwartPass, ok := readStalwartAdminCreds(); ok {
			deps.StalwartAdmin = stalwartadmin.NewClient(stalwartUser, stalwartPass)
		}
		deps.BWDaily = repository.NewBWDailyRepository(sharedDB)
		deps.DomainIPACLs = repository.NewDomainIPACLRepository(sharedDB)
		deps.DomainDirectoryPrivacy = repository.NewDomainDirectoryPrivacyRepository(sharedDB)
		// M35: migration importers — Step 1 wires the repo only.
		// Steps 3-7 land per-source importer code that calls these
		// methods; admin REST + UI in Step 8. Default-off until
		// server_settings.migrations_enabled flips.
		deps.MigrationJobs = repository.NewMigrationJobRepository(sharedDB)
		deps.MigrationSizeCache = repository.NewMigrationAccountSizeCacheRepository(sharedDB)
		deps.AutomationTokens = repository.NewAutomationTokenRepository(sharedDB)
		deps.UserAPITokens = repository.NewUserAPITokenRepository(sharedDB)
		deps.Databases = databaseRepo
		deps.DatabaseUsers = databaseUserRepo
		deps.DatabaseUserGrants = databaseUserGrantRepo
		deps.Mailboxes = mailboxRepo
		deps.MailGroups = mailGroupRepo
		deps.MailboxSSOTokens = mailboxSSOTokenRepo
		// M6.5 repos. Each step's wave adds the concrete constructor as
		// it ships; Autoresponders ships in Step 3, Forwarders in Step 5,
		// MailboxShares in Step 4. Until each lands, nil + handler guards.
		deps.Autoresponders = repository.NewEmailAutoresponderRepository(sharedDB)
		deps.MailboxShares = repository.NewMailboxShareRepository(sharedDB)
		deps.Forwarders = repository.NewEmailForwarderRepository(sharedDB)
		deps.DNSSECKeys = repository.NewDNSSECKeyRepository(sharedDB)
		// M32 (ADR-0066): singleton panel_certificate repo + reconciler.
		// Without this wiring, /admin/panel-certificate routes are
		// skipped (RegisterAdminPanelCertificateRoutes returns early
		// when PanelCerts is nil) and the Server Settings → General
		// → Panel SSL card 404s with "Failed to load panel SSL state".
		// Surfaced 2026-04-26 on first VPS install at mx.jabali-panel.com.
		panelCertRepo := repository.NewPanelCertificateRepository(sharedDB)
		deps.PanelCerts = panelCertRepo
		rec.WithPanelCertificate(panelCertRepo, services.NewPanelCertRoutability())
		rec.WithUpdateRunHistory(updateHistoryRepo)
		rec.WithUpdateAutoupdate(updateAutoupdateRepo)
		rec.WithUpdateState(updateStateRepo)

		// M6.6 — per-domain mail TLS (ADR-0091). Reconciler walks the
		// mail_certificate table each tick and dispatches ssl.mail.issue
		// / renew through the agent. Deps.MailCerts also drives the REST
		// handler for opt-in / opt-out + status read.
		mailCertRepo := repository.NewMailCertificateRepository(sharedDB)
		rec.WithMailCertificates(mailCertRepo)
		deps.MailCerts = mailCertRepo

		// M33 (ADR-0072): malware detection repos. Five repos wired
		// together — RegisterSecurityMalwareRoutes activates only when
		// all five are non-nil. Idempotent EnsureDefault on first /settings
		// access seeds the singleton row if migration 000081 hasn't run.
		deps.MalwareQuarantine = repository.NewMalwareQuarantineRepository(sharedDB)
		deps.MalwareEvents = repository.NewMalwareEventRepository(sharedDB)
		deps.MalwareSettings = repository.NewMalwareSettingsRepository(sharedDB)
		deps.YARARules = repository.NewYARACustomRuleRepository(sharedDB)
		// M33.2 (ADR-0079): mail YARA scanner state + DLQ.
		deps.MailScanState = repository.NewMailScanStateRepository(sharedDB)
		deps.MailScanFailures = repository.NewMailScanFailureRepository(sharedDB)
		// M33 follow-up: per-user manual-scan job tracking (mig 000097).
		deps.MalwareUserScans = repository.NewMalwareUserScanRepository(sharedDB)
		// M41 (ADR-0088): Snuffleupagus PHP hardening — state singleton +
		// rule overrides + incidents. Reconciler renders active.rules from
		// DB → /etc/jabali/snuffleupagus/active.rules and triggers an FPM
		// pool graceful reload via the agent.
		snufRepo := repository.NewSnuffleupagusRepository(sharedDB)
		deps.Snuffleupagus = snufRepo
		deps.SnuffleupagusReconciler = &reconciler.SnuffleupagusReconciler{
			Repo:  snufRepo,
			Agent: sharedAgent,
		}
		// Boot-time render: if DB mode != off and active.rules SHA differs
		// from last_applied_sha256, the reconciler writes the bundle and
		// reloads FPM. SHA-pinned so a clean reboot is a no-op.
		go func(rec *reconciler.SnuffleupagusReconciler) {
			rctx, rcancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer rcancel()
			if err := rec.Reconcile(rctx); err != nil {
				log.Warn("snuffleupagus boot-reconcile failed", "err", err)
			}
		}(deps.SnuffleupagusReconciler)
		// GH #347: boot-time send-as reconcile. panel-api reconciles on every
		// delegation change, but a fresh install / DB restore / stalwart reset can
		// leave Stalwart's mustMatchSender expression out of sync with the
		// mailbox_send_delegations table. Re-derive it once at startup so the
		// expression always matches the DB (0 pairs -> stock "true"; this is the
		// install/update-safe path — it runs on every boot, install_sh_is_truth).
		if sharedDB != nil && sharedAgent != nil {
			go func() {
				rctx, rcancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer rcancel()
				pairs, err := repository.NewMailboxSendDelegationRepository(sharedDB).ListAllPairs(rctx)
				if err != nil {
					log.Warn("sendas boot-reconcile: list pairs failed", "err", err)
					return
				}
				if _, err := sharedAgent.Call(rctx, "mail.sendas.reconcile", map[string]any{"pairs": pairs}); err != nil {
					log.Warn("sendas boot-reconcile: agent push failed", "err", err)
				}
			}()
		}
		// Bundle dir resolution: prefer the on-disk install path
		// (set by `make install` / install.sh); fall back to the source
		// tree for dev. The route handler also defaults to the same
		// fallback if BundleDir is empty.
		if _, err := os.Stat("/usr/share/jabali/snuffleupagus/rules"); err == nil {
			deps.SnuffleupagusBundleDir = "/usr/share/jabali/snuffleupagus/rules"
		} else {
			deps.SnuffleupagusBundleDir = "/opt/jabali-panel/install/snuffleupagus/rules"
		}
		// M34: per-user PHP-FPM egress firewall (mig 000100/000101, ADR-0084).
		// Reconciler renders /etc/nftables.d/jabali-per-user-egress.nft from
		// these rows every tick and pulls per-user drop counters back into
		// drop_count_24h. Skipped on hosts without nft socket cgroupv2 by
		// the agent itself (apply errors out, reconciler logs + retries).
		deps.UserEgressPolicies = repository.NewUserEgressPolicyRepository(sharedDB)
		deps.UserEgressRequests = repository.NewUserEgressRequestRepository(sharedDB)
		deps.UserEgressDropSamples = repository.NewUserEgressDropSampleRepository(sharedDB)
		rec.WithUserEgressPolicies(deps.UserEgressPolicies)
		rec.WithUserEgressDropSamples(deps.UserEgressDropSamples)
		// M36: per-domain IP allow/deny ACLs. Reconciler threads ACLs into
		// agent's domain.create payload; agent renders nginx directives
		// inside the server block.
		rec.WithDomainIPACLs(deps.DomainIPACLs)
		rec.WithDomainDirectoryPrivacy(deps.DomainDirectoryPrivacy)
		// M30 (ADR-0075): backup-restore workflow rows.
		deps.BackupJobs = repository.NewBackupJobRepository(sharedDB)
		// M30.2 (ADR-0080): destinations + schedules. backup_copy_jobs
		// removed — per-destination model writes directly to remote.
		deps.BackupDestinations = repository.NewBackupDestinationRepository(sharedDB)
		deps.BackupSchedules = repository.NewBackupScheduleRepository(sharedDB)
		deps.PhpMyAdminSSOTokens = phpMyAdminSSOTokenRepo
		deps.LogAccessStreams = logAccessStreamRepo
		deps.TerminalSessions = repository.NewTerminalSessionRepository(sharedDB)
		deps.PHPPools = phpPoolRepo
		deps.PHPPerformanceModes = phpPerfModeRepo
		deps.PHPPoolIniOverrides = phpPoolIniOverrideRepo
		deps.WordPressInstalls = wordpressInstallRepo
		deps.CronJobs = cronJobsRepo
		deps.DockerApps = dockerAppRepo
		deps.PythonApps = pythonAppRepo
		deps.DockerCatalog = dockerCatalog
		deps.LimitOverrides = limitOverridesRepo

		// M18: resolve the /home mount once at startup. Passed to every
		// user.limits.{apply,clear,report} call so the agent runs
		// setquota against the explicit mount path, never -a. Failure
		// degrades gracefully — QuotaMount=="" disables the disk half
		// of the pipeline while cgroups enforcement still works.
		//
		// /home==/ is supported (matches cPanel/DA): install.sh enables
		// ext4 hidden quota inodes on / via tune2fs -O quota; the
		// reconciler only ever calls setquota for hosting UIDs (>=1000)
		// so root and system daemons stay unlimited regardless of which
		// filesystem holds the quota tables. Whether quota_mount is
		// honored at runtime is a separate question, gated by
		// server_settings.disk_quota_enabled (admin UI toggle).
		if m, err := limits.QuotaMountFor("/home"); err == nil {
			deps.QuotaMount = m
			rec.WithQuotaMount(m)
		} else if log != nil {
			log.Warn("m18: could not resolve /home mount; disk-quota plumbing disabled", "err", err)
		}

		// Admin bootstrap — atomic panel row + Kratos identity. Wait up to
		// 60s for Kratos to answer /health/ready first: install.sh starts
		// jabali-kratos immediately before jabali-panel, and the panel can
		// beat Kratos to binding :4434 on a slow boot. If Kratos never
		// answers, we bootstrap the panel DB row only and log a pointer
		// to `jabali kratos-migrate` so the operator can backfill once
		// Kratos is healthy — crashing the panel here would be worse.
		var bootstrapKratos auth.KratosIdentityWriter
		if cfg.Auth.Kratos.PublicURL != "" {
			// Build the client first so the readiness poll inherits the
			// configured transport (unix-socket dialer for M25 admin
			// endpoints). Reusing the bootstrap client also halves the
			// transport count in the steady state.
			candidate := kratosclient.NewClient(cfg.Auth.Kratos.PublicURL, cfg.Auth.Kratos.AdminURL)
			if waitForKratosReady(candidate, 60*time.Second, log) {
				bootstrapKratos = candidate
			} else {
				log.Warn("Kratos not ready after 60s — bootstrapping admin in panel DB only",
					"admin_url", cfg.Auth.Kratos.AdminURL,
					"action", "run 'jabali kratos-migrate' after Kratos is healthy")
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		res, err := auth.BootstrapAdmin(ctx, userRepo, auth.BootstrapOptions{
			Email:      os.Getenv("JABALI_BOOTSTRAP_ADMIN_EMAIL"),
			Password:   os.Getenv("JABALI_BOOTSTRAP_ADMIN_PASSWORD"),
			BcryptCost: bcrypt.DefaultCost,
			Kratos:     bootstrapKratos,
		})
		cancel()
		switch {
		case err != nil:
			return err
		case res.Created:
			log.Warn("admin user created via bootstrap",
				"kratos_identity_id", res.KratosIdentityID)
			// Welcome bell row — fired exactly once, on the boot that
			// creates the admin row. Written directly to
			// notification_history (channel_id=NULL, user_id=admin) so
			// it appears in the bell even on a fresh install with no
			// channels configured (the dispatcher would ack-and-drop
			// envelopes whose target list is empty). Best-effort: any
			// failure here should not block panel start.
			if deps.NotificationHistory != nil && res.UserID != "" {
				now := time.Now().UTC()
				row := &models.NotificationHistory{
					ID:        ids.NewULID(),
					EventKind: "panel.welcome",
					Severity:  models.NotificationSeverityInfo,
					Title:     "Welcome to Jabali Panel",
					Body:      "Set up notification channels (email, Slack, ntfy, webhook, web push) so you hear about cert renewals, disk pressure, and security events. Tap to open Notifications.",
					Deeplink:  "/jabali-admin/notifications/channels",
					Outcome:   models.NotificationOutcomeSent,
					UserID:    &res.UserID,
					CreatedAt: now,
					UpdatedAt: now,
				}
				wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
				if wErr := deps.NotificationHistory.Create(wctx, row); wErr != nil {
					log.Warn("welcome bell row insert failed", "err", wErr)
				}
				wcancel()
			}
		case res.ExistingID != "":
			log.Info("admin bootstrap: already exists",
				"user_id", res.ExistingID,
				"kratos_identity_id", res.KratosIdentityID)
		}

		// Tenant bootstrap (GH#120) — auto-create a non-admin user +
		// primary domain when JABALI_BOOTSTRAP_TENANT_EMAIL is set.
		// No-op when the env var is unset, when the user already
		// exists, or when the domain already exists. Failures log
		// at error level but don't abort panel startup; the operator
		// can re-run install.sh after fixing the upstream issue.
		bootstrapTenantFromEnv(log)

		// GH #231: seed the default Tier 1/2/3 hosting packages on a fresh
		// install. EnsureDefaults is a no-op once any package exists, so
		// upgrades and operator-curated package lists are untouched.
		func() {
			seedCtx, seedCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer seedCancel()
			if err := packageRepo.EnsureDefaults(seedCtx); err != nil {
				log.Error("failed to seed default hosting packages", "err", err)
			}
			if n, err := phpPerfModeRepo.EnsureDefaults(seedCtx); err != nil {
				log.Error("failed to seed PHP performance modes", "err", err)
			} else if n > 0 {
				log.Info("seeded PHP performance modes", "count", n)
			}
		}()

		// Merge-seed server_settings from config.toml [server] block on
		// every boot. Operator edits via the admin API win — once a field
		// has a non-empty value in the DB, config won't overwrite it. But
		// empty DB fields get filled from config whenever config has a
		// value. This way a partial earlier seed (e.g. from a boot where
		// config.toml was still broken) gets repaired the next time the
		// operator re-runs install.sh with a valid config.
		func() {
			seedCtx, seedCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer seedCancel()

			row, getErr := serverSettingsRepo.Get(seedCtx)
			created := false
			if errors.Is(getErr, repository.ErrNotFound) {
				row = &models.ServerSettings{ID: 1}
				created = true
			} else if getErr != nil {
				log.Error("server_settings probe failed", "err", getErr)
				return
			}

			// GH #634: re-apply the configured FastCGI cache capacity from
			// server_settings. install.sh copies the STATIC jabali-fastcgi-cache.conf
			// on every `jabali update`, reverting any operator capacity change; this
			// boot-time re-sync (idempotent — no-op if the conf already matches)
			// restores it. Skip on first boot (row has struct-zero fields).
			if !created && row != nil && row.NginxCacheMaxSizeGB > 0 && sharedAgent != nil {
				cctx, ccancel := context.WithTimeout(seedCtx, 30*time.Second)
				if _, cerr := sharedAgent.Call(cctx, "nginx.cache.capacity_apply", map[string]any{
					"max_size_gb":  row.NginxCacheMaxSizeGB,
					"keyzone_mb":   row.NginxCacheKeyzoneMB,
					"inactive_min": row.NginxCacheInactiveMin,
				}); cerr != nil {
					log.Warn("nginx cache capacity re-apply failed", "err", cerr)
				}
				ccancel()
			}

			mutated := created
			fillIfEmpty := func(target *string, source string) {
				if *target == "" && source != "" {
					*target = source
					mutated = true
				}
			}
			fillIfEmpty(&row.Hostname, cfg.Server.Hostname)
			fillIfEmpty(&row.PublicIPv4, cfg.Server.PublicIPv4)
			fillIfEmpty(&row.PublicIPv6, cfg.Server.PublicIPv6)
			fillIfEmpty(&row.NS1Name, cfg.Server.NS1Name)
			fillIfEmpty(&row.NS1IPv4, cfg.Server.NS1IPv4)
			fillIfEmpty(&row.NS2Name, cfg.Server.NS2Name)
			fillIfEmpty(&row.NS2IPv4, cfg.Server.NS2IPv4)
			// admin_email seeds from the bootstrap env var install.sh writes
			// to /etc/jabali/panel.env. Without this, fresh installs start
			// with admin_email="" which the SSL reconciler treats as
			// "ACME not configured" → every domain sits on self-signed
			// forever, retrying every 3h only to skip straight to fallback
			// again. Mirrors JABALI_BOOTSTRAP_ADMIN_{EMAIL,PASSWORD} already
			// read by auth.BootstrapAdmin.
			fillIfEmpty(&row.AdminEmail, os.Getenv("JABALI_BOOTSTRAP_ADMIN_EMAIL"))
			// M13: stamp the latest default sandbox image so a row left
			// behind by an older release (or one whose UI clobbered it
			// with an obsolete pin) self-heals on next boot. Migration
			// 000078 already bumps debian-12-v1 → debian-13-v1, but only
			// matches that exact value; this catches NULL/"" too.
			fillIfEmpty(&row.DefaultNspawnImageVersion, "debian-13-v1")

			if mutated {
				if err := serverSettingsRepo.Upsert(seedCtx, row); err != nil {
					log.Error("failed to seed server_settings from config", "err", err)
					return
				}
				if created {
					log.Info("seeded server_settings from config.toml", "hostname", row.Hostname)
				} else {
					log.Info("merged missing server_settings fields from config.toml", "hostname", row.Hostname)
				}
			}
			// IMPORTANT: subsequent seed steps (managed_ips, VAPID,
			// page_templates, notification_event_settings) MUST run
			// every boot regardless of `mutated`, otherwise new
			// rows added to canonical lists (e.g. new event kinds in
			// AllNotificationEventKinds) never get seeded on existing
			// hosts. Bug surfaced by M43 Step 2 — security.decision.fired
			// stayed unseeded for hours because server_settings was
			// already fully populated.

			// M24 first-boot seed: materialise the is_default managed_ips
			// row(s) from the freshly-populated server_settings. Migration
			// 000057 can't do this because it runs (via db.Migrate above)
			// BEFORE this seed goroutine executes — install.sh populates
			// server_settings via cfg, not a direct DB write. Keeping the
			// seed here means the default row always lives alongside the
			// server_settings row it mirrors, and neither migration 57
			// nor install.sh needs to know about the other.
			if managedIPRepo != nil {
				if err := managedIPRepo.EnsureDefault(seedCtx, row.PublicIPv4, "ipv4"); err != nil {
					log.Error("failed to seed default managed_ips ipv4 row", "err", err)
				}
				if err := managedIPRepo.EnsureDefault(seedCtx, row.PublicIPv6, "ipv6"); err != nil {
					log.Error("failed to seed default managed_ips ipv6 row", "err", err)
				}
			}

			// M14 first-boot seed: generate the installation-global
			// VAPID keypair if not yet present (ADR-0057). Reuses the
			// same seed goroutine as managed_ips because it has the
			// same ordering constraint — migration 000065 added the
			// columns but can't populate them (per
			// feedback_migration_data_seed_ordering); the keys exist
			// only after this runs. Non-fatal on error: the Web Push
			// sender will skip channels when the keypair is missing,
			// which surfaces as a clear log event rather than
			// crash-looping the whole panel.
			if generated, err := serverSettingsRepo.EnsureVAPID(seedCtx, row.Hostname); err != nil {
				log.Error("failed to seed VAPID keypair", "err", err)
			} else if generated {
				log.Info("generated VAPID keypair for Web Push", "subject_host", row.Hostname)
			}

			// M28 first-boot seed: populate page_templates with the
			// baked-in default bodies for keys operators can later
			// override (domain default index, error pages). Migration
			// 000068 only creates the table; rows live here per
			// feedback_migration_data_seed_ordering.
			if pageTemplateRepo != nil {
				if seeded, err := pageTemplateRepo.EnsureDefaults(seedCtx); err != nil {
					log.Error("failed to seed page_templates defaults", "err", err)
				} else if seeded > 0 {
					log.Info("seeded default page_templates", "count", seeded)
				}
			}

			// M53 first-boot seed: singleton update_state + auto-update
			// config rows. Migration 000163 creates the tables only; the
			// id=1 rows live here per feedback_migration_data_seed_ordering.
			if _, err := updateStateRepo.EnsureDefault(seedCtx); err != nil {
				log.Error("failed to seed update_state row", "err", err)
			}
			if _, err := updateAutoupdateRepo.EnsureDefault(seedCtx); err != nil {
				log.Error("failed to seed update_autoupdate_config row", "err", err)
			}

			// M14.1 first-boot seed: per-event-kind enable toggles.
			// Defaults defined in models.AllNotificationEventKinds —
			// "important = on" (cert renewal failures, expiry, disk
			// crit, service down, backup fail, channel auto-disabled),
			// rest off.
			if notificationEventSettingRepo != nil {
				if seeded, err := notificationEventSettingRepo.EnsureDefaults(seedCtx); err != nil {
					log.Error("failed to seed notification_event_settings defaults", "err", err)
				} else if seeded > 0 {
					log.Info("seeded default notification event toggles", "count", seeded)
				}
			}
		}()
	}

	// GH #322: build the notification sender registry (with the provisioned
	// notify-mailbox creds) before routes so the channels handler can send the
	// "Send test" synchronously and report the real delivery result.
	if deps.Redis != nil && deps.NotificationChannels != nil {
		deps.NotificationRegistry = buildNotificationRegistry(context.Background(), deps, log)
	}

	// ---- HTTP(S) ----
	handler := app.NewWithDeps(cfg, deps)

	// M25 Step 4: open the listener up front so we know whether we got a
	// TCP socket (TLS branch still applies) or a Unix socket (TLS is
	// stripped — nginx terminates real TLS upstream of us). Stale-socket
	// cleanup, chmod 0660, and chgrp jabali-sockets all happen inside
	// listenAndPrepare so the fragile bits live in one tested place.
	listener, isUnix, err := listenAndPrepare(cfg.Server.Addr)
	if err != nil {
		return err
	}
	useTLS := !isUnix && cfg.Server.TLSCert != "" && cfg.Server.TLSKey != ""
	if isUnix && (cfg.Server.TLSCert != "" || cfg.Server.TLSKey != "") {
		log.Warn("TLS configured but listening on Unix socket — TLS keys ignored",
			"addr", cfg.Server.Addr,
			"hint", "nginx terminates TLS; remove tls_cert/tls_key from config to silence this warning")
	}

	srv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Start reconciler background loop if it's configured.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure cancel is called on all exit paths
	if deps.Reconciler != nil {
		go deps.Reconciler.Start(ctx)
	}

	// Startup ssh-config reconcile (fix for the DB↔file desync scar).
	// system.set_ssh_config is normally only called when the operator
	// flips an SSH-section field in Server Settings AND the value
	// changed vs the DB row. If the on-disk
	// /etc/ssh/sshd_config.d/jabali-sftp.conf ever drifts from the DB
	// (panel restart killed the fire-and-forget goroutine before it
	// could write; hand-edit; agent rewrote with stale state during a
	// concurrent flow) re-toggling the UI is a NO-OP because the DB
	// matches the new value already → no diff → no agent call → file
	// stays stale forever. Self-heal once at boot: load the DB row,
	// dispatch system.set_ssh_config — idempotent + safe (the agent
	// runs `sshd -t` before reload and rolls back on failure).
	if deps.ServerSettings != nil && sharedAgent != nil {
		go func() {
			rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
			defer rcancel()
			st, gerr := deps.ServerSettings.Get(rctx)
			if gerr != nil || st == nil {
				return // boot-time, repo may not be ready; reconciler ticks pick it up later
			}
			if _, err := sharedAgent.Call(rctx, "system.set_ssh_config", map[string]any{
				"port":               st.SSHPort,
				"password_auth":      st.SSHPasswordAuth,
				"user_password_auth": st.SSHUserPasswordAuth,
			}); err != nil {
				log.Warn("startup ssh-config reconcile failed (file may be out of sync with DB)", "err", err)
			} else {
				log.Info("startup ssh-config reconcile ok",
					"port", st.SSHPort,
					"password_auth", st.SSHPasswordAuth,
					"user_password_auth", st.SSHUserPasswordAuth)
			}
		}()
	}
	// M49 — single-writer audit hash-chain consumer (ADR-0106).
	// Mirrors the reconciler: long-lived goroutine bound to ctx.
	if deps.AuditConsumer != nil {
		go deps.AuditConsumer.Start(ctx)
	}
	// M13.1: daily goaccess-driven bandwidth scan. Self-scoping —
	// no-op on hosts where any of agent / domain repo / bw repo
	// isn't wired (e.g. early-boot tests).
	if sharedAgent != nil && deps.Domains != nil && deps.BWDaily != nil {
		go reconciler.StartBandwidthTicker(ctx, sharedAgent, deps.Domains, deps.BWDaily, log)
	}

	// GH #259: scheduled registrar-expiry WHOIS fetch -> domains.registrar_expires_at.
	if sharedAgent != nil && deps.Domains != nil {
		go reconciler.StartDomainExpiryTicker(ctx, sharedAgent, deps.Domains, log)
	}

	// GH #263: mailbox usage sampler -> mailboxes.last_usage_bytes (the
	// mailbox.usage probe + UpdateUsage existed but were never wired).
	if sharedAgent != nil && deps.Mailboxes != nil {
		go reconciler.StartMailboxUsageTicker(ctx, sharedAgent, deps.Mailboxes, log)
	}

	// M30.2 (ADR-0080) backup scheduler + finalizer. Per-destination
	// model — copy worker removed (no source repo, no mirror).
	if sched := backupscheduler.New(backupscheduler.Deps{
		Schedules:      deps.BackupSchedules,
		Jobs:           deps.BackupJobs,
		Destinations:   deps.BackupDestinations,
		Users:          deps.Users,
		Databases:      deps.Databases,
		DatabaseUsers:  deps.DatabaseUsers,
		DatabaseGrants: deps.DatabaseUserGrants,
		Domains:        deps.Domains,
		Mailboxes:      deps.Mailboxes,
		AppInstalls:    deps.WordPressInstalls,
		Settings:       deps.ServerSettings,
		SSLCerts:       deps.SSLCerts,
		PHPPools:       deps.PHPPools,
		PHPPoolIni:     deps.PHPPoolIniOverrides,
		Forwarders:     deps.Forwarders,
		Autoresponders: deps.Autoresponders,
		MailboxShares:  deps.MailboxShares,
		DNSSECKeys:     deps.DNSSECKeys,
		DNSZones:       deps.DNSZones,
		DNSRecords:     deps.DNSRecords,
		SSHKeys:        deps.SSHKeys,
		CronJobs:       deps.CronJobs,
		LimitOverrides: deps.LimitOverrides,
		EgressPolicies: deps.UserEgressPolicies,
		EgressRequests: deps.UserEgressRequests,
		Agent:          deps.Agent,
		SSOKey:         deps.SSOKey,
		Log:            log,
	}); sched != nil {
		go sched.Start(ctx)
	} else {
		log.Info("backup scheduler not started — required deps missing")
	}
	if fin := backupfinalizer.New(backupfinalizer.Deps{
		Jobs:         deps.BackupJobs,
		Schedules:    deps.BackupSchedules,
		Destinations: deps.BackupDestinations,
		Agent:        deps.Agent,
		Log:          log,
	}); fin != nil {
		go fin.Start(ctx)
	} else {
		log.Info("backup finalizer not started — required deps missing")
	}

	// M33.2 (ADR-0079): mail YARA scanner tick. Off by default; the tick
	// short-circuits when malware_settings.mail_scan_enabled is false so
	// boot doesn't depend on Stalwart being up. Builds the in-process
	// ingest closure here (mailscan can't import api/ — would cycle).
	if deps.MailScanState != nil && deps.MailScanFailures != nil &&
		deps.MalwareSettings != nil && deps.MalwareEvents != nil &&
		deps.MalwareQuarantine != nil {
		ingestCfg := api.SecurityMalwareHandlerConfig{
			Quarantine: deps.MalwareQuarantine,
			Events:     deps.MalwareEvents,
			Settings:   deps.MalwareSettings,
			Users:      deps.Users,
			UserScans:  deps.MalwareUserScans,
			Log:        log,
		}
		go mailscan.StartTicker(ctx, mailscan.Deps{
			State:    deps.MailScanState,
			Failures: deps.MailScanFailures,
			Settings: deps.MalwareSettings,
			Log:      log,
			Ingest: func(ctx context.Context, h mailscan.IngestHit) error {
				p := &api.MalwareEventIngestPayload{
					Source:    h.Source,
					EventType: h.EventType,
					Severity:  h.Severity,
					Signature: h.Signature,
					RawJSON:   h.RawJSON,
					Hits: []api.MalwareEventIngestHit{{
						OriginalPath:   h.OriginalPath,
						QuarantinePath: h.QuarantinePath,
						Signature:      h.Signature,
						SHA256:         h.SHA256,
						SizeBytes:      h.SizeBytes,
						Username:       h.Username,
					}},
				}
				_, _, errCode := api.IngestMalwareEventInProcess(ctx, ingestCfg, p)
				if errCode != "" {
					return errors.New(errCode)
				}
				return nil
			},
		}, 5*time.Minute)
	} else {
		log.Info("mailscan ticker not started — required deps missing")
	}

	// M14 notification dispatcher. Starts iff we have Redis + all
	// notification repos + at least one ChannelSender registered.
	// Step 3 (senders) lands slack/ntfy/webhook/webpush/email — until
	// then the registry is empty and we skip startup with a loud log,
	// keeping the pipeline in an observable "inactive" state rather
	// than silently dropping events.
	dispatcherDone, dispatcherStop := startNotificationDispatcher(ctx, deps, log)

	// M14 Step 4 event sources. These are cheap goroutines that poll
	// system state (SSL certs, disk usage, systemd units, CrowdSec
	// decisions) and publish envelopes on the same Queue the internal
	// enqueue endpoint + agent use. All respect ctx.Done, so SIGTERM
	// stops them alongside the dispatcher.
	if deps.NotificationQueue != nil {
		eventsources.Start(ctx, eventsources.Deps{
			Queue:              deps.NotificationQueue,
			History:            deps.NotificationHistory,
			SSLCerts:           deps.SSLCerts,
			Log:                log,
			Users:              deps.Users,
			Agent:              deps.Agent,
			QuotaMount:         deps.QuotaMount,
			MalwareEvents:      deps.MalwareEvents,
			MalwareSettings:    deps.MalwareSettings,
			DomainsForGhost:    deps.Domains,
			ManagedIPsForGhost: deps.ManagedIPs,
			UserEgressPolicies: deps.UserEgressPolicies,
			ServerSettings:     deps.ServerSettings,
			Snuffleupagus:      deps.Snuffleupagus,
			MailRBLStates:      deps.MailRBLStates,
			StalwartAdmin:      deps.StalwartAdmin,
			DMARCAggregate:     deps.DMARCAggregate,
			TLSRPTAggregate:    deps.TLSRPTAggregate,
			ARFReports:         deps.ARFReports,
			BWDaily:            deps.BWDaily,
			Domains:            deps.Domains,
			Packages:           deps.Packages,
			BackupJobs:         deps.BackupJobs,
		})
	}

	var ssoUDSShutdown func(context.Context) error
	if ssoKeyPtr != nil && cfg.SSO.SocketPath != "" {
		var err error
		_, ssoUDSShutdown, err = app.StartSSOUDSListener(
			cfg.SSO.SocketPath,
			deps.Databases,
			deps.Users,
			deps.PhpMyAdminSSOTokens,
			deps.AdminerSSOTokens,
			deps.AdminerSSO,
			ssoKeyPtr,
			log,
		)
		if err != nil {
			return fmt.Errorf("start SSO UDS listener: %w", err)
		}
	}
	serveErr := make(chan error, 1)
	switch {
	case useTLS:
		log.Info("TLS enabled", "cert", cfg.Server.TLSCert, "key", cfg.Server.TLSKey, "addr", cfg.Server.Addr)
		go func() { serveErr <- srv.ServeTLS(listener, cfg.Server.TLSCert, cfg.Server.TLSKey) }()
		go startHTTPRedirect(cfg.Server.Addr, log)
	case isUnix:
		log.Info("listening on Unix socket — nginx terminates TLS upstream", "addr", cfg.Server.Addr)
		go func() { serveErr <- srv.Serve(listener) }()
	default:
		log.Warn("TLS not configured — serving plain HTTP", "addr", cfg.Server.Addr)
		go func() { serveErr <- srv.Serve(listener) }()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		case sig := <-stop:
			if sig == syscall.SIGHUP {
				// Hot-reload SSO key without restarting
				log.Info("SIGHUP received, reloading SSO key")
				newKey := loadSSOKey(cfg.SSO.KeyPath, log)
				if newKey != nil {
					*ssoKeyPtr = *newKey
					log.Info("SSO key reloaded successfully")
					deps.SSOKey = ssoKeyPtr
				}
				continue // Continue serving
			}
			// SIGINT or SIGTERM — shutdown
			log.Info("shutdown signal", "signal", sig.String())
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer shutdownCancel()
			// Stop the notification dispatcher before HTTP so new
			// publishes funnel through while in-flight XACKs drain.
			if dispatcherStop != nil {
				dispatcherStop()
			}
			if ssoUDSShutdown != nil {
				if err := ssoUDSShutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("UDS server shutdown error", "err", err)
				}
			}
			if err := srv.Shutdown(shutdownCtx); err != nil {
				return err
			}
			// Wait for the dispatcher's drain to finish (or the grace
			// window to close) — guarantees we don't exit while
			// XREADGROUP still holds entries in its PEL.
			if dispatcherDone != nil {
				select {
				case <-dispatcherDone:
				case <-shutdownCtx.Done():
				}
			}
			log.Info("jabali-panel stopped")
			return nil
		}
	}
}

// loadSSOKey attempts to load the SSO encryption key from disk.
// Returns nil if the key file is missing or an error occurs.
func loadSSOKey(keyPath string, log *slog.Logger) *ssokey.Key {
	if keyPath == "" {
		return nil
	}
	ssoKey, err := ssokey.Load(keyPath)
	if err == nil {
		log.Info("SSO key loaded", "path", keyPath)
		return &ssoKey
	}
	if errors.Is(err, ssokey.ErrKeyMissing) {
		log.Warn("SSO key not found (phpMyAdmin SSO disabled)", "path", keyPath)
		return nil
	}
	log.Error("failed to load SSO key", "path", keyPath, "err", err)
	return nil
}

// buildNotificationRegistry wires the concrete channel senders once at boot and
// provisions the notify mailbox so local-mode email can authenticate (GH #322).
// Shared by the dispatcher and the channels handler's synchronous test send.
func buildNotificationRegistry(parent context.Context, deps app.Deps, log *slog.Logger) *notifications.Registry {
	registry := notifications.NewRegistry()
	registry.Register(senders.NewSlack())
	registry.Register(senders.NewDiscord())
	registry.Register(senders.NewNtfy())
	registry.Register(senders.NewWebhook())
	registry.Register(senders.NewSMS())
	registry.Register(senders.NewTelegram())
	notifyEmail := provisionNotifyMailbox(parent, deps, log)
	registry.Register(senders.NewEmail("127.0.0.1:587").WithLocalCreds(notifyLocalCreds(deps, notifyEmail)))
	if deps.ServerSettings != nil && deps.WebPushSubs != nil {
		registry.Register(senders.NewWebPush(deps.ServerSettings, deps.WebPushSubs, log))
	} else {
		log.Warn("notifications: webpush sender skipped — server_settings or webpush_subscriptions repo missing")
	}
	return registry
}

// startNotificationDispatcher wires the M14 Redis Streams consumer. It
// returns a done channel that closes once Start() returns, and a stop
// function the shutdown path calls to cancel the dispatcher context.
// Both are nil when the dispatcher is not started (no Redis, no repos,
// or no registered ChannelSenders — the registry is empty until Step 3
// wires the concrete senders).
func startNotificationDispatcher(parent context.Context, deps app.Deps, log *slog.Logger) (done <-chan struct{}, stop func()) {
	if deps.Redis == nil {
		log.Warn("notifications dispatcher: not starting — Redis not configured")
		return nil, nil
	}
	if deps.NotificationChannels == nil || deps.NotificationHistory == nil || deps.WebhookEndpoints == nil {
		log.Warn("notifications dispatcher: not starting — notification repos missing")
		return nil, nil
	}
	registry := deps.NotificationRegistry
	if registry == nil {
		log.Warn("notifications dispatcher: not starting — sender registry not built")
		return nil, nil
	}
	queue := deps.NotificationQueue
	if queue == nil {
		queue = notifications.NewQueue(deps.Redis)
	}
	d, err := notifications.NewDispatcher(
		queue, registry,
		deps.NotificationChannels, deps.NotificationHistory, deps.WebhookEndpoints,
		log, notifications.Config{},
	)
	if err == nil && deps.NotificationEventSettings != nil {
		d.WithEventSettings(deps.NotificationEventSettings)
	}
	if err == nil && deps.Users != nil {
		d.WithUsers(deps.Users)
	}
	if err != nil {
		log.Error("notifications dispatcher: construction failed", "err", err)
		return nil, nil
	}
	dispatchCtx, dispatchCancel := context.WithCancel(parent)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		if err := d.Start(dispatchCtx); err != nil {
			log.Error("notifications dispatcher: start failed", "err", err)
		}
	}()
	return doneCh, dispatchCancel
}

func startHTTPRedirect(httpsAddr string, log interface{ Debug(string, ...any) }) {
	_, port, _ := net.SplitHostPort(httpsAddr)
	if port == "" {
		port = "8443"
	}
	redirect := &http.Server{
		Addr:              ":80",
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			target := "https://" + host
			if port != "443" {
				target += ":" + port
			}
			target += r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		}),
	}
	if err := redirect.ListenAndServe(); err != nil {
		log.Debug("HTTP→HTTPS redirect listener failed", "err", err)
	}
}

// readStalwartAdminCreds parses STALWART_RECOVERY_ADMIN from
// /etc/jabali-panel/stalwart.env (the same env file panel-agent's
// mail.* commands read). Format: STALWART_RECOVERY_ADMIN=user:secret.
// Returns ok=false when the file is missing or malformed — callers
// should leave StalwartAdmin nil so the ingest sources skip themselves.
func readStalwartAdminCreds() (user, password string, ok bool) {
	data, err := os.ReadFile("/etc/jabali-panel/stalwart.env")
	if err != nil {
		return "", "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		const k = "STALWART_RECOVERY_ADMIN="
		if !strings.HasPrefix(line, k) {
			continue
		}
		rest := strings.TrimPrefix(line, k)
		idx := strings.IndexByte(rest, ':')
		if idx <= 0 || idx == len(rest)-1 {
			return "", "", false
		}
		return rest[:idx], rest[idx+1:], true
	}
	return "", "", false
}
