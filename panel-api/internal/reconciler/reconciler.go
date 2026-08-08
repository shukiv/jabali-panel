package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/nginxrules"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/pyframeworks"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/reconciler/phases"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/redirects"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/services"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sso"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// Reconciler syncs the database state with the filesystem (nginx configs, php-fpm pools).
// The database is the source of truth; the reconciler makes the filesystem match.
type Reconciler struct {
	domains        repository.DomainRepository
	users          repository.UserRepository
	dnsZones       repository.DNSZoneRepository
	dnsRecords     repository.DNSRecordRepository
	sslCerts       repository.SSLCertificateRepository
	serverSettings repository.ServerSettingsRepository
	phpPools       repository.PHPPoolRepository
	sso            sso.SSOInterface
	cfg            *config.Config
	agent          agent.AgentInterface
	dbAdmin        repository.DBAdminRepository
	log            *slog.Logger
	interval       time.Duration

	// sslIssueMu serialises certbot-touching work across the JAB-205
	// domain worker pool AND the out-of-band ReconcileOne path: certbot
	// holds a global /var/lib/letsencrypt lock, so two concurrent runs
	// make the loser fail spuriously.
	sslIssueMu sync.Mutex

	// Preview-URL state cache (see preview_urls.go) — one settings +
	// shared-cert lookup per tick instead of per domain.
	previewMu    sync.Mutex
	previewCache previewState
	previewAt    time.Time
	// moduleInstall* back the M353 module-install convergence backoff so a
	// persistently-failing install is not re-dispatched every reconcile tick.
	moduleInstallMu      sync.Mutex
	moduleInstallAttempt map[string]time.Time
	// queue holds domain IDs to reconcile out-of-band (non-blocking enqueue)
	queue chan string
	// socketReady is a function that checks if a Unix socket is ready. Mockable for testing.
	socketReady func(ctx context.Context, socketPath string, timeout, pollInterval time.Duration) bool
	// paused is an atomic flag to pause reconciliation (for SSO key rotation)
	paused atomic.Bool

	// standby* back the DR standby gate (GH #331 Step 3). A standby's reconciler
	// stays DORMANT: it builds no serving config and issues no ACME certificate,
	// so the box serves no live traffic and never fails a challenge for the
	// primary's domains (which would burn Let's Encrypt rate limits). The role is
	// cached with a short TTL so runtime pairing/promotion takes effect within a
	// tick without a per-call DB read. serverSettings nil or a read error →
	// treated as primary (fail toward active), matching middleware.StandbyReadOnly.
	standbyMu      sync.Mutex
	standbyCached  bool
	standbyFetched time.Time
	// ssoTokens holds reference to the SSO token repository for nightly prune
	ssoTokens repository.PhpMyAdminSSOTokenRepository
	// mailboxes + sendmailSSOKey back the JAB-230 relay-credential loop
	// (sendmail_cred_reconcile.go). sendmailDone caches converged domains
	// (domain ID → input fingerprint) so steady-state ticks are map lookups.
	mailboxes      repository.MailboxRepository
	sendmailSSOKey *ssokey.Key
	sendmailMu     sync.Mutex
	sendmailDone   map[string]string
	// wordPressInstalls holds reference to the WordPress installs repository
	wordPressInstalls repository.WordPressInstallRepository
	// sshKeys holds reference to the SSH keys repository
	sshKeys repository.SSHKeyRepository
	// cronJobs holds reference to the cron jobs repository
	cronJobs repository.CronJobRepository
	// pythonApps holds the python_apps repository (ADR-0131).
	pythonApps repository.PythonAppRepository
	// pyFrameworks is the loaded JAB-164 framework marketplace catalog. When a
	// python_apps row carries a framework slug and hasn't been scaffolded, the
	// reconciler resolves the entry here and dispatches app.python.scaffold
	// before app.python.apply. Optional (nil = framework installs unavailable).
	pyFrameworks *pyframeworks.Catalog

	// dockerApps holds reference to the docker_apps repository (M48 Phase 1).
	// Nil-safe: when unwired the docker-app tick is a no-op.
	dockerApps repository.DockerAppRepository
	// dockerCatalog is the loaded M48 catalog. Used by the
	// image-update poller to resolve a docker_apps row to its
	// catalog entry's image_channel. Optional.
	dockerCatalog *dockerapp.Catalog
	// logrotateEnsured tracks docker apps whose host logrotate snippet the
	// reconciler has (re)asserted this panel-api lifetime (JAB-121 phase 2).
	// Cleared on process restart, so a `jabali update` re-converges every
	// installed app's snippet exactly once.
	logrotateEnsured sync.Map
	// logScanned tracks docker apps whose log dirs the reconciler has scanned
	// for unmanaged/oversized growth this panel-api lifetime (JAB-121 phase 3).
	logScanned sync.Map
	// sharedCerts backs the JAB-170 shared-cert expiry warning (optional/nil-safe).
	sharedCerts repository.SharedCertificateRepository
	// sharedCertExpiryChecked gates the expiry warn to once per panel-api boot.
	sharedCertExpiryChecked atomic.Bool
	// M18 — hosting packages + per-user overrides + /home mount path.
	packages       repository.PackageRepository
	limitOverrides repository.UserLimitOverrideRepository
	quotaMount     string
	// M24 — managed IPs pool. Optional; when nil, the managed-IP
	// rebind pass is skipped entirely (lets existing test harnesses
	// pass without touching the new repo).
	managedIPs repository.ManagedIPRepository
	// M30.2.x — backup destinations. Optional; reconciler purges the
	// legacy /etc/jabali-panel/restic-repo.password once every row
	// has been migrated to per-destination password_enc.
	backupDestinations repository.BackupDestinationRepository
	// M47 Wave 3 — outbound throttle reconcile.
	outboundPolicies repository.MailOutboundPolicyRepository
	stalwartAdmin    ThrottleStalwartClient
	// M52 (ADR-0133) — shared resources convergence. All three required for
	// reconcileSharedResources; nil on any disables the pass. srMailboxes +
	// srMailGroups resolve a grant's polymorphic grantee → target email(s).
	sharedResources repository.SharedResourceRepository
	srMailboxes     repository.MailboxRepository
	srMailGroups    repository.MailGroupRepository
	// M13.1.1 — bandwidth quota auto-suspend. Both required for the
	// reconcileBandwidthQuotaEnforce loop; nil on either disables the
	// feature regardless of server_settings toggle.
	bwDaily           repository.BWDailyRepository
	notificationQueue *notifications.Queue
	// M28 — operator-editable page templates. Used only to pipe the
	// domain_default_index body into the agent's domain.create call
	// so a fresh docroot gets the customised welcome page rather than
	// the agent's baked-in default.
	pageTemplates repository.PageTemplateRepository

	// accountSkeleton (GH #465) + a short cache so the full skeleton is read
	// from the DB at most ~once per reconcile pass, not once per domain.
	accountSkeleton repository.AccountSkeletonRepository
	skelMu          sync.Mutex
	skelWire        []map[string]any
	skelAt          time.Time
	// dkim2Applied caches, per mail domain, the DKIM2 signing state last
	// converged to the agent (GH #648) — see dkim2_reconcile.go.
	dkim2Mu      sync.Mutex
	dkim2Applied map[string]bool
	// errorPagesHash caches the last content hash synced to
	// /var/www/jabali-errors so the per-tick reconcile only calls the
	// agent when an admin actually edits an error template.
	errorPagesHash string
	// M32 — singleton panel-cert row. When nil the panel-cert hook
	// short-circuits (lab installs, tests). When wired with a routability
	// service it drives ssl.panel.issue from ReconcileAll.
	panelCerts           repository.PanelCertificateRepository
	panelCertRoutability *services.PanelCertRoutability

	// M53 Updates Center: update_history repo. When nil the run reconciler
	// is a no-op (test fixtures / installs without the M53 wiring).
	updateRunHistory repository.UpdateHistoryRepository
	// M53 Updates Center: auto-update desired-state repo. Nil disables the
	// autoupdate converge tick.
	updateAutoupdate repository.UpdateAutoupdateConfigRepository
	// M53.1 Updates Center: update_state repo + last poll time for the
	// periodic background check. Nil disables the poll tick.
	updateState    repository.UpdateStateRepository
	lastUpdatePoll time.Time
	// M6.6 — per-domain mail TLS. Nil = phase skipped.
	mailCerts repository.MailCertificateRepository
	// M34 — per-user PHP-FPM egress firewall. Renders
	// /etc/nftables.d/jabali-per-user-egress.nft from user_egress_policies
	// every tick, then reads + resets per-user counters into
	// drop_count_24h. Both nil = pass skipped (test fixtures, hosts
	// without nft socket cgroupv2 support).
	userEgressPolicies repository.UserEgressPolicyRepository
	// M34 deep stats — per-tick drop samples drive the 24h sparkline.
	// Optional; nil disables sample persistence (drop_count_24h still
	// updates on the policy row).
	userEgressDropSamples repository.UserEgressDropSampleRepository
	// M36 — per-domain IP allow/deny rules. Optional; when nil the
	// agent dispatch omits the ip_acls field (no nginx directives
	// rendered).
	domainIPACLs repository.DomainIPACLRepository
	// M50 per-directory password protection repo. When nil, agent
	// dispatch omits directory_privacy_rules → no htpasswd files
	// written, no auth_basic location blocks rendered.
	domainDirPrivacy repository.DomainDirectoryPrivacyRepository
	// sshKeysDispatchCache: per-user hash of last-applied SSH keys +
	// timestamp. Lets ReconcileSSHKeysForUser skip the agent IPC when
	// the desired state hasn't changed since the last dispatch. Self-
	// heals every sshKeysReDispatchInterval to catch drift even when
	// the hash matches. Keyed by user ID; value type
	// sshKeysDispatchState. sync.Map = lock-free for the common
	// "many readers, one writer per key" pattern.
	sshKeysDispatchCache sync.Map

	// dnsZoneDispatchCache: per-zone hash of the last-pushed record set +
	// timestamp, same shape and rationale as sshKeysDispatchCache. Without
	// it reconcileDNSZone rewrote the SOA serial, UPDATEd dns_zones, and
	// pushed a full zone to the agent for EVERY enabled domain every tick —
	// and the agent then DELETEs and re-INSERTs every record row in
	// PowerDNS's SQL backend and shells out three times (purge auth cache,
	// wipe recursor cache, NOTIFY slaves). Because the serial changed on
	// every pass, the payload could never converge, so no downstream gate
	// could ever fire. Keyed by zone ID; value type dnsZoneDispatchState.
	dnsZoneDispatchCache sync.Map
}

// WithPanelCertificate injects the M32 panel-cert repo + routability
// service. Wire both together — the reconciler skips the hook entirely
// when either is nil so existing test fixtures don't need new
// constructors.
// WithMailCertificates wires the M6.6 per-domain mail TLS repo.
// Nil = phase is a no-op (test fixtures).
func (r *Reconciler) WithMailCertificates(repo repository.MailCertificateRepository) *Reconciler {
	r.mailCerts = repo
	return r
}

func (r *Reconciler) WithSharedCerts(repo repository.SharedCertificateRepository) *Reconciler {
	r.sharedCerts = repo
	return r
}

// checkSharedCertExpiry (JAB-170 phase 7) warns once per boot about shared
// certs expiring within 14 days or already expired — one expired shared cert
// takes down EVERY attached domain, so the alert must be loud + early.
func (r *Reconciler) checkSharedCertExpiry(ctx context.Context) {
	if r.sharedCerts == nil {
		return
	}
	if r.sharedCertExpiryChecked.Swap(true) {
		return
	}
	certs, err := r.sharedCerts.ListExpiring(ctx, time.Now().Add(14*24*time.Hour))
	if err != nil {
		r.sharedCertExpiryChecked.Store(false) // retry next pass
		return
	}
	now := time.Now()
	for i := range certs {
		n, _ := r.sharedCerts.CountAttachedDomains(ctx, certs[i].ID)
		exp := ""
		if certs[i].ExpiresAt != nil {
			exp = certs[i].ExpiresAt.UTC().Format(time.RFC3339)
		}
		if certs[i].ExpiresAt != nil && certs[i].ExpiresAt.Before(now) {
			r.log.Warn("shared cert EXPIRED — every attached domain is serving an expired cert; replace it now (one re-upload renews them all)",
				"id", certs[i].ID, "name", certs[i].Name, "expires_at", exp, "attached_domains", n)
		} else {
			r.log.Warn("shared cert expiring within 14 days — replace it (one re-upload renews all attached domains)",
				"id", certs[i].ID, "name", certs[i].Name, "expires_at", exp, "attached_domains", n)
		}
	}
}

func (r *Reconciler) WithPanelCertificate(repo repository.PanelCertificateRepository, rout *services.PanelCertRoutability) *Reconciler {
	r.panelCerts = repo
	r.panelCertRoutability = rout
	return r
}

// WithUpdateRunHistory injects the M53 update_history repo so the run
// reconciler can flip running rows to their terminal status (ADR-0118).
func (r *Reconciler) WithUpdateRunHistory(repo repository.UpdateHistoryRepository) *Reconciler {
	r.updateRunHistory = repo
	return r
}

// WithUpdateAutoupdate injects the M53 auto-update config repo so the
// autoupdate reconciler converges it onto the host (ADR-0118).
func (r *Reconciler) WithUpdateAutoupdate(repo repository.UpdateAutoupdateConfigRepository) *Reconciler {
	r.updateAutoupdate = repo
	return r
}

// WithUpdateState injects the M53.1 update_state repo so the periodic poll
// tick can refresh the Updates page snapshot + notify on new security updates.
func (r *Reconciler) WithUpdateState(repo repository.UpdateStateRepository) *Reconciler {
	r.updateState = repo
	return r
}

// WithUserEgressPolicies injects the M34 per-user egress repo. When nil
// (the default in test fixtures) the egress reconciler pass is skipped
// entirely — no agent calls, no nft writes.
func (r *Reconciler) WithUserEgressPolicies(repo repository.UserEgressPolicyRepository) *Reconciler {
	r.userEgressPolicies = repo
	return r
}

// WithUserEgressDropSamples injects the M34 deep-stats sample repo.
// When set, the egress counter-read tick persists per-user drop deltas
// into user_egress_drop_samples. Pruned to last 25h every tick.
func (r *Reconciler) WithUserEgressDropSamples(repo repository.UserEgressDropSampleRepository) *Reconciler {
	r.userEgressDropSamples = repo
	return r
}

// WithDomainIPACLs injects the M36 per-domain ACL repo. When set,
// createDomainOnAgent fetches the domain's allow/deny rules and
// includes them in the agent's domain.create payload so nginx
// renders the directives inside the server block.
func (r *Reconciler) WithDomainIPACLs(repo repository.DomainIPACLRepository) *Reconciler {
	r.domainIPACLs = repo
	return r
}

// WithDomainDirectoryPrivacy injects the M50 per-directory password
// protection repo. When set, createDomainOnAgent fetches each domain's
// rules + credentials so the agent writes htpasswd files and the vhost
// gets auth_basic location blocks.
func (r *Reconciler) WithDomainDirectoryPrivacy(repo repository.DomainDirectoryPrivacyRepository) *Reconciler {
	r.domainDirPrivacy = repo
	return r
}

// WithPageTemplates injects the M28 page template repo. When nil (the
// default in tests), domain.create params don't include the body and
// the agent falls back to its compiled-in template.
func (r *Reconciler) WithPageTemplates(repo repository.PageTemplateRepository) *Reconciler {
	r.pageTemplates = repo
	return r
}

// WithAccountSkeleton injects the GH #465 docroot-skeleton repo. Nil (tests)
// means domain.create carries no skeleton and the agent lays only the default
// index.
func (r *Reconciler) WithAccountSkeleton(repo repository.AccountSkeletonRepository) *Reconciler {
	r.accountSkeleton = repo
	return r
}

// skeletonWire returns the skeleton files as the domain.create wire shape,
// cached ~30s so a busy pass reads the (potentially large) blob once. The
// agent no-clobbers, so re-sending an unchanged skeleton is harmless.
func (r *Reconciler) skeletonWire(ctx context.Context) []map[string]any {
	if r.accountSkeleton == nil {
		return nil
	}
	r.skelMu.Lock()
	defer r.skelMu.Unlock()
	if !r.skelAt.IsZero() && time.Since(r.skelAt) < 30*time.Second {
		return r.skelWire
	}
	files, err := r.accountSkeleton.List(ctx)
	if err != nil {
		return r.skelWire // keep the last good set on a transient error
	}
	wire := make([]map[string]any, 0, len(files))
	for _, f := range files {
		wire = append(wire, map[string]any{"rel_path": f.RelPath, "content": f.Content})
	}
	r.skelWire, r.skelAt = wire, time.Now()
	return wire
}

// WithMailThrottles wires the M47 Wave 3 outbound-throttle reconciler.
// Both args are required — nil disables the pass entirely.
func (r *Reconciler) WithMailThrottles(repo repository.MailOutboundPolicyRepository, sc ThrottleStalwartClient) *Reconciler {
	r.outboundPolicies = repo
	r.stalwartAdmin = sc
	return r
}

// WithSSLCerts adds SSL certificate repository support to the reconciler.
// Call this before using SSL certificate reconciliation.
func (r *Reconciler) WithSSLCerts(sslCerts repository.SSLCertificateRepository) *Reconciler {
	r.sslCerts = sslCerts
	return r
}

