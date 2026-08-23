// Package eventsources wires the in-process producers that fan system
// events into the M14 notification pipeline. Each source is a small
// goroutine that observes some piece of system state on a cadence and
// publishes envelopes on the dispatcher's Queue — senders never talk
// to the kernel or shell; that job belongs here.
//
// All envelopes funnel through Publisher.Publish. Sources share the
// Deduper helper so the same transient state (85% disk, failing
// service) doesn't fire every tick.
package eventsources

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// AgentCaller mirrors the narrow slice eventsources need from the
// agent: a single Call that takes method+params and returns raw
// JSON. Avoids importing the full agent package and keeps tests
// trivial.
type AgentCaller interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// Publisher is the write end of the notification pipeline. Matches
// notifications.Queue.Publish; defined here as an interface so tests
// can supply a capturing fake without pulling in Redis.
type Publisher interface {
	Publish(ctx context.Context, env notifications.Envelope) (string, error)
}

// Clock returns the current time. Tests inject a fake clock.
type Clock func() time.Time

// SSLCertLister is the narrow read-only slice cert_renew needs. Keeps
// the eventsources package decoupled from the full SSL repo surface
// (and lets tests supply a tiny fake).
type SSLCertLister interface {
	ListAll(ctx context.Context) ([]repository.SSLCertificateWithDomain, error)
}

// HistoryLookup is the narrow surface dedupe needs. Kept separate so a
// future in-memory dedupe implementation can be slotted in without
// touching the full repo.
type HistoryLookup interface {
	ListRecentByEvent(ctx context.Context, kind string, since time.Time) ([]models.NotificationHistory, error)
}


// StalwartQueryClient is the narrow Stalwart-admin slice the M47
// Wave 4/6/8 ingest sources need — just Query (Get / Create / Update
// aren't needed here; the ingest is read-only).
type StalwartQueryClient interface {
	Query(ctx context.Context, typeName string, filters ...string) (json.RawMessage, error)
}

// Deps bundles the collaborators every source needs. Zero-valued fields
// are legal — sources check and skip themselves when a dependency is
// missing rather than panicking, so on a minimal install (no SSL certs
// table, no CrowdSec) the remaining sources still run.
type Deps struct {
	Queue       Publisher
	History     HistoryLookup
	SSLCerts    SSLCertLister
	Log         *slog.Logger
	Now         Clock
	CrowdSecBin string // path to cscli; empty → disable the crowdsec source
	// disk_quota source — needs Users + Agent + QuotaMount to call
	// agent.user.limits.report per user. All three required; missing
	// any disables the source rather than panicking.
	Users      repository.UserRepository
	Agent      AgentCaller
	QuotaMount string
	// M33 malware events — runMalware drains malware_events.notified=0
	// rows into the dispatcher with severity-tier gating from
	// malware_settings.notify_threshold.
	MalwareEvents   repository.MalwareEventRepository
	MalwareSettings repository.MalwareSettingsRepository
	// M38 Ghost Domain Detector — periodic DNS-alignment check. Both
	// fields required for the source to start; nil disables.
	DomainsForGhost    DomainGhostRepo
	ManagedIPsForGhost ManagedIPLister
	// M34 per-user PHP-FPM egress firewall burst source. Compares each
	// user's drop_count_24h tick value against ServerSettings.EgressBurstThreshold
	// every minute and fires egress.drop.burst when one or more users
	// cross. Nil ServerSettings or UserEgressPolicies disables the source.
	UserEgressPolicies repository.UserEgressPolicyRepository
	ServerSettings     repository.ServerSettingsRepository
	// M41 Snuffleupagus ingest. Nil disables the ingest source.
	Snuffleupagus repository.SnuffleupagusRepository
	// M13.1 bandwidth quota event source. All three needed; nil
	// disables the source.
	BWDaily  repository.BWDailyRepository
	Domains  repository.DomainRepository
	Packages repository.PackageRepository
	// M14 backup_fail watcher. Nil disables the source — tests +
	// minimal installs without M30 backups skip the goroutine.
	BackupJobs repository.BackupJobRepository
	// M47 Wave 5 mail-RBL monitor. ServerSettings + MailRBLStates
	// both required (ServerSettings provides the public IPv4 to
	// probe; MailRBLStates is where transitions are persisted).
	MailRBLStates repository.MailRBLStateRepository

	// M47 Wave 4/6/8 ingest sources. StalwartAdmin is the
	// stalwart-cli subprocess wrapper; nil disables all three
	// ingest goroutines. Each ingest also needs its own repo:
	// DMARCAggregate / TLSRPTAggregate / ARFReports.
	StalwartAdmin   StalwartQueryClient
	DMARCAggregate  repository.DMARCAggregateRepository
	TLSRPTAggregate repository.TLSRPTAggregateRepository
	ARFReports      repository.ARFReportRepository
}