// WithPHPPools adds PHP pool repository support to the reconciler.
// Call this before using PHP pool reconciliation.
func (r *Reconciler) WithPHPPools(phpPools repository.PHPPoolRepository) *Reconciler {
	r.phpPools = phpPools
	return r
}

// WithSSO injects the SSO service for mysqladmin shadow account backfill.
// Call this before using mysqladmin reconciliation.
func (r *Reconciler) WithSSO(sso sso.SSOInterface) *Reconciler {
	r.sso = sso
	return r
}

// WithConfig injects the application config so the reconciler can read
// runtime flags (e.g. cfg.ACME.StagingOnly) during SSL convergence.
func (r *Reconciler) WithConfig(cfg *config.Config) *Reconciler {
	r.cfg = cfg
	return r
}

// Config bundles reconciler configuration.
type Config struct {
	Interval time.Duration
	QueueLen int
}

// New creates a new Reconciler.
func New(domains repository.DomainRepository, users repository.UserRepository, agentClient agent.AgentInterface, log *slog.Logger, cfg Config) *Reconciler {
	if cfg.Interval <= 0 {
		cfg.Interval = 60 * time.Second
	}
	if cfg.QueueLen <= 0 {
		cfg.QueueLen = 100
	}
	r := &Reconciler{
		domains:  domains,
		users:    users,
		agent:    agentClient,
		log:      log,
		interval: cfg.Interval,
		queue:    make(chan string, cfg.QueueLen),
	}
	// Initialize default socketReady function
	r.socketReady = r.waitSocketReady
	return r
}

// WithDNSRepos adds DNS repository support to the reconciler.
// Call this before using ReconcileDNSZone.
func (r *Reconciler) WithDNSRepos(dnsZones repository.DNSZoneRepository, dnsRecords repository.DNSRecordRepository, serverSettings repository.ServerSettingsRepository) *Reconciler {
	r.dnsZones = dnsZones
	r.dnsRecords = dnsRecords
	r.serverSettings = serverSettings
	return r
}

// WithManagedIPs registers the M24 managed_ips repo. When set, each
// ReconcileAll pass runs a managed-IP rebind sweep: rows flagged
// is_bound=TRUE whose address is missing from the kernel get an
// ip.bind; rows that exceed their retry budget flip to degraded=TRUE.
func (r *Reconciler) WithManagedIPs(repo repository.ManagedIPRepository) *Reconciler {
	r.managedIPs = repo
	return r
}

// WithSSOTokens injects the SSO token repository for nightly prune.
func (r *Reconciler) WithSSOTokens(ssoTokens repository.PhpMyAdminSSOTokenRepository) *Reconciler {
	r.ssoTokens = ssoTokens
	return r
}

// WithBackupDestinations registers the M30.2 destinations repo. When
// set, ReconcileAll runs reconcileResticLegacyPassword every pass
// to purge /etc/jabali-panel/restic-repo.password once every
// destination has its own per-row sealed password.
func (r *Reconciler) WithBackupDestinations(repo repository.BackupDestinationRepository) *Reconciler {
	r.backupDestinations = repo
	return r
}

// WithBandwidthQuotaEnforce wires M13.1.1 quota-driven domain
// suspension. Both bwDaily + notificationQueue required; nil on
// either disables the loop entirely.
func (r *Reconciler) WithBandwidthQuotaEnforce(bw repository.BWDailyRepository, q *notifications.Queue) *Reconciler {
	r.bwDaily = bw
	r.notificationQueue = q
	return r
}

// WithWordPressInstalls adds WordPress installs repository support to the reconciler.
// Call this before using WordPress installs reconciliation.
func (r *Reconciler) WithWordPressInstalls(wp repository.WordPressInstallRepository) *Reconciler {
	r.wordPressInstalls = wp
	return r
}

// WithSSHKeys adds SSH key repository support to the reconciler.
// Call this before using SSH key reconciliation.
func (r *Reconciler) WithSSHKeys(sshKeys repository.SSHKeyRepository) *Reconciler {
	r.sshKeys = sshKeys
	return r
}

// WithCronJobs adds cron jobs repository support to the reconciler.
// Call this before using cron jobs reconciliation.
func (r *Reconciler) WithCronJobs(cronJobs repository.CronJobRepository) *Reconciler {
	r.cronJobs = cronJobs
	return r
}

// Pause stops the reconciler from running its main loop. Used for SSO key rotation.
func (r *Reconciler) Pause() {
	r.paused.Store(true)
	r.log.Info("reconciler paused")
}

// Resume resumes the reconciler after a pause.
func (r *Reconciler) Resume() {
	r.paused.Store(false)
	r.log.Info("reconciler resumed")
}

// IsPaused returns true if the reconciler is paused.
func (r *Reconciler) IsPaused() bool {
	return r.paused.Load()
}

// isStandby reports whether this box is a DR standby (GH #331 Step 3). A
// standby's automatic reconcile loops early-return so it builds no serving
// config and issues no ACME certificate. Cached with a short TTL so promotion
// (role → primary) takes effect within a tick; a nil repo or a read error is
// treated as primary so a DB blip never wrongly parks a live box. This gates the
// AUTOMATIC loops only — the explicit ReconcileAllForce path (used by promote)
// stays ungated so it always converges regardless of the cached role.
func (r *Reconciler) isStandby(ctx context.Context) bool {
	if r.serverSettings == nil {
		return false
	}
	const ttl = 5 * time.Second
	r.standbyMu.Lock()
	defer r.standbyMu.Unlock()
	if !r.standbyFetched.IsZero() && time.Since(r.standbyFetched) < ttl {
		return r.standbyCached
	}
	s, err := r.serverSettings.Get(ctx)
	if err != nil || s == nil {
		return r.standbyCached // keep last-known; fail toward active primary
	}
	r.standbyCached = s.IsStandby()
	r.standbyFetched = time.Now()
	return r.standbyCached
}

// Start blocks until ctx is cancelled, running ReconcileAll every interval
// and draining the out-of-band queue. Must be called once per process.
func (r *Reconciler) Start(ctx context.Context) {
	r.log.Info("reconciler starting", "interval", r.interval)

	// Run once at startup to converge any stale state
	if err := r.ReconcileAll(ctx); err != nil {
		r.log.Error("initial reconcile failed", "err", err)
	}

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// SSL retry ticker: process pending ACME retries every 1 minute
	sslRetryTicker := time.NewTicker(1 * time.Minute)
	defer sslRetryTicker.Stop()

	// SSO token prune ticker: clean up expired tokens every 5 minutes
	// JAB-203: reconcile the panel's view of each certificate with the file on
	// disk. Hourly is ample — certbot's timer runs twice a day and a cert lives
	// 90 days — and it keeps a freshly-renewed cert from showing stale expiry in
	// the UI for long.
	sslObserveTicker := time.NewTicker(1 * time.Hour)
	defer sslObserveTicker.Stop()
	pruneTicker := time.NewTicker(5 * time.Minute)
	defer pruneTicker.Stop()

	// Update-run finish ticker: poll running `jabali update` / apt transient
	// units every 5s so a finished run's update_history row flips to
	// success/failed promptly. ReconcileAll already calls reconcileUpdateRuns,
	// but its 60s cadence left the top-bar Tasks indicator spinning for up to
	// a minute after the update actually finished. Cheap: ListRunning is a
	// no-op when idle, and the agent status call only fires for a running row.
	updateRunTicker := time.NewTicker(5 * time.Second)
	defer updateRunTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("reconciler stopping")
			return
		case domainID := <-r.queue:
			if r.IsPaused() {
				r.log.Debug("reconcile one skipped (paused)", "domain_id", domainID)
				continue
			}
			if err := r.ReconcileOne(ctx, domainID); err != nil {
				r.log.Error("reconcile one failed", "domain_id", domainID, "err", err)
			}
		case <-ticker.C:
			if r.IsPaused() {
				r.log.Debug("periodic reconcile skipped (paused)")
				continue
			}
			if err := r.ReconcileAll(ctx); err != nil {
				r.log.Error("periodic reconcile failed", "err", err)
			}
		case <-sslRetryTicker.C:
			if r.IsPaused() {
				r.log.Debug("ssl retry skipped (paused)")
				continue
			}
			r.RetrySSLDueForACME(ctx)
		case <-sslObserveTicker.C:
			if r.IsPaused() {
				continue
			}
			r.ReconcileSSLObservation(ctx)
			// JAB-224: same cadence — an exhausted certificate on an
			// SSL-enabled domain is re-armed so cutover is picked up without
			// operator action. Rate-limited inside the pass.
			r.ReconcileSSLResurrect(ctx)
		case <-updateRunTicker.C:
			if r.IsPaused() {
				continue
			}
			r.reconcileUpdateRuns(ctx)
		case <-pruneTicker.C:
			if r.ssoTokens != nil {
				if count, err := r.ssoTokens.PurgeExpired(ctx); err != nil {
					r.log.Error("sso token prune failed", "err", err)
				} else {
					r.log.Debug("sso tokens purged", "count", count)
				}
			}
		}
	}
}

// Schedule enqueues a domain ID for out-of-band reconciliation. Non-blocking;
// drops the request if the queue is full.
func (r *Reconciler) Schedule(domainID string) {
	select {
	case r.queue <- domainID:
	default:
		r.log.Warn("reconcile queue full, dropping request", "domain_id", domainID)
	}
}

// ReconcileAll diffs the DB against the agent's filesystem state and converges them.
// Returns an error if the agent list call fails; on per-domain errors, logs and continues.
// domainLoopWorkers bounds the JAB-205 per-domain convergence pool.
// Deliberately modest — the goal is that one slow domain (ACME retry,
// wedged agent call) no longer stalls the whole fleet, not maximum
// parallelism. certbot work is additionally serialised on sslIssueMu.
const domainLoopWorkers = 4

func (r *Reconciler) ReconcileAll(ctx context.Context) error {
	// GH #331 Step 3: a DR standby stays dormant — no serving config, no ACME.
	// The whole convergence loop below (panel/mail/shared certs, per-domain
	// SSL + nginx vhosts, DNS zone push, module installs) is suppressed so the
	// box serves nothing until `jabali dr promote`. Config is (re)built from the
	// replicated DB at promote time via ReconcileAllForce.
	if r.isStandby(ctx) {
		r.log.Debug("reconcile all skipped — DR standby (not serving)")
		return nil
	}
	// Coarse per-block timings. A tick that outruns the interval means
	// the next ticker fire lands immediately behind it and drift repair
	// degrades to back-to-back passes — worth a WARN that names the
	// slow block, not just a vague "panel feels slow" report later.
	tt := newTickTimings()
	defer func() {
		total := tt.total()
		if total > r.interval {
			r.log.Warn("reconcile tick overran interval", "took", total.Round(time.Millisecond), "interval", r.interval, "slowest", tt.summary())
		} else {
			r.log.Debug("reconcile tick complete", "took", total.Round(time.Millisecond), "slowest", tt.summary())
		}
	}()

	// M32: panel-cert hook runs early so a successful issue lands
	// before the rest of the loop touches the agent. Cheap noop when
	// use_le=0 or routability gate fails.
	r.reconcilePanelCertificate(ctx)
	r.reconcileUpdateRuns(ctx)
	r.reconcileAutoupdate(ctx)

	// M353: install optional modules whose flag reads On but whose packages/
	// services aren't actually present (the reported --minimal "DNS On / pdns
	// inactive" state). Quick status probe per enabled module; a detached,
	// backoff-gated install for any that isn't installed+active.
	r.reconcileModuleInstalls(ctx)
	r.reconcileUpdatePoll(ctx)

	// Converge the shared branded error pages (404/403/500) from the
	// editable page_template rows into /var/www/jabali-errors. Hash-gated:
	// a noop on every tick until an admin edits a template.
	r.reconcileErrorPages(ctx)

	// GH #648: converge DKIM2 signing across mail domains to the
	// server_settings toggle. Applied-state-gated — noop steady state.
	r.reconcileDKIM2(ctx)

	// JAB-230: every domain gets a noreply@ SendOnly relay identity + a
	// cred file for the jabali-sendmail shim. Fingerprint-gated noop in
	// steady state; doubles as the fleet backfill after `jabali update`.
	r.reconcileSendmailCreds(ctx)

	tt.mark("certs_updates_modules")

	// M6.6 — per-domain mail TLS. Sits next to panel-cert so the
	// two cert flows share the same admin email/public IP context.
	r.reconcileMailCertificates(ctx)
	r.reconcileSharedResources(ctx)

	// M34: per-user PHP-FPM egress firewall. Cheap noop when the repo
	// isn't wired (test fixtures) or when there are zero policies.
	r.reconcileUserEgress(ctx)
	// JAB-195: keep this host's own public IPs allowlisted so AppSec never
	// scores WordPress loopback traffic (WooCommerce Action Scheduler et al).
	r.reconcileSelfAllowlist(ctx)

	tt.mark("mail_shared_egress")

	// PHP pool reconciliation first, so domain regens see latest pool state.
	r.ReconcilePHPPools(ctx)
	// GH #329: converge the per-domain versioned pools (apply pending, reap
	// orphans). No-op when no user has a non-default pool.
	r.reconcileVersionedPHPPools(ctx)

	// GH #686: garbage-collect FPM pools orphaned by a deleted user. user.delete
	// tears them down inline; this backstop self-heals pools orphaned before that
	// fix (or by a failed teardown). Bounded + guarded agent-side.
	r.reconcileOrphanFPMPools(ctx)

	tt.mark("php_pools")

	// M24: managed IPs BEFORE the domain loop. If a domain is bound to a
	// secondary IP that fell off the kernel (host reboot, netplan drop),
	// re-bind it first so the vhost render later in the loop can find
	// the address live when nginx parses the config.
	r.ReconcileManagedIPs(ctx)

	// Backfill mysqladmin shadow accounts for users that don't have one yet.
	// This is a separate pass that doesn't block domain reconciliation.
	r.reconcileMysqlAdminShadow(ctx)

	// M30.2: once every backup_destinations row has a per-destination
	// restic password, the legacy shared file at
	// /etc/jabali-panel/restic-repo.password is vestigial. Purge it
	// so an operator who rotates and walks away doesn't leave the
	// shared key on disk.
	r.reconcileResticLegacyPassword(ctx)

	// M13.1.1 bandwidth-quota auto-suspend (opt-in via
	// server_settings.bandwidth_quota_enforce_enabled). Walks users
	// with package quota > 0, sums month-to-date bytes, suspends
	// (or restores) their domains. Cheap noop when toggle is off.
	r.reconcileBandwidthQuotaEnforce(ctx)

	// M18 rate-limit zone fragment MUST converge BEFORE the domain loop:
	// domain.create on the agent writes each vhost then runs `nginx -t`.
	// If a vhost references `limit_req zone=rl_<id>` but the zone hasn't
	// been declared in 00-jabali-ratelimits.conf yet, `nginx -t` fails with
	// "zero size shared memory zone" (actually "unknown zone" but the
	// symptom is the same) and domain.create aborts — the domain never
	// lands. Running this first makes the zone declaration visible before
	// any vhost that references it is (re-)written. The post-loop call
	// further down handles the reverse transition (rate_limit_rps → 0:
	// vhost stops referencing first, then the fragment drops the zone).
	r.ReconcileNginxRateLimits(ctx)

	tt.mark("ips_quota_ratelimits")

	// Get the list of enabled sites from the agent
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := r.agent.Call(agentCtx, "domain.list", nil)
	if err != nil {
		return fmt.Errorf("agent list failed: %w", err)
	}

	var resp struct {
		Sites []string `json:"sites"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return fmt.Errorf("failed to parse agent response: %w", err)
	}

	agentSites := make(map[string]bool)
	for _, site := range resp.Sites {
		agentSites[site] = true
	}

	// Fetch all domains from DB. Repository.List returns (domains, total, err).
	allDomains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		return fmt.Errorf("failed to list domains: %w", err)
	}

	enabledDomains := make(map[string]*models.Domain)
	disabledDomains := make(map[string]*models.Domain)

	for i := range allDomains {
		d := &allDomains[i]
		if d.IsEnabled {
			enabledDomains[d.Name] = d
		} else {
			disabledDomains[d.Name] = d
		}
	}

	// Preview URLs: converge the shared wildcard DNS record + cert row
	// before the domain loop, so a freshly-enabled preview host resolves
	// (and its vhost can reference the cert) within this same tick.
	r.reconcilePreviewInfra(ctx, enabledDomains)

	// M6.3: make sure the panel's self-zone is forwardable through the
	// local recursor. The zone is bootstrapped by install.sh (not a DB
	// row), so it's not covered by the enabledDomains loop below.
	// Idempotent on the agent side.
	r.reconcileRecursorSelfZone(ctx)

	tt.mark("list_state")

	// Convergence:
	// 1. Every enabled DB domain gets a domain.create every pass. The
	// agent's writeVhost is content-hash gated — it re-renders the vhost,
	// compares to what's on disk, and only reloads nginx when the bytes
	// differ. This makes rebinding a domain (e.g. switching its PHP pool)
	// automatically propagate on the next tick without needing an
	// explicit "force" endpoint, and the cost in the unchanged case is
	// one agent RPC per domain per minute (no nginx reload, no SSL IO).
	// Previously we only called this when the domain was missing from
	// the agent set, which silently stalled binding changes for existing
	// domains. Logged only when the domain is newly-added or disabled so
	// the steady-state reconcile stays quiet.
	// JAB-205: converge enabled domains through a bounded worker pool
	// instead of strictly serially. Safety analysis (see the PR):
	//   - the agent serialises every vhost write + nginx -t + reload
	//     under its global nginxOpMu, so concurrent domain.create calls
	//     cannot interleave a broken config into a reload;
	//   - the panel's agent client dials a fresh socket connection per
	//     call — no shared-connection multiplexing to corrupt;
	//   - certbot cannot run concurrently (shared /var/lib/letsencrypt
	//     lock) — reconcileSSLForDomain serialises itself on sslIssueMu,
	//     which also closes the pre-existing ReconcileOne-vs-loop race;
	//   - everything else in the per-domain path (DNS rows + per-zone
	//     upserts, Stalwart HTTP, PHP binding reads) is per-domain or
	//     transactional. ReconcileOne already ran concurrently with this
	//     loop via the out-of-band queue, so per-domain convergence has
	//     always had to tolerate a concurrent sibling.
	// Workers deliberately modest: the win is not raw parallelism but
	// that one slow domain (ACME retry, wedged agent call) no longer
	// stalls every domain behind it.
	var wg sync.WaitGroup
	sem := make(chan struct{}, domainLoopWorkers)
	for name, domain := range enabledDomains {
		wg.Add(1)
		sem <- struct{}{}
		go func(name string, domain *models.Domain) {
			defer wg.Done()
			defer func() { <-sem }()
			r.reconcileEnabledDomain(ctx, name, domain, agentSites)
		}(name, domain)
	}
	wg.Wait()

	tt.mark(fmt.Sprintf("domain_loop(%d)", len(enabledDomains)))

	// 2. Disabled DB domain that IS in agent set -> call domain.create with is_enabled=false
	for name, domain := range disabledDomains {
		// Docker-app proxy domain disabled -> tear down its vhost
		// (the dedicated path, not the tenant disable-placeholder).
		if domain.ManagedBy == models.DomainManagedByDockerApp {
			r.removeDockerAppVhost(ctx, name)
			continue
		}
		if agentSites[name] {
			r.log.Info("reconcile: disabling unwanted domain", "domain", name)
			r.reconcileDNSZone(ctx, domain)
			r.createDomainOnAgent(ctx, domain)
		}
	}

	// Well-known nginx vhosts that ship with the distro — not managed by
	// the panel, not interesting to log. Also: the panel's OWN API vhost
	// (`jabali-panel` on :8443) and any `<domain>-mail` vhost (derived
	// from email-enabled tenant domains and managed via
	// reconcileWebmailVhosts below — never a standalone domain row).
	// Before this list / the `-mail` suffix skip, every reconciler tick
	// printed N+1 WARN lines for sites that are intentional, just not
	// tracked as domain rows.
	knownSystemSites := map[string]bool{
		"default":         true,
		"default-ssl":     true,
		"000-default":     true,
		"000-default-ssl": true,
		"jabali-panel":    true,
	}

	// 3. Orphan in agent set (no DB row) -> log warning but don't auto-delete
	for site := range agentSites {
		if knownSystemSites[site] {
			continue
		}
		// Derived mail vhosts (`<domain>-mail`) are written by
		// reconcileWebmailVhosts when a tenant domain has email
		// enabled. They have no domain row of their own and are
		// not orphans.
		if strings.HasSuffix(site, "-mail") {
			continue
		}
		if _, found := enabledDomains[site]; !found {
			if _, found := disabledDomains[site]; !found {
				r.log.Warn("reconcile: orphan site found in agent, no DB row", "site", site,
					"detail", "manual cleanup may be needed")
				// M6.3: also drop the recursor forwarder — idempotent, so
				// safe even if it was never added. Keeps the forwards file
				// from accumulating stale zones when a domain gets deleted
				// from the DB out-of-band. If the operator re-creates the
				// domain, the next tick re-adds the forwarder via the
				// enabledDomains loop.
				r.reconcileRecursorForwardRemove(ctx, site)
			}
		}
	}

	tt.mark("disable_orphan_sweep")

	r.reconcileWordPressInstalls(ctx)
	// Reconcile WordPress installs (sweep stuck rows, probe drift).

	// Webmail (M6 Step 8): toggle mail.<domain> vhost based on
	// domains.email_enabled. Self-scoping — no-op when sslCerts isn't
	// wired; per-domain errors don't abort the sweep.
	r.reconcileWebmailVhosts(ctx)

	r.reconcileCronJobs(ctx)
	// Reconcile cron jobs: apply enabled jobs, remove disabled jobs, cleanup orphans.

	// M48 docker apps: dispatch installs + status-poll.
	r.reconcileDockerApps(ctx)
	r.reconcilePythonApps(ctx)
	r.checkSharedCertExpiry(ctx)
	// Drive pending/renewing ACME shared certs (DNS-01 wildcards)
	// through the agent — see acme_shared_certs.go.
	r.reconcileAcmeSharedCerts(ctx)

	r.reconcileSSHKeysForAllUsers(ctx)
	// Reconcile SSH keys: sync authorized_keys files for all users.

	// M18: per-user resource limits + per-domain nginx rate limits.
	// Both are safe to run last — they do their own drift detection
	// and are idempotent when nothing has changed.
	r.ReconcileUserLimits(ctx)
	// ReconcileNginxRateLimits also runs at the TOP of ReconcileAll to
	// guarantee zones are declared BEFORE vhosts that reference them
	// (fixes 0→N rate_limit_rps). Running it again here handles the
	// reverse direction (N→0): after the domain loop re-rendered the
	// vhost without `limit_req`, this pass drops the stale zone
	// declaration from the fragment. Both calls are content-hash
	// gated so the no-change case is a cheap file-read.
	r.ReconcileNginxRateLimits(ctx)

	// M46: converge curated DB tuning from db_tuning_settings.
	// Idempotent — the agent no-ops when the rendered drop-in is
	// byte-identical, so steady state is a cheap file read.
	r.reconcileDBTuning(ctx)

	// M47 Wave 3: converge outbound throttle rows into Stalwart's
	// MtaOutboundThrottle objects. Each row's stalwart_id tracks
	// the upstream id so updates target the right object.
	r.reconcileMailThrottles(ctx)

	tt.mark("post_sweeps")

	return nil
}

// ReconcileOne converges a single domain ID. If the domain doesn't exist in the DB,
// it is treated as deleted and we call domain.disable on the agent.
func (r *Reconciler) ReconcileOne(ctx context.Context, domainID string) error {
	// GH #331 Step 3: a DR standby serves no traffic — skip single-domain
	// convergence too (same posture as ReconcileAll).
	if r.isStandby(ctx) {
		r.log.Debug("reconcile one skipped — DR standby", "domain_id", domainID)
		return nil
	}
	domain, err := r.domains.FindByID(ctx, domainID)
	if err != nil {
		// Domain not found in DB (e.g., it was deleted). Assume it's supposed to be gone.
		// We don't know the domain name without a DB row, so we can't disable it.
		// This is okay; the next ReconcileAll will catch any orphans.
		r.log.Info("domain not found in DB, treating as deleted", "domain_id", domainID)
		return nil
	}

	// DNS zone convergence runs FIRST and independently of the nginx/
	// user provisioning below. Previously this lived at the end of
	// createDomainOnAgent, so any early return there (missing
	// user.Username, user lookup failure, etc.) skipped DNS push →
	// PowerDNS never learned the zone → NXDOMAIN/REFUSED for live
	// queries. DNS and nginx are orthogonal concerns and must not
	// share a failure path.
	dnsCtx, dnsCancel := context.WithTimeout(ctx, 30*time.Second)
	r.reconcileDNSZone(dnsCtx, domain)
	dnsCancel()

	// Converge SSL state next so createDomainOnAgent picks up any
	// newly-issued (or revoked) cert paths when it regenerates the vhost.
	sslCtx, sslCancel := context.WithTimeout(ctx, 2*time.Minute)
	r.reconcileSSLForDomain(sslCtx, domain)
	sslCancel()

	// Auto-bind unbound domains to their owner's pool before the agent
	// RPC. Same rationale as the enabledDomains loop in ReconcileAll:
	// without this, a newly-created domain's first vhost render has
	// hasPHP=false and the browser downloads .php files. This is the
	// on-demand (Schedule'd) path so we still depend on the user having
	// a pool already; the periodic ReconcileAll tick backfills any
	// domain whose user's pool was created after its Schedule call.
	r.ensureDomainPHPBinding(ctx, domain)

	// Converge the rate-limit zone fragment BEFORE domain.create. Without
	// this, a user who changes rate_limit_rps from 0→N via the API gets a
	// Schedule() that runs ReconcileOne, which writes a vhost referencing
	// `rl_<id>` before the zone is declared → nginx -t fails → the change
	// never lands. Same ordering rule as ReconcileAll; the function is
	// idempotent (content-hash gated) so calling it per-domain is cheap
	// in the unchanged case.
	r.ReconcileNginxRateLimits(ctx)

	// Always call domain.create with is_enabled to converge to desired state.
	// The agent handles both enabled and disabled via the is_enabled parameter.
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r.createDomainOnAgent(agentCtx, domain)

	return nil
}

// ReconcileAllForce forces regeneration of all domains from the database,
// regardless of their current state on the agent. Every domain gets a fresh
// domain.create call to ensure all configurations are up-to-date.
func (r *Reconciler) ReconcileAllForce(ctx context.Context) error {
	// Rate-limit zone fragment first — same ordering rule as ReconcileAll.
	// Vhost-side limit_req references must find their zones already
	// declared or the agent's nginx -t will abort domain.create.
	r.ReconcileNginxRateLimits(ctx)

	allDomains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		return fmt.Errorf("failed to list domains: %w", err)
	}

	for i := range allDomains {
		d := &allDomains[i]

		// DNS runs independently — see ReconcileOne for rationale.
		dnsCtx, dnsCancel := context.WithTimeout(ctx, 30*time.Second)
		r.reconcileDNSZone(dnsCtx, d)
		dnsCancel()

		sslCtx, sslCancel := context.WithTimeout(ctx, 2*time.Minute)
		r.reconcileSSLForDomain(sslCtx, d)
		sslCancel()

		agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		r.createDomainOnAgent(agentCtx, d)
		cancel()
	}

	return nil
}

// createDomainOnAgent calls the agent to provision a domain.
// Logs errors but doesn't return them so reconciliation can continue.
// reconcilePHPPools ensures every panel user has a default PHP pool
// and converges pending/error pools to active status via agent apply.
// Uses injectable socket-ready check for test mocking.
func (r *Reconciler) ReconcilePHPPools(ctx context.Context) {
	if r.phpPools == nil {
		return
	}

	// Batch load up to 50 users that need PHP pools.
	// A user needs a pool if: no row exists OR existing pool status is pending/error
	allUsers, _, err := r.users.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Error("failed to list users for PHP pool reconciliation", "err", err)
		return
	}

	usersNeedingPools := make([]*models.User, 0)
	for i := range allUsers {
		user := &allUsers[i]
		pool, err := r.phpPools.FindByUserID(ctx, user.ID)
		if err != nil && err != repository.ErrNotFound {
			r.log.Error("failed to fetch PHP pool for user", "user_id", user.ID, "err", err)
			continue
		}

		// User needs a pool if no pool exists OR pool is pending/error
		if pool == nil || pool.Status == "pending" || pool.Status == "error" {
			usersNeedingPools = append(usersNeedingPools, user)
		}

		if len(usersNeedingPools) >= 50 {
			break
		}
	}

	// Process each user: ensure slice, create pool if missing, apply if pending/error
	for _, user := range usersNeedingPools {
		pool, err := r.phpPools.FindByUserID(ctx, user.ID)
		if err != nil && err != repository.ErrNotFound {
			r.log.Error("failed to fetch pool during apply", "user_id", user.ID, "err", err)
			continue
		}

		// Users without a Linux account (admin with username=NULL) get
		// no pool, slice, or apply. If a pool row exists (likely from
		// an earlier buggy reconcile), mark it error so it stays visible
		// instead of stuck pending forever.
		if user.Username == nil || *user.Username == "" || user.IsAdmin {
			if pool != nil && pool.Status != "error" {
				msg := "user has no Linux username; skipping pool provision"
				_ = r.phpPools.SetStatus(ctx, pool.ID, "error", &msg)
			}
			continue
		}

		// Ensure per-user slice and FPM drop-ins exist (idempotent via agent)
		if user.Username != nil && *user.Username != "" {
			ensureCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, sliceErr := r.agent.Call(ensureCtx, "user.slice.ensure", map[string]string{"username": *user.Username})
			cancel()
			if sliceErr != nil {
				r.log.Warn("failed to ensure user slice", "user_id", user.ID, "username", *user.Username, "err", sliceErr)
				// Warn but continue — slice not existing is a recoverable state; next tick retries.
			} else {
				r.log.Info("user slice ensured", "user_id", user.ID, "username", *user.Username)
			}
		}

		// Create default pool if missing. Version comes from the DB-
		// backed server_settings.default_php_version (set by the admin
		// via POST /admin/php/versions/:version/default). Falls back to
		// 8.4 if the row is missing, the lookup fails, or the column is
		// empty — a non-authoritative fallback so first-boot before the
		// migration ran still creates a working pool.
		if pool == nil {
			defaultPHP := "8.4"
			if r.serverSettings != nil {
				settingsCtx, settingsCancel := context.WithTimeout(ctx, 5*time.Second)
				if s, sErr := r.serverSettings.Get(settingsCtx); sErr == nil && s != nil && s.DefaultPHPVersion != "" {
					defaultPHP = s.DefaultPHPVersion
				}
				settingsCancel()
			}
			pool = &models.PHPPool{
				ID:                        ids.NewULID(),
				UserID:                    user.ID,
				PHPVersion:                defaultPHP,
				PmMode:                    "ondemand",
				PmMaxChildren:             20,
				ProcessIdleTimeoutSeconds: 60,
				Status:                    "pending",
			}
			if err := r.phpPools.Create(ctx, pool); err != nil {
				r.log.Error("failed to create default PHP pool",
					"user_id", user.ID, "pool_id", pool.ID, "err", err)
				continue
			}
			r.log.Info("created default PHP pool for user", "user_id", user.ID, "pool_id", pool.ID)
		}

		// If pool is already active, skip agent call
		if pool.Status == "active" {
			continue
		}

		// Call agent to provision the pool
		r.applyPHPPool(ctx, user, pool, true)
	}
}

// reconcileVersionedPHPPools converges the per-domain PHP-version pools (GH
// #329) — the ADDITIONAL, non-default pools a user accrues when a domain is
// pinned to a non-default PHP version. It runs after ReconcilePHPPools (which
// owns the default pool) and is a no-op on hosts with no versioned pools.
//
// For each user's pools ordered created_at ASC, pools[0] is the default
// (owned by ReconcilePHPPools) and pools[1:] are versioned. Each versioned
// pool is either applied (pending/error) or reaped (no domains bound).
//
// This also fixes the latent cpanel mixed-version restore bug: restore inserts
// multiple pool rows, but before #329 only the default was ever applied.
// versionedPoolReapGrace is how long a domain-less versioned pool is spared
// from reaping — long enough that the bind handler's pool-create → domain-bind
// (and the cpanel-restore insert → bind) has certainly finished. Slow restores
// stay comfortably inside it.
const versionedPoolReapGrace = 5 * time.Minute

func (r *Reconciler) reconcileVersionedPHPPools(ctx context.Context) {
	if r.phpPools == nil {
		return
	}
	allUsers, _, err := r.users.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Error("failed to list users for versioned PHP pool reconciliation", "err", err)
		return
	}

	processed := 0
	for i := range allUsers {
		user := &allUsers[i]
		if user.Username == nil || *user.Username == "" || user.IsAdmin {
			continue
		}
		pools, err := r.phpPools.ListByUserID(ctx, user.ID)
		if err != nil {
			r.log.Error("failed to list pools for user", "user_id", user.ID, "err", err)
			continue
		}
		if len(pools) <= 1 {
			continue // only the default pool (or none) — nothing versioned
		}

		// pools[0] is the default (created_at ASC); the rest are versioned.
		for j := 1; j < len(pools); j++ {
			pool := &pools[j]

			// Reap an orphan versioned pool: no domains bound => tear the
			// master down and drop the row. The default pool is never reaped.
			cnt, cErr := r.domains.CountByPHPPoolID(ctx, pool.ID)
			if cErr != nil {
				r.log.Error("failed to count domains for pool", "pool_id", pool.ID, "err", cErr)
				continue
			}
			if cnt == 0 {
				// Grace period: a pool the bind handler (or cpanel restore)
				// just created is momentarily domain-less between the pool
				// INSERT and the domain bind. Reaping in that window would
				// delete a pool a domain is about to reference (FK
				// fk_domain_php_pool → 500 on the version change / an unbound
				// restored domain). Only reap pools old enough that the bind
				// must have completed. A genuine orphan (domain later removed)
				// ages past the grace and is reaped on a subsequent tick.
				if time.Since(pool.CreatedAt) < versionedPoolReapGrace {
					continue
				}
				r.reapVersionedPool(ctx, user, pool)
				continue
			}

			// Apply pending/error versioned pools (active ones are converged).
			if pool.Status == "pending" || pool.Status == "error" {
				r.applyPHPPool(ctx, user, pool, false)
				processed++
			}
		}

		if processed >= 50 {
			break // bound agent work per tick, same policy as ReconcilePHPPools
		}
	}
}

// reapVersionedPool tears down an orphaned versioned PHP pool: stop+disable the
// master and remove its files on the agent, then delete the DB row. Best-effort
// — if the agent call fails the row is left for the next tick to retry. The
// default pool must never be passed here.
func (r *Reconciler) reapVersionedPool(ctx context.Context, user *models.User, pool *models.PHPPool) {
	username := *user.Username
	slug := models.PoolSlug(username, pool.PHPVersion, false)

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, err := r.agent.Call(callCtx, "php.pool.remove", map[string]any{
		"username": username,
		"slug":     slug,
	})
	cancel()
	if err != nil {
		r.log.Warn("reap versioned pool: agent remove failed; will retry",
			"pool_id", pool.ID, "slug", slug, "err", err)
		return
	}
	if err := r.phpPools.Delete(ctx, pool.ID); err != nil {
		r.log.Error("reap versioned pool: delete row failed",
			"pool_id", pool.ID, "err", err)
		return
	}
	r.log.Info("reaped orphan versioned PHP pool", "pool_id", pool.ID, "slug", slug)
}

// reconcileOrphanFPMPools garbage-collects FPM pools whose owning user was
// deleted (GH #686). user.delete tears down a deleted user's pool inline, but
// this backstop also self-heals pools orphaned before that fix shipped (or by a
// failed teardown). It hands the agent the authoritative set of live usernames;
// the agent reaps only pools whose owner is absent from that set AND has no
// surviving OS account (see php.pool.reap-orphans).
func (r *Reconciler) reconcileOrphanFPMPools(ctx context.Context) {
	if r.agent == nil || r.users == nil {
		return
	}
	users, _, err := r.users.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Error("orphan FPM reap: failed to list users", "err", err)
		return
	}
	keep := make([]string, 0, len(users))
	for i := range users {
		if users[i].Username != nil && *users[i].Username != "" {
			keep = append(keep, *users[i].Username)
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := r.agent.Call(callCtx, "php.pool.reap-orphans", map[string]any{
		"keep_usernames": keep,
	})
	if err != nil {
		r.log.Warn("orphan FPM reap: agent call failed; will retry next tick", "err", err)
		return
	}
	var resp struct {
		Reaped []string `json:"reaped"`
	}
	if json.Unmarshal(raw, &resp) == nil && len(resp.Reaped) > 0 {
		r.log.Info("reaped orphan FPM pools", "count", len(resp.Reaped), "slugs", resp.Reaped)
	}
}

// reconcileMysqlAdminShadow ensures all active users have mysqladmin shadow accounts.
// This is a safety net so the first SSO click is fast; the API handler also calls
// EnsureShadow lazily. Called every reconcile tick as a separate pass
// (does not block domain reconciliation).
func (r *Reconciler) reconcileMysqlAdminShadow(ctx context.Context) {
	if r.sso == nil {
		// SSO service not configured; skip this pass.
		return
	}

	// Query users who have a Linux username but no mysqladmin shadow yet.
	// Limit to 50 per pass to avoid overwhelming the system with agent calls.
	users, _, err := r.users.List(ctx, repository.ListOptions{Limit: 50})
	if err != nil {
		r.log.Error("reconcile: failed to list users for mysqladmin shadow backfill", "err", err)
		return
	}

	// Filter to users with a Linux username and no mysqladmin_username yet
	for _, user := range users {
		// Skip users with no Linux username (admins with empty username)
		if user.Username == nil || *user.Username == "" || user.IsAdmin {
			continue
		}

		// Skip if mysqladmin shadow already provisioned
		if user.MysqladminUsername != nil && *user.MysqladminUsername != "" {
			continue
		}

		// Ensure shadow account via the SSO service.
		// This call will:
		// - Query the agent to provision the MariaDB user (if not exists)
		// - Rotate the password (on recovery path) if user already exists
		// - Encrypt and store the credentials in the user row
		// All within a transaction with FOR UPDATE locking.
		ensureCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := r.sso.EnsureShadow(ensureCtx, user.ID)
		cancel()

		if err != nil {
			// Log the error but continue with next user (resilient loop)
			r.log.Warn("reconcile: failed to ensure mysqladmin shadow for user",
				"user_id", user.ID,
				"username", user.Username,
				"err", err)
			continue
		}

		r.log.Info("reconcile: mysqladmin shadow ensured for user",
			"user_id", user.ID,
			"username", user.Username)
	}
}

// ReapplyPHPPoolForUser re-renders a single user's pool from the current
// template + package state (GH #402 per-package disable_functions opt-out).
// Used by the package-update fan-out so an admin flipping php_exec_enabled
// takes effect immediately instead of waiting for the next sweep. No-op when
// the user has no pool. Implements api.PackagePHPPoolReconciler.
func (r *Reconciler) ReapplyPHPPoolForUser(ctx context.Context, userID string) error {
	if r.phpPools == nil || r.users == nil {
		return nil
	}
	user, err := r.users.FindByID(ctx, userID)
	if err != nil || user == nil {
		return err
	}
	pool, err := r.phpPools.FindByUserID(ctx, userID)
	if err != nil {
		if err == repository.ErrNotFound {
			return nil
		}
		return err
	}
	if pool == nil {
		return nil
	}
	r.applyPHPPool(ctx, user, pool, true)
	return nil
}

// applyPHPPool calls the agent to provision a PHP pool, waits for socket ready,
// and triggers nginx regeneration for bound domains.
func (r *Reconciler) applyPHPPool(ctx context.Context, user *models.User, pool *models.PHPPool, isDefault bool) {
	if user.Username == nil || *user.Username == "" || user.IsAdmin {
		errMsg := "user has no username"
		r.phpPools.SetStatus(ctx, pool.ID, "error", &errMsg)
		return
	}
	username := *user.Username

	// Slug + socket for this pool (GH #329). The default pool keeps the legacy
	// slug==username / /run/php/jabali-<user> socket; a versioned pool gets its
	// own slug + socket. Panel and agent derive these identically.
	slug := models.PoolSlug(username, pool.PHPVersion, isDefault)
	socketPath := models.PoolSocketPath(username, pool.PHPVersion, isDefault)

	// Call agent to apply the pool configuration
	params := map[string]any{
		"user_id":     user.ID,
		"pool_id":     pool.ID,
		"username":    username,
		"php_version": pool.PHPVersion,
		"slug":        slug,
		// Versioned pools MUST NOT wipe sibling-version pool files; the default
		// pool keeps the legacy wipe-stale-versions behaviour (harmless — its
		// glob only matches jabali-<user>.conf, never a versioned slug).
		"additive":                          !isDefault,
		"pm_mode":                           pool.PmMode,
		"pm_max_children":                   pool.PmMaxChildren,
		"process_idle_timeout_seconds":      pool.ProcessIdleTimeoutSeconds,
		"pm_start_servers":                  pool.PmStartServers,
		"pm_min_spare_servers":              pool.PmMinSpareServers,
		"pm_max_spare_servers":              pool.PmMaxSpareServers,
		"pm_max_requests":                   pool.PmMaxRequests,
		"request_terminate_timeout_seconds": pool.RequestTerminateTimeoutSeconds,
	}

	// GH #402: if the user's package opts out of the #401 command-exec
	// lockdown, send disable_functions="" (explicit opt-out -> agent emits no
	// line). Omitting the key entirely (the default) lets the agent apply its
	// safe default. Only an admin-assigned package can flip this; a tenant has
	// no path to it.
	if r.packages != nil && user.PackageID != nil && *user.PackageID != "" {
		if pkg, perr := r.packages.FindByID(ctx, *user.PackageID); perr == nil && pkg != nil && pkg.PHPExecEnabled {
			params["disable_functions"] = ""
		}
	}

	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err := r.agent.Call(agentCtx, "php.pool.apply", params)
	if err != nil {
		errMsg := fmt.Sprintf("agent apply failed: %v", err)
		r.log.Error("php.pool.apply failed", "pool_id", pool.ID, "user_id", user.ID, "err", err)
		r.phpPools.SetStatus(ctx, pool.ID, "error", &errMsg)
		return
	}

	// Wait for socket to be ready (2 second timeout, 100ms polls)
	ready := r.socketReady(ctx, socketPath, 2*time.Second, 100*time.Millisecond)
	if !ready {
		errMsg := "socket did not become ready after agent apply"
		r.log.Warn("php pool socket timeout", "pool_id", pool.ID, "socket", socketPath)
		r.phpPools.SetStatus(ctx, pool.ID, "error", &errMsg)
		return
	}

	// Mark pool as active
	if err := r.phpPools.SetStatus(ctx, pool.ID, "active", nil); err != nil {
		r.log.Error("failed to mark PHP pool active", "pool_id", pool.ID, "err", err)
		return
	}
	r.log.Info("PHP pool applied and marked active", "pool_id", pool.ID, "user_id", user.ID)

	// Trigger nginx regeneration for all domains bound to this pool
	r.regenerateNginxForPool(ctx, pool)
}

// waitSocketReady checks if a Unix socket file exists and is ready.
// Uses polling with timeout. Exported for test mocking.
func (r *Reconciler) waitSocketReady(ctx context.Context, socketPath string, timeout, pollInterval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return false
		}
	}
}

// regenerateNginxForPool finds all domains using this pool and regenerates nginx.
func (r *Reconciler) regenerateNginxForPool(ctx context.Context, pool *models.PHPPool) {
	allDomains, _, err := r.domains.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Error("failed to list domains for nginx regen", "pool_id", pool.ID, "err", err)
		return
	}

	for i := range allDomains {
		domain := &allDomains[i]
		if domain.PHPPoolID != nil && *domain.PHPPoolID == pool.ID {
			r.createDomainOnAgent(ctx, domain)
		}
	}
}

// ensureDomainPHPBinding auto-binds the domain to its owner's default PHP pool
// if it has no binding yet. This is a no-op if the domain already has a PHPPoolID
// or if the user has no pools.
func (r *Reconciler) ensureDomainPHPBinding(ctx context.Context, domain *models.Domain) {
	// If domain already has a pool binding, nothing to do.
	if domain.PHPPoolID != nil {
		return
	}

	// If phpPools repo is not available, skip.
	if r.phpPools == nil {
		return
	}

	// Find the user's (single) PHP pool.
	poolCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	pool, err := r.phpPools.FindByUserID(poolCtx, domain.UserID)
	cancel()

	if err != nil {
		if err != repository.ErrNotFound {
			r.log.Warn("ensure domain PHP binding: failed to find pool",
				"domain_id", domain.ID, "domain", domain.Name, "user_id", domain.UserID, "err", err)
		}
		return
	}

	if pool == nil {
		// User has no pool yet — skip binding. ReconcilePHPPools guarantees every
		// user has at least one pool, but it may not have converged yet on first boot.
		return
	}

	// Bind domain to the user's pool. Use the dedicated repo method —
	// the generic Update has php_pool_id off its allowlist, so a call
	// there silently drops the binding, the DB row stays NULL, and the
	// reconciler re-binds on every tick (3 domains x 60 ticks/hr =
	// 180 no-op log lines/hr; the actual log is benign but indicates
	// the column never gets persisted at all).
	updateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := r.domains.UpdatePHPPoolID(updateCtx, domain.ID, &pool.ID); err != nil {
		cancel()
		r.log.Error("ensure domain PHP binding: failed to update domain",
			"domain_id", domain.ID, "domain", domain.Name, "pool_id", pool.ID, "err", err)
		return
	}
	cancel()
	domain.PHPPoolID = &pool.ID // reflect in the in-memory copy for the rest of the pass

	r.log.Info("ensure domain PHP binding: auto-bound domain to pool",
		"domain_id", domain.ID, "domain", domain.Name, "pool_id", pool.ID)
}

// effectiveInterceptErrors resolves the branded-application-error toggle for
// a domain (GH #879): the per-domain nginx_safe_options.intercept_errors
// override wins when set; otherwise the server-wide
// intercept_app_errors_default applies (false when settings are unavailable).
func (r *Reconciler) effectiveInterceptErrors(ctx context.Context, domain *models.Domain) bool {
	if o := domain.NginxSafeOptions.InterceptErrors; o != nil {
		return *o
	}
	if r.serverSettings != nil {
		if s, err := r.serverSettings.Get(ctx); err == nil && s != nil {
			return s.InterceptAppErrorsDefault
		}
	}
	return false
}

func (r *Reconciler) createDomainOnAgent(ctx context.Context, domain *models.Domain) {
	user, err := r.users.FindByID(ctx, domain.UserID)
	if err != nil {
		r.log.Error("failed to fetch user for domain", "domain_id", domain.ID, "user_id", domain.UserID, "err", err)
		return
	}

	// Username should always be set for non-admin users hosting domains.
	if user.Username == nil || *user.Username == "" || user.IsAdmin {
		r.log.Error("user has no username for domain", "domain_id", domain.ID, "user_id", domain.UserID)
		return
	}
	username := *user.Username

	// Determine PHP configuration from domain's pool binding
	hasPHP := false
	var phpVersion string
	var fpmSocket string
	if domain.PHPPoolID != nil && r.phpPools != nil {
		phpCtx, phpCancel := context.WithTimeout(ctx, 5*time.Second)
		pool, err := r.phpPools.FindByID(phpCtx, *domain.PHPPoolID)
		phpCancel()
		if err != nil {
			r.log.Warn("failed to fetch PHP pool for domain, PHP disabled", "domain_id", domain.ID, "pool_id", *domain.PHPPoolID, "err", err)
		} else if pool != nil {
			hasPHP = true
			phpVersion = pool.PHPVersion
			// Resolve this domain's FPM socket from its bound pool (GH #329).
			// The default pool (the user's earliest, created_at ASC) keeps the
			// legacy /run/php/jabali-<user> socket — byte-identical vhost; a
			// versioned pool gets its own /run/php/jabali-<user>-php<ver> socket.
			isDefault := true
			listCtx, listCancel := context.WithTimeout(ctx, 5*time.Second)
			if pools, lErr := r.phpPools.ListByUserID(listCtx, domain.UserID); lErr == nil && len(pools) > 0 {
				isDefault = pools[0].ID == pool.ID
			}
			listCancel()
			fpmSocket = models.PoolSocketPath(username, pool.PHPVersion, isDefault)
		}
	}

	params := map[string]any{
		"username":    username,
		"domain":      domain.Name,
		"doc_root":    domain.DocRoot,
		"has_php":     hasPHP,
		"php_version": phpVersion,
		// GH #329: explicit FPM socket for the domain's bound pool. Empty for
		// static domains; the agent falls back to the legacy per-user socket
		// when unset, so older callers stay byte-identical.
		"fpm_socket": fpmSocket,
		"is_enabled": domain.IsEnabled,
		// ADR-0108 per-domain nginx FastCGI micro-cache opt-in. The
		// agent renders the cache + bypass directives only when true;
		// false ⇒ vhost byte-identical to the pre-0108 shape.
		"cache_enabled": domain.CacheEnabled,
		// Gitea #420: scope the page cache to the WP install path ("/" = whole domain).
		"cache_path":        domain.CachePath,
		"cache_ttl_seconds": domain.CacheTTLSeconds,
	}

	// cache-query-allowlist: param names that get their own cache entry
	// (e.g. "paged"). Omitted when empty so opt-out domains send a
	// byte-identical payload and the agent renders the unchanged vhost.
	if names := domain.CacheQueryAllowlistNames(); len(names) > 0 {
		params["cache_query_allowlist"] = names
	}

	// GH #601: a domain can host several cache-enabled installs (e.g. / and
	// /blog); the page-cache gate must allow EVERY one's path prefix, not just
	// the single denormalized cache_path. Gather them and pass cache_paths;
	// the agent builds a multi-path gate (or drops the gate if any is "/").
	// Best-effort — on error / none, the agent falls back to cache_path.
	if domain.CacheEnabled && r.wordPressInstalls != nil {
		liCtx, liCancel := context.WithTimeout(ctx, 5*time.Second)
		insts, iErr := r.wordPressInstalls.ListCacheEnabledByDomainID(liCtx, domain.ID)
		liCancel()
		if iErr == nil && len(insts) > 0 {
			params["cache_paths"] = cachePathsFromInstalls(insts)
		}
		if iErr == nil && len(insts) > 0 {
			if bp := cacheBypassPathsFromInstalls(insts); len(bp) > 0 {
				params["cache_bypass_paths"] = bp // GH #616 per-install URL exclusions
			}
		}
	}

	// M36 per-domain IP ACLs. Fetch + thread to agent so nginx renders
	// allow/deny directives inside the server block. Empty / unwired
	// repo → no rules → agent renders no directives (zero overhead).
	if r.domainIPACLs != nil {
		aclCtx, aclCancel := context.WithTimeout(ctx, 3*time.Second)
		acls, err := r.domainIPACLs.ListByDomain(aclCtx, domain.ID)
		aclCancel()
		if err == nil && len(acls) > 0 {
			rules := make([]map[string]string, 0, len(acls))
			for _, a := range acls {
				rules = append(rules, map[string]string{
					"cidr":   a.CIDR,
					"action": a.Action,
				})
			}
			params["ip_acls"] = rules
		}
	}

	// M50 per-directory password protection. Fetch rules + their
	// credentials so the agent can write the htpasswd file and emit
	// one `location ^~ <path>/ { auth_basic ...; }` block per rule.
	// Always send the full rule-ID set (even when empty) so the agent
	// can prune orphaned htpasswd files from previously-deleted rules.
	if r.domainDirPrivacy != nil {
		dpCtx, dpCancel := context.WithTimeout(ctx, 3*time.Second)
		rules, err := r.domainDirPrivacy.ListRulesByDomain(dpCtx, domain.ID)
		if err == nil {
			ruleIDs := make([]string, 0, len(rules))
			payload := make([]map[string]any, 0, len(rules))
			for _, rule := range rules {
				ruleIDs = append(ruleIDs, rule.ID)
				creds, cErr := r.domainDirPrivacy.ListCredentialsByRule(dpCtx, rule.ID)
				if cErr != nil {
					creds = nil
				}
				credPayload := make([]map[string]string, 0, len(creds))
				for _, c := range creds {
					credPayload = append(credPayload, map[string]string{
						"username":      c.Username,
						"password_hash": c.PasswordHash,
					})
				}
				payload = append(payload, map[string]any{
					"rule_id":     rule.ID,
					"path":        rule.Path,
					"realm":       rule.Realm,
					"credentials": credPayload,
				})
			}
			params["directory_privacy_rule_ids"] = ruleIDs
			if len(payload) > 0 {
				params["directory_privacy_rules"] = payload
			}
		}
		dpCancel()
	}

	// Add PHP INI overrides (only if not NULL).
	if domain.PHPMemoryLimit != nil {
		params["php_memory_limit"] = *domain.PHPMemoryLimit
	}
	if domain.PHPUploadMaxFilesize != nil {
		params["php_upload_max_filesize"] = *domain.PHPUploadMaxFilesize
	}
	if domain.PHPPostMaxSize != nil {
		params["php_post_max_size"] = *domain.PHPPostMaxSize
	}
	if domain.PHPMaxInputVars != nil {
		params["php_max_input_vars"] = *domain.PHPMaxInputVars
	}
	if domain.PHPMaxExecutionTime != nil {
		params["php_max_execution_time"] = *domain.PHPMaxExecutionTime
	}
	if domain.PHPMaxInputTime != nil {
		params["php_max_input_time"] = *domain.PHPMaxInputTime
	}

	cust := ""
	if domain.NginxCustomDirectives != nil {
		cust = *domain.NginxCustomDirectives
	}
	// Append the owner-set curated safe options (GH #307). These render to
	// fixed, vetted directives (max body size, HSTS, security headers, gzip) —
	// no caller-supplied target/path — so they're safe to inject alongside the
	// admin-only raw directives.
	if safe := domain.NginxSafeOptions.Render(); safe != "" {
		if cust != "" && !strings.HasSuffix(cust, "\n") {
			cust += "\n"
		}
		cust += safe
	}
	params["custom_directives"] = cust

	// GH #879: branded 500 for app errors — location-scoped in the vhost's
	// PHP blocks, so it travels as its own param rather than via Render().
	// Server-wide default with a per-domain tri-state override.
	params["intercept_errors"] = r.effectiveInterceptErrors(ctx, domain)

	// GH #962: PATH_INFO location for front-controller PHP apps (osTicket, …).
	// Per-domain nginx_safe_options toggle; needs the FPM socket so it travels
	// as its own param rather than via Render(). Default off ⇒ byte-identical.
	params["path_info"] = domain.NginxSafeOptions.PathInfo

	params["redirect_directives"] = redirects.Compile(domain)
	params["rule_directives"] = nginxrules.Compile(domain)

	// M18 per-domain HTTP limits. The agent renders them verbatim via
	// BuildRateLimitDirectives, which is a no-op when both are zero.
	// Sending domain_id regardless keeps the wire payload stable across
	// reconciles even when an operator flips a rate limit off.
	params["domain_id"] = domain.ID
	params["rate_limit_rps"] = domain.RateLimitRPS
	params["connection_limit"] = domain.ConnectionLimit

	params["index_priority"] = domain.IndexPriority

	// M24 listen IPs: resolve FK → address string. Empty string ⇒ the
	// agent renders the all-interfaces fallback. We deliberately omit
	// the params keys when ManagedIPs isn't wired so older code paths
	// (tests, profiles without an IP pool) keep their pre-M24 behaviour
	// unchanged. ResolveListenIP also handles the "binding deleted out
	// from under us" case by falling back to the family default.
	if r.managedIPs != nil {
		if v4 := r.resolveListenIPAddress(ctx, domain.ListenIPv4ID, "ipv4"); v4 != "" {
			params["listen_ipv4"] = v4
		}
		if v6 := r.resolveListenIPAddress(ctx, domain.ListenIPv6ID, "ipv6"); v6 != "" {
			params["listen_ipv6"] = v6
		}
	}

	// M28 — operator-editable default index body. Handed to the agent
	// verbatim as a Go text/template string; empty means "use agent's
	// baked-in default". Safe when pageTemplates isn't wired (tests).
	if r.pageTemplates != nil {
		tplCtx, tplCancel := context.WithTimeout(ctx, 5*time.Second)
		if row, err := r.pageTemplates.Get(tplCtx, models.PageTemplateDomainDefaultIndex); err == nil && row != nil {
			params["default_index_template"] = row.Content
		}
		tplCancel()
	}

	// GH #465 — account docroot skeleton. The agent lays these into a fresh
	// docroot before the default index (so a skeleton index.html wins) and
	// never clobbers an existing path, so re-sending on every converge is safe.
	if wire := r.skeletonWire(ctx); len(wire) > 0 {
		params["skeleton"] = wire
	}

	// Fetch SSL certificate paths for the vhost. We serve any cert whose
	// files exist on disk regardless of issuance status — that includes
	// 'issued' (Let's Encrypt success), 'self_signed' (operator-set), and
	// 'pending_acme_retry' (the self-signed fallback we generate on every
	// ACME failure so HTTPS keeps working until LE comes through). The
	// only state we deliberately skip is 'revoked' — those rows have their
	// cert_path cleared by sslRevokeForDomain so the check is belt-and-
	// braces.
	// JAB-170: a shared-cert domain serves the ONE shared pair by path,
	// independent of any per-domain ssl_certificates row. The agent stats the
	// path and falls back to HTTP if the shared cert is not on disk yet.
	if domain.SSLMode == models.SSLModeShared && domain.SharedCertificateID != nil && *domain.SharedCertificateID != "" {
		params["ssl_cert_path"], params["ssl_key_path"] = sharedCertPaths(*domain.SharedCertificateID)
	} else if r.sslCerts != nil {
		sslCtx, sslCancel := context.WithTimeout(ctx, 10*time.Second)
		cert, err := r.sslCerts.FindByDomainID(sslCtx, domain.ID)
		sslCancel()
		if err == nil && cert != nil && cert.Status != models.SSLStatusRevoked &&
			cert.CertPath != nil && cert.KeyPath != nil {
			params["ssl_cert_path"] = *cert.CertPath
			params["ssl_key_path"] = *cert.KeyPath
		}
	}

	// Preview URL params (temp URLs) — nil when disabled or no hostname.
	for k, v := range r.previewParams(ctx, domain) {
		params[k] = v
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	_, err = r.agent.Call(callCtx, "domain.create", params)
	if err != nil {
		r.log.Error("domain create failed on agent",
			"domain_id", domain.ID,
			"domain", domain.Name,
			"err", err)
	}
}

// sharedCertDir mirrors the agent's sslSharedRoot: /etc/jabali/ssl/shared/<id>
// holds the ONE cert/key pair a shared certificate serves from every attached
// domain (JAB-170).
const sharedCertDir = "/etc/jabali/ssl/shared"

// sharedCertPaths returns the on-disk fullchain + privkey paths for a shared
// certificate id. The agent stat-guards them, so an id whose cert has not been
// installed yet renders as HTTP-only rather than a broken :443 block.
func sharedCertPaths(id string) (certPath, keyPath string) {
	dir := filepath.Join(sharedCertDir, id)
	return filepath.Join(dir, "fullchain.pem"), filepath.Join(dir, "privkey.pem")
}

// reconcileDockerAppDomain renders the proxy-only nginx vhost for a
// managed_by='docker_app' domain. These domains are admin-owned with an
// empty docroot, so they CANNOT go through createDomainOnAgent (its
// tenant username/docroot assumptions reject them outright — that was
// the bug that left every docker-app reverse-proxy without a vhost).
// This is the dedicated path: resolve the loopback upstream from the
// domain's proxy_pass rule + the converged SSL cert, then dispatch
// docker_app.vhost_apply. The caller's loop branch skips all tenant-only
// convergence (PHP pool, mail, DKIM, DNS zone) by not running it.
func (r *Reconciler) reconcileDockerAppDomain(ctx context.Context, domain *models.Domain) {
	if !domain.IsEnabled {
		r.removeDockerAppVhost(ctx, domain.Name)
		return
	}

	// Upstream + websocket flag come from the proxy_pass rule the docker
	// install handler attached (path "/").
	upstream := ""
	websocket := false
	for _, rule := range domain.NginxRules {
		if rule.Type == "proxy_pass" && rule.Target != "" {
			upstream = rule.Target
			if rule.Websocket != nil {
				websocket = *rule.Websocket
			}
			break
		}
	}
	if upstream == "" {
		r.log.Warn("docker-app domain has no proxy_pass rule; skipping vhost",
			"domain", domain.Name, "domain_id", domain.ID)
		return
	}
	// Gitea #533: the proxy_pass target must be one of THIS app's own enabled
	// reverse-proxy ports. A stale/imported/edited docker-app domain row could
	// otherwise render a public vhost to another app's backend or an unrelated
	// loopback service (the agent only checks the http://127.0.0.1:<port> shape).
	if !r.dockerAppOwnsUpstreamPort(ctx, domain, upstream) {
		r.log.Warn("docker-app domain proxy_pass target is not an owned reverse-proxy port; skipping vhost",
			"domain", domain.Name, "domain_id", domain.ID, "upstream", upstream)
		return
	}

	// Cert must be on disk before the :443 block renders.
	// reconcileSSLForDomain (run by the loop before us) drops a
	// self-signed fallback on every ACME failure, so this populates
	// within a tick of the domain appearing.
	var certPath, keyPath string
	if domain.SSLMode == models.SSLModeShared && domain.SharedCertificateID != nil && *domain.SharedCertificateID != "" {
		certPath, keyPath = sharedCertPaths(*domain.SharedCertificateID)
	} else if r.sslCerts != nil {
		sslCtx, sslCancel := context.WithTimeout(ctx, 10*time.Second)
		cert, err := r.sslCerts.FindByDomainID(sslCtx, domain.ID)
		sslCancel()
		if err == nil && cert != nil && cert.Status != models.SSLStatusRevoked &&
			cert.CertPath != nil && cert.KeyPath != nil {
			certPath = *cert.CertPath
			keyPath = *cert.KeyPath
		}
	}
	if certPath == "" || keyPath == "" {
		r.log.Info("docker-app domain awaiting SSL cert; vhost deferred", "domain", domain.Name)
		return
	}

	params := map[string]any{
		"domain_name":   domain.Name,
		"upstream":      upstream,
		"ssl_cert_path": certPath,
		"ssl_key_path":  keyPath,
		"websocket":     websocket,
	}
	if r.managedIPs != nil {
		if v4 := r.resolveListenIPAddress(ctx, domain.ListenIPv4ID, "ipv4"); v4 != "" {
			params["listen_ipv4"] = v4
		}
		if v6 := r.resolveListenIPAddress(ctx, domain.ListenIPv6ID, "ipv6"); v6 != "" {
			params["listen_ipv6"] = v6
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := r.agent.Call(callCtx, "docker_app.vhost_apply", params); err != nil {
		r.log.Error("docker-app vhost apply failed",
			"domain", domain.Name, "domain_id", domain.ID, "err", err)
	}
}

// dockerAppOwnsUpstreamPort reports whether upstream's port is one of the
// docker app's own enabled reverse-proxy ports (Gitea #533). Fails closed when
// the app link, repo, or port can't be resolved.
func (r *Reconciler) dockerAppOwnsUpstreamPort(ctx context.Context, domain *models.Domain, upstream string) bool {
	if r.dockerApps == nil || domain.DockerAppID == nil {
		return false
	}
	u, err := url.Parse(upstream)
	if err != nil {
		return false
	}
	port := u.Port()
	if port == "" {
		return false
	}
	pCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ports, err := r.dockerApps.ListPortsForApp(pCtx, *domain.DockerAppID)
	if err != nil {
		return false
	}
	for _, p := range ports {
		if p.ReverseProxy && p.Enabled && strconv.Itoa(p.HostPort) == port {
			return true
		}
	}
	return false
}

// removeDockerAppVhost tears down a docker-app proxy vhost (disabled or
// deleted domain). Idempotent on the agent side.
func (r *Reconciler) removeDockerAppVhost(ctx context.Context, domainName string) {
	if domainName == "" {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := r.agent.Call(callCtx, "docker_app.vhost_remove",
		map[string]string{"domain_name": domainName}); err != nil {
		r.log.Warn("docker-app vhost remove failed", "domain", domainName, "err", err)
	}
}

// resolveListenIPAddress returns the kernel address string for a
// domain's per-family listen binding. When the explicit binding is
// missing (NULL or the row was somehow deleted), falls back to the
// family default. Returns "" when no default exists either, OR when
// the IP-pool repo isn't wired (older deployments / test profiles) —
// the agent's vhost template treats "" as "use all-interfaces fallback".
//
// Short timeouts because the call sits inside the per-domain reconcile
// hot path; an IP-pool DB stall must NOT block nginx convergence.
func (r *Reconciler) resolveListenIPAddress(ctx context.Context, id *uint64, family string) string {
	if r.managedIPs == nil {
		return ""
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if id != nil {
		row, err := r.managedIPs.FindByID(lookupCtx, *id)
		if err == nil {
			return row.Address
		}
		// Fall through to default — IP row missing despite FK RESTRICT
		// is a corruption signal, but converging the vhost is more
		// important than crashing the reconciler.
	}
	row, err := r.managedIPs.FindDefaultByFamily(lookupCtx, family)
	if err != nil {
		return ""
	}
	return row.Address
}

// been removed. Called by the DELETE handler after it deletes the row,
// because once the row is gone ReconcileOne(id) can no longer find it
// and orphan detection in ReconcileAll is intentionally conservative
// (log-only). This is the explicit "yes, actually tear this down" path.
func (r *Reconciler) ReconcileDeleted(ctx context.Context, domainName string) {
	if domainName == "" {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := r.agent.Call(callCtx, "domain.delete", map[string]string{"domain": domainName})
	if err != nil {
		r.log.Warn("domain delete failed on agent",
			"domain", domainName,
			"err", err)
	}

	// Reconcile DNS zone deletion if DNS repos are wired
	r.reconcileDNSZoneDeleted(ctx, domainName)
}

// reconcileDNSZone ensures a domain's DNS zone and records are provisioned
// on the agent. Called during domain reconciliation to push the zone state
// to PowerDNS via the agent.
func (r *Reconciler) reconcileDNSZone(ctx context.Context, domain *models.Domain) {
	if r.dnsZones == nil {
		return // DNS feature not wired — skip
	}

	zone, err := r.dnsZones.FindByDomainID(ctx, domain.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Zone doesn't exist yet — create + bootstrap.
			zone = &models.DNSZone{
				ID:             ids.NewULID(),
				DomainID:       domain.ID,
				Name:           domain.Name,
				RefreshSeconds: 3600,
				RetrySeconds:   600,
				ExpireSeconds:  604800,
				MinimumTTL:     3600,
				IsEnabled:      true,
				CreatedAt:      time.Now().UTC(),
				UpdatedAt:      time.Now().UTC(),
			}
			if err := r.dnsZones.Create(ctx, zone); err != nil {
				r.log.Error("create zone failed", "domain", domain.Name, "err", err)
				return
			}
			srv, _ := r.serverSettings.Get(ctx)
			// Skip Jabali mail rows when the domain opts out of Jabali mail
			// (provider none/m365/google). Empty provider == jabali (matches
			// reconcileMailProviderRecords), so legacy/default domains keep
			// their mail rows. GH #189: a "No mail" domain never even briefly
			// has mail DNS.
			includeMail := domain.MailProvider == "" || domain.MailProvider == models.MailProviderJabali
			boots := dnscompile.BootstrapRecords(zone.ID, zone.Name, srv, ids.NewULID, includeMail, domain.CreateWWW)
			for i := range boots {
				if err := r.dnsRecords.Create(ctx, &boots[i]); err != nil {
					r.log.Error("bootstrap record failed", "err", err)
					return
				}
			}
			r.log.Info("bootstrapped DNS zone", "zone", zone.Name, "records", len(boots))
		} else {
			r.log.Error("find zone failed", "domain", domain.Name, "err", err)
			return
		}
	}

	if !zone.IsEnabled {
		return
	}

	srv, _ := r.serverSettings.Get(ctx)
	// Migrate legacy M4-bootstrap rows to the current shape before we
	// list records for compile. Safe to call every tick: idempotent by
	// design (re-running finds no rows matching the sentinel content).
	r.migrateBootstrapShape(ctx, zone, srv)

	// M24: converge the apex A/AAAA records to the domain's effective
	// listen IP. Idempotent — only writes when content drifts from the
	// effective binding, never touches user-edited rows. Runs every
	// reconcile pass so a binding change surfaces in DNS within one
	// reconcile cycle (≤60s default) per Step 7 exit criteria.
	r.convergeApexAddrRecords(ctx, zone, domain)

	// GH#181: converge mail DNS to the domain's mail provider (jabali /
	// none / m365 / google) BEFORE listing for compile, so the right
	// MX/SPF/autodiscover (or none) get pushed to PowerDNS this pass.
	r.reconcileMailProviderRecords(ctx, zone, domain, srv)

	records, err := r.dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		r.log.Error("list records failed", "zone", zone.Name, "err", err)
		return
	}
	compiled := dnscompile.Compile(zone, records, srv)

	// Derive AXFR and NOTIFY lists from ServerSettings.
	var allowAXFR, alsoNotify []string
	if srv != nil && srv.NS2IPv4 != "" {
		allowAXFR = []string{srv.NS2IPv4}
		alsoNotify = []string{srv.NS2IPv4}
	}
	// ns1 is the master, so it doesn't need AXFR permission for itself.
	// Localhost allow is only needed for manual ops troubleshooting via
	// `dig AXFR @127.0.0.1` — add that for debugging.
	allowAXFR = append(allowAXFR, "127.0.0.1")

	// Gate the whole push on a content compare. Unconditionally stamping the
	// serial and pushing meant EVERY enabled domain, EVERY tick: one
	// dns_zones UPDATE, plus an agent RPC that DELETEs and re-INSERTs every
	// record row in PowerDNS's SQL backend and shells out three times (purge
	// auth cache, wipe recursor cache, NOTIFY slaves). With a slave
	// configured that also meant NOTIFY/AXFR churn every minute, and
	// `rec_control wipe-cache` meant recursor entries for hosted zones never
	// outlived one tick. dnsZonePushNeeded self-heals every
	// dnsZoneReDispatchInterval, so out-of-band pdns drift is still corrected.
	now := time.Now().UTC()
	hash := desiredDNSZoneHash(compiled, allowAXFR, alsoNotify)
	if !r.dnsZonePushNeeded(zone.ID, hash, now) {
		return
	}

	// Bump the serial only when we are actually pushing changed content —
	// a serial that moves every tick is what made this unconvergeable.
	zone.Serial = now.Unix()
	_ = r.dnsZones.Update(ctx, zone)

	pushCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := r.agent.Call(pushCtx, "dns.zone.upsert", map[string]any{
		"zone":            zone.Name,
		"records":         compiled,
		"allow_axfr_from": allowAXFR,
		"also_notify":     alsoNotify,
	}); err != nil {
		// Do NOT record the hash on failure: the agent may hold older
		// content, so the next tick must retry rather than believe it is
		// converged.
		r.log.Error("dns.zone.upsert failed", "zone", zone.Name, "err", err)
		return
	}
	r.dnsZonePushed(zone.ID, hash, now)
}

// reconcileDNSZoneDeleted tears down a DNS zone on the agent after its DB row
// has been deleted. Called by the domain deletion handler.
func (r *Reconciler) reconcileDNSZoneDeleted(ctx context.Context, zoneName string) {
	if r.dnsZones == nil {
		return // DNS feature not wired — skip
	}
	if zoneName == "" {
		return
	}
	pushCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := r.agent.Call(pushCtx, "dns.zone.delete", map[string]string{"zone": zoneName}); err != nil {
		r.log.Warn("dns.zone.delete failed", "zone", zoneName, "err", err)
	}
}

// linuxUserFromEmail derives the Linux username from an email address.
// Takes the part before the @ symbol (e.g., "alice@example.com" -> "alice").
func linuxUserFromEmail(email string) string {
	for i, ch := range email {
		if ch == '@' {
			return email[:i]
		}
	}
	return email
}

// sslIssueResult mirrors the shape panel-agent/internal/commands/ssl_issue.go
// returns on success. Timestamps come back as RFC3339 strings.
type sslIssueResult struct {
	CertPath  string `json:"cert_path"`
	KeyPath   string `json:"key_path"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	Staging   bool   `json:"staging"`
}

// sslSelfSignResult mirrors the shape of ssl.self_sign agent response.
type sslSelfSignResult struct {
	CertPath  string `json:"cert_path"`
	KeyPath   string `json:"key_path"`
	ExpiresAt string `json:"expires_at"`
}

// sanHostnamesForDomain returns the extra SANs the cert for this
// domain should cover beyond [domain, www.domain]. mail.<domain> ships
// once email is enabled so Bulwark's server-side JMAP verify fetch
// hits a trusted cert. Empty slice for domains without email.
//
// autoconfig.<domain> is included once pdns auto-creates the
// matching CNAME (via dnscompile/email_records.go → 'autoconfig
// CNAME mail.<zone>'). Webmail vhost server_name covers both
// mail.<domain> + autoconfig.<domain> so ACME HTTP-01 challenges
// land on the right vhost + the issued cert SAN matches Thunderbird
// + Outlook auto-config probes. Originally dropped on incident
// 2026-04-26 (jabali.site stuck pending_acme_retry) because the
// CNAME wasn't yet wired; M32 follow-up restores it now that the
// DNS side ships the record by default.
//
// autodiscover.<domain> is included too (GH #185): email_records.go
// ships the matching CNAME (GH #134) and the webmail vhost server_name
// already covers it, so Outlook's direct
// autodiscover.<domain>/autodiscover.xml probe lands on a cert that
// names it instead of a TLS mismatch.
func sanHostnamesForDomain(d *models.Domain) []string {
	if d == nil {
		return nil
	}
	var out []string
	// www.<domain> is issued ONLY when the domain opted into the www CNAME
	// (GH #895). It rides as an extra SAN (the base name is added agent-side
	// from p.Domain); resolvableSANs drops it if the record isn't live yet,
	// so it never tanks issuance. Placed before the SkipAutoSAN return so an
	// opt-in www survives even when the helper subdomains are skipped.
	if d.CreateWWW {
		out = append(out, "www."+d.Name)
	}
	// Tenant opt-out: when SkipAutoSAN is set, panel won't add the
	// auto-derived helper SANs (mail/autoconfig/mta-sts) — only the base
	// name (agent-side) and www.<domain> when opted in.
	if d.SkipAutoSAN {
		return out
	}
	if d.EmailEnabled {
		out = append(out, "mail."+d.Name, "autoconfig."+d.Name, "autodiscover."+d.Name)
	}
	// M47 Wave 7 / ADR-0109: mta-sts.<domain> must be a SAN on the
	// domain's TLS cert so the agent-served MTA-STS vhost can present
	// a valid CA-signed chain (RFC 8461 §3.3 hard requirement). Once
	// the SAN is in place, the Wave 7c reconciler step writes the
	// vhost and the policy becomes live.
	if d.MTASTSEnabled {
		out = append(out, "mta-sts."+d.Name)
	}
	return out
}

// resolvableSANs filters out hostnames with no A/AAAA records — those
// would fail the LE HTTP-01 challenge and tank the whole cert. Logs
// each drop so the operator can see which SAN is missing DNS. Resolves
// in parallel with a short per-host timeout so a slow recursor doesn't
// stall every cert issue.
// resolvableSANs filters SANs to those that resolve to PUBLIC DNS A/AAAA
// records — i.e. what Let's Encrypt's challenge validator will see when
// it queries the authoritative nameservers. The default resolver
// (/etc/resolv.conf -> 127.0.0.1 local pdns-recursor -> local pdns-auth)
// would return jabali's local PDNS view, which is misleading when the
// tenant delegates DNS to a foreign registrar and the registrar has no
// record for mail.<tenant>. LE would then 'acme:error:dns' on the
// challenge and the whole cert (including the valid apex name) fails.
//
// Caught 2026-06-04 on puzzle.linux-hosting.net: tenant domains
// (dailycrosswordpuzzlesolutions.com, crosswordpuzzleanswers.net, …)
// had no public DNS for mail.<tenant> or autoconfig.<tenant>, but the
// local PDNS happily returned puzzle's IP. resolvableSANs let those
// SANs through, certbot asked LE for them, LE failed with DNS error,
// the apex name was tarred by the same brush. 127+ retries each.
func (r *Reconciler) resolvableSANs(ctx context.Context, names []string) []string {
	if len(names) == 0 {
		return nil
	}
	type result struct {
		name string
		ok   bool
	}
	resCh := make(chan result, len(names))
	for _, n := range names {
		go func(name string) {
			lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
			defer cancel()
			addrs := dnsverify.LookupHostExternal(lookupCtx, name)
			resCh <- result{name: name, ok: len(addrs) > 0}
		}(n)
	}
	keep := make([]string, 0, len(names))
	for i := 0; i < len(names); i++ {
		res := <-resCh
		if res.ok {
			keep = append(keep, res.name)
		} else {
			r.log.Warn("ssl: skipping SAN — public DNS has no A/AAAA records (LE would fail)", "san", res.name)
		}
	}
	return keep
}

// acmeRetryInterval is how long to wait between ACME (Let's Encrypt) attempts
// after a failure. Flat 3 hours per the panel's "always-recovering SSL" policy:
// every domain gets a self-signed cert immediately on first ACME failure (so
// HTTPS keeps working) and the panel keeps trying ACME forever, every 3 hours,
// until it succeeds. No exponential backoff and no max-retry cap — the
// background ticker is cheap and a stuck cert should never become permanent.
const acmeRetryInterval = 3 * time.Hour

// acmeRetryDelay is the per-attempt backoff before re-trying a failed ACME
// issuance. 3 hours is a sane cap for persistent ACME-side failures
// (rate limits, validation that keeps failing), but a punishingly long
// wait for transient config errors the admin just fixed (e.g. nginx
// upstream missing -> "nginx test failed" -> all certs stuck in
// pending_acme_retry for 3h after a 30-second fix; observed in GH#114).
// Use exponential backoff capped at the existing 3h:
//
//	attempt 1 -> 5m   (quickest retry after a fix)
//	attempt 2 -> 15m
//	attempt 3 -> 45m
//	attempt 4 -> 2h
//	attempt 5+ -> 3h  (cap)
//
// retryCount is the count AFTER this failure is recorded.
func acmeRetryDelay(retryCount int) time.Duration {
	delays := []time.Duration{
		5 * time.Minute,
		15 * time.Minute,
		45 * time.Minute,
		2 * time.Hour,
	}
	if retryCount < 1 {
		retryCount = 1
	}
	if retryCount-1 >= len(delays) {
		return acmeRetryInterval
	}
	return delays[retryCount-1]
}

// reconcileSSLForDomain converges the ssl_certificates row for a domain to
// reflect the state the DB has declared. State machine:
//   - ssl_enabled && (no row | pending, retry_count=0)                              → tryACMEOrFallback
//   - ssl_enabled && status='pending_acme_retry' && next_retry_at <= now           → tryACMEOrFallback
//   - ssl_enabled && status='renewing'                                             → sslRenewForDomain
//   - ssl_enabled && status='self_signed'                                          → tryACMEOrFallback (attempt upgrade)
//   - !ssl_enabled && status='issued'                                              → sslRevokeForDomain
//   - !ssl_enabled && status='self_signed'                                         → no-op
//
// On ACME success the row is updated (paths + timestamps + status=issued).
// On first ACME failure, ssl.self_sign is called for fallback, then status=pending_acme_retry
// with exponential backoff. After 20 failures, status=failed (manual retry only).
// Errors are logged, never returned — SSL failures must not block the rest of the reconciler loop.
func (r *Reconciler) reconcileSSLForDomain(ctx context.Context, domain *models.Domain) {
	if r.sslCerts == nil || r.serverSettings == nil {
		return // SSL feature not wired — skip
	}
	// One certbot at a time (shared LE lock) — see sslIssueMu. Cheap in
	// steady state: domains with a live cert don't reach certbot, so the
	// critical section is a few indexed reads.
	r.sslIssueMu.Lock()
	defer r.sslIssueMu.Unlock()

	cert, err := r.sslCerts.FindByDomainID(ctx, domain.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		r.log.Error("ssl: find cert failed", "domain", domain.Name, "err", err)
		return
	}

	// GH #246: route by explicit ssl_mode. Legacy rows (empty mode) are
	// treated as 'le' so behaviour is unchanged until the backfill runs.
	switch domain.SSLMode {
	case models.SSLModeNone:
		// No certificate: drop any cert so createDomainOnAgent stops
		// emitting the :443 block. Revoke at LE for issued certs; for
		// self-signed/custom/pending just clear the row.
		if cert != nil && cert.Status != models.SSLStatusRevoked {
			if cert.Status == models.SSLStatusIssued {
				r.sslRevokeForDomain(ctx, domain, cert)
			} else if err := r.sslCerts.MarkRevoked(ctx, cert.ID); err != nil {
				r.log.Error("ssl: none-mode clear failed", "domain", domain.Name, "err", err)
			}
		}
	case models.SSLModeCustom:
		// Operator-managed: the PUT /domains/:id/ssl/custom endpoint installs
		// the cert synchronously via ssl.install_custom. The reconciler does
		// not touch custom certs (no auto-renew) — it only leaves them in
		// place for createDomainOnAgent to serve.
	case models.SSLModeSelf:
		r.sslEnsureSelfSigned(ctx, domain, cert)
	default: // SSLModeLE and legacy empty mode
		switch {
		// GH #896/#887: on the FIRST reconcile of a fresh domain there is no
		// cert file yet, and the docroot/vhost is created later in this SAME
		// ReconcileOne (SSL runs before createDomainOnAgent). Attempting ACME
		// now fails on a missing webroot (the #887 "docroot does not exist"),
		// and leaves the site with no :443 during the wait (#896). Instead
		// bootstrap a self-signed placeholder (no webroot needed): the vhost
		// step later in this tick re-reads the cert row and serves HTTPS
		// immediately, and the next tick's self_signed branch upgrades to
		// Let's Encrypt once the docroot exists.
		case cert == nil:
			r.sslEnsureSelfSigned(ctx, domain, nil)
		case cert.Status == models.SSLStatusPending && cert.RetryCount == 0 && cert.CertPath == nil:
			r.sslEnsureSelfSigned(ctx, domain, cert)
		case cert.Status == models.SSLStatusPending && cert.RetryCount == 0:
			r.tryACMEOrFallback(ctx, domain, cert)
		case cert.Status == models.SSLStatusPendingACMERetry && cert.NextRetryAt != nil && cert.NextRetryAt.Before(time.Now().UTC()):
			r.tryACMEOrFallback(ctx, domain, cert)
		case cert.Status == models.SSLStatusRenewing:
			r.sslRenewForDomain(ctx, domain, cert)
		case cert.Status == models.SSLStatusSelfSigned:
			r.tryACMEOrFallback(ctx, domain, cert)
		}
	}
}

// sslEnsureSelfSigned converges a 'self' mode domain to a self-signed cert
// (GH #246). It signs only when there is no usable cert yet — no cert row,
// a non-self-signed row, or a self-signed cert within 30 days of expiry —
// so the per-tick reconcile is a no-op once converged. Never attempts ACME.
func (r *Reconciler) sslEnsureSelfSigned(ctx context.Context, domain *models.Domain, cert *models.SSLCertificate) {
	needsSign := cert == nil ||
		cert.Status != models.SSLStatusSelfSigned ||
		cert.CertPath == nil || cert.KeyPath == nil ||
		(cert.ExpiresAt != nil && cert.ExpiresAt.Before(time.Now().UTC().Add(30*24*time.Hour)))
	if !needsSign {
		return
	}

	ssParams := map[string]any{"domain": domain.Name, "days": 365}
	if extras := sanHostnamesForDomain(domain); len(extras) > 0 {
		sanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if filtered := r.resolvableSANs(sanCtx, extras); len(filtered) > 0 {
			ssParams["hostnames"] = filtered
		}
		cancel()
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := r.agent.Call(callCtx, "ssl.self_sign", ssParams)
	if err != nil {
		r.log.Warn("ssl: self mode self_sign failed", "domain", domain.Name, "err", err)
		return
	}
	var res sslSelfSignResult
	if err := json.Unmarshal(raw, &res); err != nil {
		r.log.Warn("ssl: self mode parse result failed", "domain", domain.Name, "err", err)
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, res.ExpiresAt)
	if err != nil {
		r.log.Warn("ssl: self mode parse expires_at failed", "domain", domain.Name, "err", err)
		return
	}
	if cert == nil {
		newCert := &models.SSLCertificate{
			ID:        ids.NewULID(),
			DomainID:  domain.ID,
			Status:    models.SSLStatusSelfSigned,
			CertPath:  &res.CertPath,
			KeyPath:   &res.KeyPath,
			ExpiresAt: &expiresAt,
		}
		if err := r.sslCerts.Create(ctx, newCert); err != nil {
			r.log.Error("ssl: self mode create cert row failed", "domain", domain.Name, "err", err)
			return
		}
	} else if err := r.sslCerts.UpdateSelfSigned(ctx, cert.ID, res.CertPath, res.KeyPath, expiresAt); err != nil {
		r.log.Error("ssl: self mode update cert row failed", "domain", domain.Name, "err", err)
		return
	}
	r.log.Info("ssl: self-signed (self mode)", "domain", domain.Name, "expires_at", expiresAt.Format(time.RFC3339))
}

// needsIssue returns true when a certificate should be issued fresh: either
// there is no cert row yet, or the row is in a state that wants to try again
// (pending after API enable, or failed from a prior attempt).
func needsIssue(cert *models.SSLCertificate) bool {
	if cert == nil {
		return true
	}
	return cert.Status == models.SSLStatusPending || cert.Status == models.SSLStatusFailed
}

// tryACMEOrFallback attempts ACME issuance; on failure, calls ssl.self_sign
// for a fallback cert and schedules ACME retry with exponential backoff.
// Called by reconcileSSLForDomain when ACME should be attempted.
func (r *Reconciler) tryACMEOrFallback(ctx context.Context, domain *models.Domain, cert *models.SSLCertificate) {
	srv, err := r.serverSettings.Get(ctx)
	if err != nil || srv == nil {
		r.log.Error("ssl: read server_settings failed", "domain", domain.Name, "err", err)
		return
	}

	// Ensure a cert row exists so we have an id to thread status updates through.
	if cert == nil {
		cert = &models.SSLCertificate{
			ID:         ids.NewULID(),
			DomainID:   domain.ID,
			Status:     models.SSLStatusPending,
			RetryCount: 0,
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		if err := r.sslCerts.Create(ctx, cert); err != nil {
			r.log.Error("ssl: create cert row failed", "domain", domain.Name, "err", err)
			return
		}
	}

	// admin_email is required by Let's Encrypt's ACME flow but NOT by
	// self-sign. Skip the ACME attempt without admin_email — but still
	// generate a self-signed cert so HTTPS works, then schedule a retry
	// for 3h later (when the operator may have set the email).
	if srv.AdminEmail == "" {
		r.fallbackToSelfSignAndRetry(ctx, domain, cert, "server_settings.admin_email not set")
		return
	}

	staging := false
	if r.cfg != nil {
		staging = r.cfg.ACME.StagingOnly
	}

	// Try ACME with 60s timeout
	issueCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	params := map[string]any{
		"domain":  domain.Name,
		"webroot": domain.DocRoot,
		"email":   srv.AdminEmail,
		"staging": staging,
	}
	if extras := sanHostnamesForDomain(domain); len(extras) > 0 {
		// Drop unresolvable SAN names — they would otherwise fail
		// HTTP-01 and tank the whole cert. The base name +
		// www.<name> always remain (added agent-side).
		filtered := r.resolvableSANs(issueCtx, extras)
		// JAB-226: resolving is not enough. During a migration the helper
		// names still answer with the SOURCE box's address until its
		// nameservers stop being authoritative, so they pass the resolvable
		// test, the challenge lands on the old server, and one 404 fails the
		// whole certificate — apex included. Only applied on the ACME path:
		// a self-signed cert covering a name that points elsewhere is
		// harmless, and filtering it there would just churn the cert.
		if len(filtered) > 0 {
			filtered = r.reachableSANs(issueCtx, domain.Name, filtered)
		}
		if len(filtered) > 0 {
			params["hostnames"] = filtered
		}
	}

	raw, err := r.agent.Call(issueCtx, "ssl.issue", params)
	if err == nil {
		// ACME success
		issued, expires, ok := parseSSLIssueResult(raw, r.log, domain.Name)
		if !ok {
			msg := "agent returned unparseable ssl.issue result"
			_ = r.sslCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusFailed, &msg)
			return
		}
		var res sslIssueResult
		_ = json.Unmarshal(raw, &res)
		if err := r.sslCerts.UpdateAfterIssuance(ctx, cert.ID, issued, expires, res.CertPath, res.KeyPath); err != nil {
			r.log.Error("ssl: write issuance failed", "domain", domain.Name, "err", err)
			return
		}
		r.log.Info("ssl: issued", "domain", domain.Name, "expires_at", expires.Format(time.RFC3339))
		return
	}

	// GH #887: the document root isn't there yet (fresh domain / subdomain the
	// panel is still provisioning). This is NOT a real ACME failure — recording
	// it would surface a scary "…/public_html does not exist" cert error and bump
	// the retry backoff. The domain already carries a self-signed cert (the
	// bootstrap path), so HTTPS keeps working; just leave the row as-is and let
	// the next tick retry once the docroot exists. No LE request was spent.
	if isWebrootNotReady(err) {
		r.log.Debug("ssl: webroot not ready, deferring ACME to next tick", "domain", domain.Name)
		return
	}

	// ACME failed — fall through to self-sign + scheduled retry.
	r.fallbackToSelfSignAndRetry(ctx, domain, cert, firstLine(err.Error()))
}

// isWebrootNotReady reports whether an ssl.issue error is the agent's typed
// "the document root does not exist yet" signal (GH #887), as opposed to a real
// ACME failure. Matched on the stable marker string the agent embeds in the
// error message so it survives the NDJSON wire round-trip.
func isWebrootNotReady(err error) bool {
	return err != nil && strings.Contains(err.Error(), "webroot_not_ready")
}

// fallbackToSelfSignAndRetry is the "ACME unavailable" path used by both
// the missing-admin-email branch and an actual ACME failure. It:
//
//  1. Generates a self-signed cert (only on the first failure when no cert
//     exists yet) so HTTPS keeps working while ACME is being retried.
//  2. Bumps retry_count, records lastError, and schedules the next ACME
//     attempt for 3 hours from now (flat — no exponential backoff, no cap).
//
// The cert row stays in 'pending_acme_retry' status forever until ACME
// succeeds; the SSL ticker will pick it up at next_retry_at.
func (r *Reconciler) fallbackToSelfSignAndRetry(ctx context.Context, domain *models.Domain, cert *models.SSLCertificate, lastError string) {
	var fallbackCertPath *string
	var fallbackKeyPath *string
	var fallbackExpiresAt *time.Time

	if cert.CertPath == nil {
		// No cert file yet; generate self-signed fallback so HTTPS works
		// while we keep retrying ACME.
		selfSignCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		ssParams := map[string]any{
			"domain": domain.Name,
			"days":   365,
		}
		if extras := sanHostnamesForDomain(domain); len(extras) > 0 {
			// Self-sign uses the SAME SAN filter so the fallback
			// cert covers exactly what ACME will retry next tick.
			if filtered := r.resolvableSANs(selfSignCtx, extras); len(filtered) > 0 {
				ssParams["hostnames"] = filtered
			}
		}
		raw, sErr := r.agent.Call(selfSignCtx, "ssl.self_sign", ssParams)
		if sErr != nil {
			r.log.Warn("ssl: self_sign fallback failed", "domain", domain.Name, "err", sErr)
		} else {
			var res sslSelfSignResult
			if pErr := json.Unmarshal(raw, &res); pErr != nil {
				r.log.Warn("ssl: parse self_sign result failed", "domain", domain.Name, "err", pErr)
			} else if expiresAt, tErr := time.Parse(time.RFC3339, res.ExpiresAt); tErr != nil {
				r.log.Warn("ssl: parse self_sign expires_at failed", "domain", domain.Name, "err", tErr)
			} else {
				fallbackCertPath = &res.CertPath
				fallbackKeyPath = &res.KeyPath
				fallbackExpiresAt = &expiresAt
				r.log.Info("ssl: self-signed fallback generated", "domain", domain.Name, "expires_at", expiresAt.Format(time.RFC3339))
			}
		}
	}

	newRetryCount := cert.RetryCount + 1

	// Hard cap. Past acmeMaxRetries the cert row goes to status=failed
	// so the reconciler stops hammering LE (which rate-limits at
	// 5 failures per hostname per hour, 50 certs per registered domain
	// per week — see https://letsencrypt.org/docs/rate-limits/). The
	// operator unblocks issuance with a "Retry" button in the SSL UI
	// (resets retry_count and flips status back to pending).
	//
	// Caught 2026-06-04 on puzzle: a single hostname accumulated 127
	// attempts because the prior comment ("After 20 failures, status=
	// failed") never landed as code.
	if newRetryCount >= acmeMaxRetries {
		failMsg := lastError + " (capped at " + fmt.Sprintf("%d", acmeMaxRetries) + " attempts — manual retry required)"
		_ = r.sslCerts.UpdateAfterACMEFailureCapped(ctx, cert.ID, failMsg, newRetryCount, fallbackCertPath, fallbackKeyPath, fallbackExpiresAt)
		r.log.Warn("ssl: acme retry cap reached, marking failed", "domain", domain.Name, "retry_count", newRetryCount, "err", lastError)
		return
	}

	delay := acmeRetryDelay(newRetryCount)
	nextRetry := time.Now().UTC().Add(delay)
	_ = r.sslCerts.UpdateAfterACMEFailure(ctx, cert.ID, lastError, nextRetry, newRetryCount, fallbackCertPath, fallbackKeyPath, fallbackExpiresAt)
	r.log.Warn("ssl: acme unavailable, retrying with backoff", "domain", domain.Name, "retry_count", newRetryCount, "delay", delay.String(), "next_retry_at", nextRetry.Format(time.RFC3339), "err", lastError)
}

// acmeMaxRetries is the cap on consecutive ACME failures before the
// reconciler stops attempting issuance. Set so a hostname that's
// genuinely misconfigured (DNS not pointing here, port 80 blocked,
// etc.) stops burning LE rate limits, but a transient ACME outage or
// a sub-hour config flip still gets through. acmeRetryDelay sums to:
//
//	attempts 1..4: 5m + 15m + 45m + 2h ~= 3h
//	attempts 5..20: 3h × 16 = 48h
//
// Total window ~= 51h. Past 20 = stop.
const acmeMaxRetries = 20

// sslRenewForDomain runs an ACME renewal and updates the cert row on success.
//
// Email-enabled domains short-circuit to tryACMEOrFallback: ssl.renew
// only refreshes the existing SAN set, but M6.1 may need to grow SANs
// (e.g., email just got enabled → mail.<domain> must land on the cert).
// tryACMEOrFallback calls ssl.issue which uses --expand when needed.
func (r *Reconciler) sslRenewForDomain(ctx context.Context, domain *models.Domain, cert *models.SSLCertificate) {
	if len(sanHostnamesForDomain(domain)) > 0 {
		r.tryACMEOrFallback(ctx, domain, cert)
		return
	}
	raw, err := r.agent.Call(ctx, "ssl.renew", map[string]any{"domain": domain.Name})
	if err != nil {
		msg := firstLine(err.Error())
		_ = r.sslCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusFailed, &msg)
		r.log.Error("ssl: ssl.renew failed", "domain", domain.Name, "err", err)
		return
	}
	issued, expires, ok := parseSSLIssueResult(raw, r.log, domain.Name)
	if !ok {
		msg := "agent returned unparseable ssl.renew result"
		_ = r.sslCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusFailed, &msg)
		return
	}
	var res sslIssueResult
	_ = json.Unmarshal(raw, &res)
	if err := r.sslCerts.UpdateAfterRenewal(ctx, cert.ID, issued, expires, res.CertPath, res.KeyPath); err != nil {
		r.log.Error("ssl: write renewal failed", "domain", domain.Name, "err", err)
		return
	}
	r.log.Info("ssl: renewed", "domain", domain.Name, "expires_at", expires.Format(time.RFC3339))
}