// Start boots every configured source in its own goroutine. Each
// source respects ctx.Done and returns cleanly on cancellation so
// serve.go's dispatcher shutdown drags them along.
func Start(ctx context.Context, d Deps) {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Queue == nil {
		d.Log.Warn("eventsources: no publisher; not starting any source")
		return
	}
	go runCertRenew(ctx, d)
	go runDiskFull(ctx, d)
	go runServiceDown(ctx, d)
	go runNginxConfig(ctx, d)
	go runCrowdSec(ctx, d)
	go runLoadHigh(ctx, d)
	go runSystemUpdate(ctx, d)
	go runDiskQuota(ctx, d)
	go runSSHLogin(ctx, d)
	go runMalware(ctx, d)
	go runDomainGhost(ctx, d)
	go runEgressBurst(ctx, d)
	go runAideTamper(ctx, d)
	go runSnuffleupagusIngest(ctx, d)
	go runSecurityDecision(ctx, d)
	go runPostgresMonitor(ctx, d)
	go runPanelLogTail(ctx, d)
	go runBandwidthQuota(ctx, d)
	go runExecAuditBurst(ctx, d)
	go runBackupFail(ctx, d)
	go runBackupSuccess(ctx, d)
	go runMailRBL(ctx, d)
	go runMailDmarcIngest(ctx, d)
	go runMailTlsRptIngest(ctx, d)
	go runMailAbuseIngest(ctx, d)
	// domain_expiry remains a stub — WHOIS pipeline lives outside the
	// panel-api process today (M15 future work).
}

// shouldFire returns true when no history row for eventKind with the
// given dedupe key exists newer than `cooldown`. Keeps the stream from
// growing unbounded on a stuck failure state.
func shouldFire(ctx context.Context, d Deps, eventKind, dedupeTag string, cooldown time.Duration) bool {
	if d.History == nil {
		return true
	}
	rows, err := d.History.ListRecentByEvent(ctx, eventKind, d.Now().Add(-cooldown))
	if err != nil {
		// Querying failed — log once, don't block the fire. Worst case
		// we get a duplicate notification, which beats silencing a real
		// incident.
		d.Log.Warn("eventsources: history dedupe lookup failed", "event_kind", eventKind, "err", err)
		return true
	}
	for _, row := range rows {
		// Dedupe key is stashed in the body for now — ADR-0056 doesn't
		// have a dedicated column. Matching on substring is cheap and
		// good enough: if the body doesn't contain the tag, it's a
		// different incident and should fire again.
		if dedupeTag == "" || contains(row.Body, dedupeTag) {
			return false
		}
	}
	return true
}

// contains is strings.Contains without the strings import in the hot
// path — trivial inline to keep imports lean.
func contains(haystack, needle string) bool {
	n := len(needle)
	if n == 0 {
		return true
	}
	for i := 0; i+n <= len(haystack); i++ {
		if haystack[i:i+n] == needle {
			return true
		}
	}
	return false
}