// sslRevokeForDomain revokes an issued cert when ssl_enabled flips off.
func (r *Reconciler) sslRevokeForDomain(ctx context.Context, domain *models.Domain, cert *models.SSLCertificate) {
	if _, err := r.agent.Call(ctx, "ssl.revoke", map[string]any{
		"domain": domain.Name,
		"reason": "superseded",
	}); err != nil {
		// LE-side revoke is best-effort. Log it, but STILL clear the local
		// cert below — the operator's intent (stop serving TLS, e.g. SSL mode
		// None) must hold even when the ACME revoke call fails, otherwise the
		// vhost keeps the :443 block + http->https redirect and the site is
		// unreachable (GH #246).
		r.log.Error("ssl: ssl.revoke failed (clearing local cert anyway)", "domain", domain.Name, "err", err)
	}
	// Mark revoked AND clear paths so createDomainOnAgent stops emitting
	// the 443 server block next time it runs.
	if err := r.sslCerts.MarkRevoked(ctx, cert.ID); err != nil {
		r.log.Error("ssl: mark revoked failed", "domain", domain.Name, "err", err)
		return
	}
	r.log.Info("ssl: revoked", "domain", domain.Name)
}

// ReconcileSSLInline attempts ACME issuance synchronously with a timeout.
// Called during domain create to ensure the cert is available before the HTTP response.
// Never errors out — failures are logged; the cert state is already in the database.
func (r *Reconciler) ReconcileSSLInline(ctx context.Context, domain *models.Domain) {
	if r.sslCerts == nil || !domain.SSLEnabled {
		return // SSL feature not wired or not enabled — skip
	}

	// Create a cert row for this domain if one doesn't exist
	cert, err := r.sslCerts.FindByDomainID(ctx, domain.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		r.log.Error("ssl inline: find cert failed", "domain", domain.Name, "err", err)
		return
	}

	if cert == nil {
		cert = &models.SSLCertificate{
			ID:         ids.NewULID(),
			DomainID:   domain.ID,
			Status:     models.SSLStatusPending,
			RetryCount: 0,
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		}
		if err := r.sslCerts.Create(ctx, cert); err != nil {
			r.log.Error("ssl inline: create cert row failed", "domain", domain.Name, "err", err)
			return
		}
	}

	// Attempt ACME issuance synchronously
	r.tryACMEOrFallback(ctx, domain, cert)
}

// RetrySSLDueForACME finds all certificates due for ACME retry
// and attempts to reissue them. Called by the SSL retry ticker.
func (r *Reconciler) RetrySSLDueForACME(ctx context.Context) {
	if r.sslCerts == nil {
		return // SSL feature not wired — skip
	}
	if r.isStandby(ctx) {
		return // DR standby issues no ACME (GH #331 Step 3)
	}

	certs, err := r.sslCerts.ListDueForACMERetry(ctx, time.Now().UTC(), 10)
	if err != nil {
		r.log.Error("ssl: list due for acme retry failed", "err", err)
		return
	}

	if len(certs) == 0 {
		return
	}

	r.log.Debug("ssl: processing acme retries", "count", len(certs))

	for _, cert := range certs {
		// Fetch the domain for context
		domain, err := r.domains.FindByID(ctx, cert.DomainID)
		if err != nil {
			r.log.Error("ssl: find domain failed for retry", "domain_id", cert.DomainID, "err", err)
			continue
		}

		r.log.Debug("ssl: retrying acme issuance", "domain", domain.Name, "retry_count", cert.RetryCount)
		r.tryACMEOrFallback(ctx, domain, &cert)
	}
}

// parseSSLIssueResult decodes the agent's ssl.issue / ssl.renew response
// (which agent.Call delivers as json.RawMessage) and parses the timestamps.
// Returns ok=false on any parse failure — caller should mark the cert row
// 'failed' in that case.
func parseSSLIssueResult(raw json.RawMessage, log *slog.Logger, domain string) (time.Time, time.Time, bool) {
	var res sslIssueResult
	if err := json.Unmarshal(raw, &res); err != nil {
		log.Error("ssl: decode agent result failed", "domain", domain, "err", err)
		return time.Time{}, time.Time{}, false
	}
	issued, err := time.Parse("2006-01-02T15:04:05Z", res.IssuedAt)
	if err != nil {
		log.Error("ssl: parse issued_at failed", "domain", domain, "err", err, "value", res.IssuedAt)
		return time.Time{}, time.Time{}, false
	}
	expires, err := time.Parse("2006-01-02T15:04:05Z", res.ExpiresAt)
	if err != nil {
		log.Error("ssl: parse expires_at failed", "domain", domain, "err", err, "value", res.ExpiresAt)
		return time.Time{}, time.Time{}, false
	}
	return issued, expires, true
}

// firstLine returns the first line of s, bounded at 512 bytes so we never
// stuff a giant stderr dump into last_error.
func firstLine(s string) string {
	if i := indexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// migrateBootstrapShape rewrites legacy M4-bootstrap rows to the current
// shape on every zone reconcile. Two rewrites happen:
//
//  1. www A / AAAA → www CNAME <zone>. Apex-IP changes then propagate
//     via the single apex A/AAAA row, instead of per-record.
//  2. The pre-ip4 SPF `"v=spf1 mx ~all"` → `"v=spf1 mx ip4:… ~all"`
//     (plus ip6 when configured). Matches what BootstrapRecords now
//     emits for freshly-provisioned zones.
//
// Guard rails keep operator edits untouched:
//
//   - Only rows with Managed=true AND ManagedBy IS NULL are considered.
//     Operator-created rows (Managed=false) and feature-scoped rows
//     (M6 email uses ManagedBy="m6") stay as they are.
//   - SPF rewrite requires an EXACT match against
//     dnscompile.LegacyBootstrapSPFContent. One character of drift
//     means the operator touched it; skip.
//   - www rewrite requires that a CNAME doesn't already exist —
//     otherwise a double-run could crash on a unique-index violation,
//     and a manual (legitimate) CNAME override from the operator
//     must not be clobbered by the legacy A row this function is
//     about to delete.
//
// Idempotent: once the new shape is in place, subsequent calls find
// nothing to do (no legacy A rows, no legacy SPF content).
// convergeApexAddrRecords upserts the `@` A and `@` AAAA records to
// match the domain's effective listen IP. Only touches rows flagged
// Managed=true AND ManagedBy IS NULL (the bootstrap-owned apex addrs);
// user-edited rows (Managed=false) and M6-owned rows (ManagedBy="m6")
// are left untouched. Missing rows are created — solves the pre-M24
// bootstrap case where no v6 was configured at zone-create but the
// admin later adds one to the pool.
//
// Uses `mail` to check presence — the MX host's A/AAAA stay pinned to
// the server primary because the MTA listens there, regardless of where
// the tenant's vhost binds. The `mail` reconcile is out of scope for
// Step 7 (no bug reported, no plan task) — if we ever want it, it needs
// its own sentinel logic.
func (r *Reconciler) convergeApexAddrRecords(ctx context.Context, zone *models.DNSZone, domain *models.Domain) {
	if r.dnsRecords == nil || zone == nil || domain == nil {
		return
	}
	// Without the IP pool we have no source of truth for what the
	// effective addresses should be. Skipping is safe because the
	// pre-M24 BootstrapRecords already wrote the server-primary
	// addresses on zone create.
	if r.managedIPs == nil {
		return
	}
	v4 := r.resolveListenIPAddress(ctx, domain.ListenIPv4ID, "ipv4")
	v6 := r.resolveListenIPAddress(ctx, domain.ListenIPv6ID, "ipv6")

	existing, err := r.dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		r.log.Error("converge apex addrs: list records failed", "zone", zone.Name, "err", err)
		return
	}
	// GH #527: apex A/AAAA rows use the configured default TTL, not a
	// hardcoded value, so operator changes to default_dns_ttl take effect.
	var srv *models.ServerSettings
	if r.serverSettings != nil {
		srv, _ = r.serverSettings.Get(ctx)
	}
	ttl := models.EffectiveDNSTTL(srv)
	r.ensureApexAddrRow(ctx, zone.ID, existing, "A", v4, ttl)
	r.ensureApexAddrRow(ctx, zone.ID, existing, "AAAA", v6, ttl)
}

// ensureApexAddrRow finds the `@` record of the given type in the
// existing slice. Three outcomes:
//   - Row exists, Managed=true, ManagedBy=NULL, content already correct: no-op.
//   - Row exists, Managed=true, ManagedBy=NULL, content drifts: UPDATE.
//   - Row exists, Managed=false OR ManagedBy!=NULL: skip (operator edit / M6).
//   - No row exists, content non-empty: INSERT (system bootstrap of newly-added family).
//
// Content="" means no effective IP for that family (e.g. server has no
// v6 configured and no v6 binding). In that case we DON'T create a row
// and DON'T blank an existing managed row — the operator may intend to
// add v6 later; clobbering the row would drop a working record.
func (r *Reconciler) ensureApexAddrRow(ctx context.Context, zoneID string, existing []models.DNSRecord, recType, content string, ttl int) {
	var existingRow *models.DNSRecord
	for i := range existing {
		if existing[i].Name == "@" && existing[i].Type == recType {
			existingRow = &existing[i]
			break
		}
	}
	if existingRow != nil {
		if !existingRow.Managed || existingRow.ManagedBy != nil {
			return
		}
		if existingRow.Content == content {
			return
		}
		if content == "" {
			// No effective IP for this family — leave the existing
			// row alone. See method doc for rationale.
			return
		}
		existingRow.Content = content
		existingRow.UpdatedAt = time.Now().UTC()
		if err := r.dnsRecords.Update(ctx, existingRow); err != nil {
			r.log.Error("converge apex addrs: update failed",
				"zone_id", zoneID, "type", recType, "err", err)
			return
		}
		r.log.Info("converge apex addrs: updated", "zone_id", zoneID, "type", recType, "content", content)
		return
	}
	if content == "" {
		return
	}
	rec := &models.DNSRecord{
		ID:        ids.NewULID(),
		ZoneID:    zoneID,
		Name:      "@",
		Type:      recType,
		Content:   content,
		TTL:       ttl,
		Managed:   true,
		IsEnabled: true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := r.dnsRecords.Create(ctx, rec); err != nil {
		r.log.Error("converge apex addrs: create failed",
			"zone_id", zoneID, "type", recType, "err", err)
		return
	}
	r.log.Info("converge apex addrs: created", "zone_id", zoneID, "type", recType, "content", content)
}

func (r *Reconciler) migrateBootstrapShape(ctx context.Context, zone *models.DNSZone, srv *models.ServerSettings) {
	if r.dnsRecords == nil || srv == nil || zone == nil {
		return
	}
	existing, err := r.dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		r.log.Error("migrate bootstrap: list records failed", "zone", zone.Name, "err", err)
		return
	}

	// ---------- www A/AAAA → www CNAME <zone> -----------------------
	//
	// Two filters, different scopes. Eligibility to delete requires the
	// row be a Managed bootstrap row (Managed=true, ManagedBy=nil).
	// But ANY www CNAME — operator-created or otherwise — disqualifies
	// the whole rewrite: we must never leave both a CNAME and an A on
	// the same name (DNS protocol violation), and we must never delete
	// an A out from under a CNAME the operator added deliberately.
	var legacyWWW []models.DNSRecord
	wwwCNAMEExists := false
	for i := range existing {
		rec := existing[i]
		if rec.Name != "www" {
			continue
		}
		if rec.Type == "CNAME" {
			wwwCNAMEExists = true
			continue
		}
		if (rec.Type == "A" || rec.Type == "AAAA") && rec.Managed && rec.ManagedBy == nil {
			legacyWWW = append(legacyWWW, rec)
		}
	}
	if len(legacyWWW) > 0 && !wwwCNAMEExists && zone.Name != "" {
		failed := false
		for _, rec := range legacyWWW {
			if err := r.dnsRecords.Delete(ctx, rec.ID); err != nil {
				r.log.Error("migrate bootstrap: delete legacy www failed",
					"zone", zone.Name, "id", rec.ID, "type", rec.Type, "err", err)
				failed = true
				break
			}
		}
		if !failed {
			now := time.Now().UTC()
			cname := models.DNSRecord{
				ID:        ids.NewULID(),
				ZoneID:    zone.ID,
				Name:      "www",
				Type:      "CNAME",
				Content:   zone.Name,
				TTL:       models.EffectiveDNSTTL(srv), // GH #527
				Managed:   true,
				IsEnabled: true,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := r.dnsRecords.Create(ctx, &cname); err != nil {
				r.log.Error("migrate bootstrap: create www CNAME failed",
					"zone", zone.Name, "err", err)
			} else {
				r.log.Info("migrated www to CNAME",
					"zone", zone.Name, "deleted", len(legacyWWW), "cname_target", zone.Name)
			}
		}
	}

	// ---------- legacy SPF → ip4/ip6 SPF ----------------------------
	want := dnscompile.BuildSPFString(srv)
	for i := range existing {
		rec := existing[i]
		if rec.Name != "@" || rec.Type != "TXT" || !rec.Managed || rec.ManagedBy != nil {
			continue
		}
		if rec.Content != dnscompile.LegacyBootstrapSPFContent {
			continue
		}
		if rec.Content == want {
			continue // no-op (neither v4 nor v6 configured)
		}
		rec.Content = want
		rec.UpdatedAt = time.Now().UTC()
		if err := r.dnsRecords.Update(ctx, &rec); err != nil {
			r.log.Error("migrate bootstrap: update SPF failed",
				"zone", zone.Name, "err", err)
		} else {
			r.log.Info("migrated SPF to ip4/ip6 shape",
				"zone", zone.Name, "new_content", rec.Content)
		}
		break // only one apex SPF row, stop scanning
	}

	// ---------- legacy MX short-label → FQDN ------------------------
	//
	// Pre-fix BootstrapRecords wrote MX content as the bare label
	// "mail". PDNS serves content verbatim — the wire answer "mail."
	// is a root-relative name that resolvers treat as a TLD lookup and
	// fail. Rewrite to "mail.<zone>" so the paired apex mail A/AAAA
	// row is actually reachable.
	//
	// Same eligibility as the SPF rewrite: Managed=true AND
	// ManagedBy=nil. Operator-edited (Managed=false) and feature-owned
	// (e.g. M6 email) rows are left alone.
	if zone.Name != "" {
		wantMX := "mail." + zone.Name
		for i := range existing {
			rec := existing[i]
			if rec.Name != "@" || rec.Type != "MX" || !rec.Managed || rec.ManagedBy != nil {
				continue
			}
			if rec.Content != "mail" {
				continue // operator-edited, or already migrated, or points elsewhere
			}
			rec.Content = wantMX
			rec.UpdatedAt = time.Now().UTC()
			if err := r.dnsRecords.Update(ctx, &rec); err != nil {
				r.log.Error("migrate bootstrap: update MX failed",
					"zone", zone.Name, "err", err)
			} else {
				r.log.Info("migrated MX to FQDN shape",
					"zone", zone.Name, "new_content", rec.Content)
			}
			break // only one apex MX row, stop scanning
		}
	}

	// ---------- backfill ns1/ns2 A records --------------------------
	//
	// New zones get these via BootstrapRecords. Existing zones that
	// pre-date the fix have @ NS rows (synthesised at compile time
	// from server_settings.ns1_name / ns2_name) but no in-zone A
	// rows for the nameserver labels, so `host ns1.<zone>` returns
	// NXDOMAIN. Insert when:
	//   - server_settings.ns1_name (or ns2_name) ends with "." + zone.Name
	//   - corresponding A row is missing
	// Idempotent: skips when the row already exists.
	if zone.Name != "" {
		for _, ns := range []struct{ name, ipv4 string }{
			{srv.NS1Name, srv.NS1IPv4},
			{srv.NS2Name, srv.NS2IPv4},
		} {
			if ns.name == "" || ns.ipv4 == "" {
				continue
			}
			suffix := "." + zone.Name
			if !strings.HasSuffix(ns.name, suffix) {
				continue
			}
			label := strings.TrimSuffix(ns.name, suffix)
			if label == "" {
				continue
			}
			already := false
			for _, rec := range existing {
				if rec.Name == label && rec.Type == "A" {
					already = true
					break
				}
			}
			if already {
				continue
			}
			now := time.Now().UTC()
			rec := &models.DNSRecord{
				ID:        ids.NewULID(),
				ZoneID:    zone.ID,
				Name:      label,
				Type:      "A",
				Content:   ns.ipv4,
				TTL:       models.EffectiveDNSTTL(srv), // GH #527
				Managed:   true,
				IsEnabled: true,
				CreatedAt: now,
				UpdatedAt: now,
			}
			if err := r.dnsRecords.Create(ctx, rec); err != nil {
				r.log.Error("migrate bootstrap: insert ns A failed",
					"zone", zone.Name, "label", label, "err", err)
			} else {
				r.log.Info("backfilled ns A record",
					"zone", zone.Name, "name", label, "content", ns.ipv4)
			}
		}
	}
}

// cachePathsFromInstalls maps a domain's cache-enabled installs to the set of
// page-cache path prefixes the agent's buildCacheGate consumes (GH #601). This
// is the reconciler↔agent contract: subdirectory "" → "/" (root, whole domain),
// "blog" or "/blog/" → "/blog", "shop/eu" → "/shop/eu"; deduped, order-stable.
// Kept as a pure function so the exact emitted shape is unit-tested (the agent
// side is tested separately — this closes the seam).
func cachePathsFromInstalls(insts []models.ApplicationInstall) []string {
	seen := map[string]bool{}
	paths := make([]string, 0, len(insts))
	for _, in := range insts {
		p := "/"
		if s := strings.Trim(in.Subdirectory, "/"); s != "" {
			p = "/" + s
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}

// cacheBypassPathsFromInstalls collects the union of every cache-enabled
// install's per-install URL exclusions (GH #616) on a domain — extra path
// prefixes that must always bypass the page cache, on top of the built-in
// wp-admin/cart/checkout set. The agent re-sanitizes each before rendering it
// into the nginx bypass regex (config-injection trust boundary). Pure +
// seam-tested; deduped, order-stable.
func cacheBypassPathsFromInstalls(insts []models.ApplicationInstall) []string {
	seen := map[string]bool{}
	out := []string{}
	for i := range insts {
		data, _ := insts[i].ParseCacheSettings()
		// GH #618: the selected profile contributes its preset bypass paths on
		// top of the user url_exclusions.
		paths := append(append([]string{}, models.CacheProfilePaths(data.Profile)...), data.URLExclusions...)
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// reconcileEnabledDomain converges one enabled domain — the body of the
// ReconcileAll per-domain loop, extracted so the JAB-205 worker pool can
// run domains concurrently. Must remain safe to run concurrently with
// itself and with ReconcileOne (see the pool comment in ReconcileAll).
func (r *Reconciler) reconcileEnabledDomain(ctx context.Context, name string, domain *models.Domain, agentSites map[string]bool) {
	// Docker-app proxy domains (managed_by='docker_app') are
	// admin-owned, docroot-less reverse proxies — they take a
	// dedicated render path and skip EVERY tenant-only convergence
	// step (DNS zone, PHP pool, mail, DKIM, MTA-STS, M6.5 phases,
	// createDomainOnAgent). Branch out high, run only {SSL, proxy
	// vhost}, like the IsPanelPrimary row below.
	if domain.ManagedBy == models.DomainManagedByDockerApp {
		if !agentSites[name] {
			r.log.Info("reconcile: docker-app proxy domain", "domain", name)
		}
		sslCtx, sslCancel := context.WithTimeout(ctx, 2*time.Minute)
		r.reconcileSSLForDomain(sslCtx, domain)
		sslCancel()
		r.reconcileDockerAppDomain(ctx, domain)
		return
	}
	// Panel-primary rows (is_panel_primary=1) are mail-only and
	// intentionally skipped further down (the continue at the
	// IsPanelPrimary branch) — they are NEVER added to the agent's
	// site list, so logging "creating missing" for them every
	// reconciler tick was pure spam ("puzzle.linux-hosting.net"
	// loop). Only log for real tenant domains where the missing
	// state is actionable.
	if !agentSites[name] && !domain.IsPanelPrimary {
		r.log.Info("reconcile: creating missing domain", "domain", name)
	}
	r.reconcileDNSZone(ctx, domain)

	// M6.4 (ADR-0048): is_panel_primary rows are mail-only. They
	// have no public docroot, no PHP, no per-tenant SSL cert (the
	// self-signed panel cert already covers mail.<hostname> via its
	// SAN). The HTTP-vhost path would fail anyway: admin owners have
	// no Linux username, doc_root is empty ("must start with /home/"
	// per the agent validator), and creating a server_name=<host>
	// vhost would hijack /webmail from the default vhost. Skip SSL,
	// PHP, and domain.create for these rows; the shared
	// reconcileWebmailVhosts sweep (lower in ReconcileAll) still
	// applies mail.<host>, and ensurePanelPrimaryDKIM below handles
	// DKIM/Stalwart/DNS provisioning that the HTTP email-enable
	// handler would normally run.
	if domain.IsPanelPrimary {
		r.reconcileRecursorForward(ctx, name)
		r.ensurePanelPrimaryDKIM(ctx, domain)
		return
	}

	// Converge SSL state BEFORE the agent RPC. createDomainOnAgent
	// renders the vhost using the cert paths the ssl_certificates row
	// points at, so a fresh-issued cert must land in the DB before the
	// vhost template is re-rendered this pass. This is also what drives
	// the 3-hour ACME retry for pending_acme_retry certs — without this
	// call in the steady-state loop, retries only ran on out-of-band
	// Schedule() or an explicit force, and seed-time domains never got
	// their first cert attempted at all.
	sslCtx, sslCancel := context.WithTimeout(ctx, 2*time.Minute)
	r.reconcileSSLForDomain(sslCtx, domain)
	sslCancel()
	// M47 Wave 7c — converge mta-sts vhost (idempotent diff-aware).
	r.reconcileMTAStsForDomain(ctx, domain)
	// Auto-bind unbound domains to their owner's pool BEFORE the
	// agent RPC. Without this, a newly-created domain renders an
	// nginx vhost with no "location ~ \\.php$" block and the browser
	// downloads info.php instead of executing it. ReconcilePHPPools
	// already ran at the top of ReconcileAll so every user has a
	// pool; this associates it with pre-existing unbound domains.
	r.ensureDomainPHPBinding(ctx, domain)
	// Auto-provision DKIM keypair + Stalwart domain for any tenant
	// domain with email_enabled=1 but no DKIM material yet. Since
	// mig 000123 makes email_enabled the default, this replaces the
	// former manual domain.email_enable operator step.
	r.ensureTenantEmailEnabled(ctx, domain)
	// Back-fill M6 DNS rows for tenants enabled before DKIM-emit
	// code shipped (or whose insert failed mid-flight). No-op when
	// the rows already exist; safe on every tick.
	r.ensureTenantDKIMRecords(ctx, domain)
	r.createDomainOnAgent(ctx, domain)
	// M6.3: ensure the recursor has a forwarder for this zone so
	// local resolution hits pdns-server on loopback :5300. Idempotent.
	r.reconcileRecursorForward(ctx, name)

	// M6.5: Email features (forwarders, autoresponders, catch-all, disclaimer,
	// shared folders, logs). Each feature is registered as a Phase during init(),
	// enabling parallel Wave development without file collisions (ADR-0051).
	// This is a no-op until Wave B/C populate the phase implementations.
	// Domain-level phases called with nil context; mailbox phases deferred to Wave B+.
	if err := phases.ReconcileDomainAll(ctx, domain, nil); err != nil {
		r.log.Error("reconcile: M6.5 phase domain reconciliation failed", "domain", name, "err", err)
		// Log error but continue — one phase failure doesn't abort the entire domain.
	}
}
