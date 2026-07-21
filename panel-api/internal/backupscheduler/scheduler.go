// Package backupscheduler — M30.1 in-process scheduler. Ticks every
// 60s, scans backup_schedules.next_run_at <= now, dispatches each
// overdue row to panel-agent via the same backup.create call the REST
// handler uses.
//
// Why in-process instead of a systemd timer + CLI tick (like the M30
// retention sweep)? At 60s cadence the cobra cold-start cost dominates,
// and we already have several goroutine-based tickers (reconciler, M14
// notification dispatcher, eventsources). Missed ticks during panel
// restart are tolerated — the schedules stay overdue and the next tick
// picks them up.
//
// One backup at a time per (kind, user-id) is enforced by the agent;
// the scheduler just dispatches and lets the agent return 409 Conflict
// when the prior job is still running. We log + skip on conflict.
package backupscheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupmetadata"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// EnqueueInterval is the cadence at which due schedules are scanned and
// enqueued as backup_jobs. Bigger than 60s loses scheduling precision;
// smaller burns DB queries with no operator value.
const EnqueueInterval = 60 * time.Second

// DispatchInterval is the cadence at which queued backup_jobs are
// drained under the concurrency limit. Smaller = quicker reaction when
// a slot frees, but every tick is one settings-fetch + count query.
const DispatchInterval = 10 * time.Second

// MaxDuePerTick caps how many overdue schedules one tick will enqueue.
// Bigger = potential DB pressure on the join queries; smaller = recovery
// from a long pause takes more ticks. 50 fits a 5-minute outage on a
// 1k-user host.
const MaxDuePerTick = 50

// DefaultMaxConcurrent is the fallback when server_settings is missing
// or zero. 2 keeps a fast disk busy without melting restic on a wide
// fan-out.
const DefaultMaxConcurrent = 2

// Deps is the dependency bundle passed by serve.go.
type Deps struct {
	Schedules      repository.BackupScheduleRepository
	Jobs           repository.BackupJobRepository
	Destinations   repository.BackupDestinationRepository
	Users          repository.UserRepository
	Databases      repository.DatabaseRepository
	DatabaseUsers  repository.DatabaseUserRepository
	DatabaseGrants repository.DatabaseUserGrantRepository
	Domains        repository.DomainRepository
	Mailboxes      repository.MailboxRepository
	AppInstalls    repository.ApplicationInstallRepository
	Settings       repository.ServerSettingsRepository
	// Packages + Notify (GH #454) enforce the per-package retention cap on
	// SCHEDULED backups too — the on-demand path already does. Both OPTIONAL:
	// nil Packages skips the cap check entirely (pre-#454 behaviour); nil Notify
	// just omits the tenant/admin notice.
	Packages repository.PackageRepository
	Notify   *notifications.Queue

	// Schema-v2 metadata producer fan-out — every nullable repo here is
	// queried by buildScheduleMetadata so scheduled backups carry the
	// same per-user state as manual admin / self-shell jobs.
	SSLCerts       repository.SSLCertificateRepository
	PHPPools       repository.PHPPoolRepository
	PHPPoolIni     repository.PHPPoolIniOverrideRepository
	Forwarders     repository.EmailForwarderRepository
	Autoresponders repository.EmailAutoresponderRepository
	MailboxShares  repository.MailboxShareRepository
	DNSSECKeys     repository.DNSSECKeyRepository
	DNSZones       repository.DNSZoneRepository
	DNSRecords     repository.DNSRecordRepository
	SSHKeys        repository.SSHKeyRepository
	CronJobs       repository.CronJobRepository
	LimitOverrides repository.UserLimitOverrideRepository
	EgressPolicies repository.UserEgressPolicyRepository
	EgressRequests repository.UserEgressRequestRepository

	Agent agent.AgentInterface

	// M30.2.x — sso key for unsealing per-destination restic
	// passwords. Optional; nil falls back to the legacy shared file.
	SSOKey *ssokey.Key
	Log    *slog.Logger
}

// Scheduler is the goroutine wrapper. Construct via New, start with
// Start(ctx). Returns immediately; the loop runs until ctx is done.
type Scheduler struct{ deps Deps }

// New returns a configured scheduler. Returns nil if any required dep
// is missing — callers log + skip start in that case so an incomplete
// deployment doesn't crash on boot.
func New(deps Deps) *Scheduler {
	if deps.Schedules == nil || deps.Jobs == nil || deps.Destinations == nil ||
		deps.Users == nil || deps.Agent == nil || deps.Log == nil {
		return nil
	}
	return &Scheduler{deps: deps}
}

// Start runs the enqueue + dispatch loops until ctx is done. Two
// tickers because a 60s enqueue cadence is too slow to react when a
// dispatch slot frees up — drains run every 10s.
func (s *Scheduler) Start(ctx context.Context) {
	enqueueT := time.NewTicker(EnqueueInterval)
	dispatchT := time.NewTicker(DispatchInterval)
	defer enqueueT.Stop()
	defer dispatchT.Stop()
	s.deps.Log.Info("backup scheduler started",
		"enqueue_interval", EnqueueInterval,
		"dispatch_interval", DispatchInterval)
	// Run one of each immediately so a panel restart doesn't leave
	// overdue schedules waiting up to 60s for the next firing.
	s.tickEnqueue(ctx)
	s.tickDispatch(ctx)
	for {
		select {
		case <-ctx.Done():
			s.deps.Log.Info("backup scheduler stopped")
			return
		case <-enqueueT.C:
			s.tickEnqueue(ctx)
		case <-dispatchT.C:
			s.tickDispatch(ctx)
		}
	}
}

// TickOnce runs one enqueue + dispatch pass synchronously. Exposed
// for the `jabali backup scheduler tick` CLI subcommand so an
// operator can manually poke the scheduler without waiting for the
// next 60s + 10s ticks. Production path stays the long-running
// Start goroutine — TickOnce is for ops debugging + bootstrap-
// script tests.
func (s *Scheduler) TickOnce(ctx context.Context) {
	s.tickEnqueue(ctx)
	s.tickDispatch(ctx)
}

func (s *Scheduler) tickEnqueue(ctx context.Context) {
	now := time.Now().UTC()
	due, err := s.deps.Schedules.ListDue(ctx, now, MaxDuePerTick)
	if err != nil {
		s.deps.Log.Error("scheduler list-due failed", "err", err)
		return
	}
	for _, sched := range due {
		s.enqueue(ctx, sched)
	}
}

// tickDispatch reads the current concurrency limit, counts running
// jobs, and dispatches queued rows to fill the gap.
func (s *Scheduler) tickDispatch(ctx context.Context) {
	max := uint32(DefaultMaxConcurrent)
	if s.deps.Settings != nil {
		set, err := s.deps.Settings.Get(ctx)
		if err == nil && set != nil && set.BackupMaxConcurrentJobs > 0 {
			max = set.BackupMaxConcurrentJobs
		}
	}
	running, err := s.deps.Jobs.CountByStatus(ctx, models.BackupJobStatusRunning)
	if err != nil {
		s.deps.Log.Error("dispatcher count-running failed", "err", err)
		return
	}
	slots := int(max) - int(running)
	if slots <= 0 {
		return
	}
	queued, err := s.deps.Jobs.ListQueuedOldest(ctx, slots)
	if err != nil {
		s.deps.Log.Error("dispatcher list-queued failed", "err", err)
		return
	}
	for _, j := range queued {
		s.dispatchOne(ctx, j)
	}
}

// enqueue handles one overdue schedule end-to-end: compute next
// firing, create queued backup_jobs rows for each fan-out target, mark
// the schedule ran. The dispatcher tick then drains the queue.
func (s *Scheduler) enqueue(ctx context.Context, sched models.BackupSchedule) {
	logger := s.deps.Log.With(
		"schedule_id", sched.ID,
		"kind", sched.Kind,
		"user_id", strDeref(sched.UserID),
	)

	next, err := internalbackup.NextFire(sched.CronExpr, time.Now().UTC())
	if err != nil {
		logger.Error("invalid cron in DB; disabling for one tick", "cron_expr", sched.CronExpr, "err", err)
		// Push next_run_at far enough out that we don't re-process this
		// row every tick. Operator must fix the cron via UI.
		bump := time.Now().UTC().Add(24 * time.Hour)
		_ = s.deps.Schedules.UpdateNextRun(ctx, sched.ID, bump)
		return
	}

	enqueued, qErr := s.enqueueBackup(ctx, sched)
	if qErr != nil {
		logger.Error("scheduler enqueue failed", "err", qErr)
		// Still advance next_run_at so we don't tight-loop on a
		// permanent failure (e.g. user deleted but FK didn't cascade).
		_ = s.deps.Schedules.UpdateNextRun(ctx, sched.ID, next)
		return
	}
	if !enqueued {
		// No fan-out targets — advance next_run_at without marking ran.
		_ = s.deps.Schedules.UpdateNextRun(ctx, sched.ID, next)
		return
	}

	if err := s.deps.Schedules.MarkRan(ctx, sched.ID, time.Now().UTC(), next); err != nil {
		logger.Error("schedule mark-ran failed", "err", err)
	}
}

// enqueueBackup creates queued backup_jobs rows for the given
// schedule. Per ADR-0080 each linked destination gets its own row
// (no source-then-mirror); the dispatcher tick later picks them up
// and calls the agent under the concurrency cap. All rows from one
// tick share a run_id so the admin UI can roll them up under a
// single header.
func (s *Scheduler) enqueueBackup(ctx context.Context, sched models.BackupSchedule) (bool, error) {
	runID := ids.NewULID()
	dests, err := s.deps.Schedules.GetDestinations(ctx, sched.ID)
	if err != nil {
		return false, fmt.Errorf("load schedule destinations: %w", err)
	}
	if len(dests) == 0 {
		s.deps.Log.Warn("schedule has no destinations linked; skipping",
			"schedule_id", sched.ID)
		return false, nil
	}
	// Filter out disabled destinations — leaving them in would create
	// jobs the dispatcher would then mark failed. Cleaner to drop here.
	enabledDests := dests[:0]
	for _, d := range dests {
		if d.Enabled {
			enabledDests = append(enabledDests, d)
		}
	}
	if len(enabledDests) == 0 {
		s.deps.Log.Warn("schedule destinations are all disabled; skipping",
			"schedule_id", sched.ID)
		return false, nil
	}
	switch sched.Kind {
	case models.BackupScheduleKindAccount:
		anyUser := false
		explicitIDs, err := s.deps.Schedules.GetUserIDs(ctx, sched.ID)
		if err != nil {
			return false, fmt.Errorf("load schedule users: %w", err)
		}
		var targets []models.User
		if len(explicitIDs) == 0 {
			notAdmin := false
			users, _, err := s.deps.Users.List(ctx, repository.ListOptions{
				Limit:   10000,
				IsAdmin: &notAdmin,
			})
			if err != nil {
				return false, fmt.Errorf("list users: %w", err)
			}
			targets = users
		} else {
			for _, uid := range explicitIDs {
				u, err := s.deps.Users.FindByID(ctx, uid)
				if err != nil || u == nil {
					s.deps.Log.Warn("schedule references missing user; skipping",
						"schedule_id", sched.ID, "user_id", uid)
					continue
				}
				if u.IsAdmin {
					s.deps.Log.Warn("schedule references admin; skipping",
						"schedule_id", sched.ID, "user_id", uid)
					continue
				}
				targets = append(targets, *u)
			}
		}
		for i := range targets {
			u := &targets[i]
			for j := range enabledDests {
				if ok := s.enqueueAccountBackup(ctx, sched, u, &enabledDests[j], runID); ok {
					anyUser = true
				}
			}
		}
		anySystem := false
		if sched.IncludeSystemBackup {
			for j := range enabledDests {
				if ok := s.enqueueSystemBackup(ctx, sched, &enabledDests[j], runID); ok {
					anySystem = true
				}
			}
		}
		return anyUser || anySystem, nil

	case models.BackupScheduleKindSystem:
		any := false
		for j := range enabledDests {
			if ok := s.enqueueSystemBackup(ctx, sched, &enabledDests[j], runID); ok {
				any = true
			}
		}
		return any, nil

	default:
		return false, fmt.Errorf("unknown schedule kind %q", sched.Kind)
	}
}

// enqueueAccountBackup creates one backup_jobs row in status=queued
// for the given (user, destination) pair. Per ADR-0080 the destination
// is a first-class field on backup_jobs.
func (s *Scheduler) enqueueAccountBackup(ctx context.Context, sched models.BackupSchedule, user *models.User, dest *models.BackupDestination, runID string) bool {
	logger := s.deps.Log.With("schedule_id", sched.ID, "user_id", user.ID, "destination_id", dest.ID, "run_id", runID)
	// Retention cap (GH #454): honour the tenant's package policy on scheduled
	// backups, matching the on-demand path. Only enforced when the package repo
	// is wired AND the plan actually caps backups; otherwise the scheduled
	// backup proceeds unchanged (no regression for uncapped/unresolvable plans).
	if s.deps.Packages != nil {
		if pkg := s.userPackage(ctx, user); pkg != nil && pkg.BackupsEnabled() && s.atOrOverCap(ctx, user.ID, int(pkg.MaxBackups)) {
			if pkg.BackupRetentionPrunes() && s.pruneOldestForUser(ctx, user, int(pkg.MaxBackups)) {
				s.notifyBackupLimit(ctx, user, "prune", pkg.MaxBackups)
			} else {
				s.notifyBackupLimit(ctx, user, "reject", pkg.MaxBackups)
				logger.Warn("scheduled backup skipped: retention cap reached", "policy", pkg.BackupRetentionPolicy, "max_backups", pkg.MaxBackups)
				return false
			}
		}
	}
	schedID := sched.ID
	rid := runID
	dID := dest.ID
	job := &models.BackupJob{
		ID:            ids.NewULID(),
		UserID:        user.ID,
		DestinationID: &dID,
		ScheduleID:    &schedID,
		RunID:         &rid,
		Kind:          models.BackupJobKindAccountBackup,
		Status:        models.BackupJobStatusQueued,
		// Denormalise the schedule's content onto the job so the dispatcher
		// trims the db/mailbox lists to match without re-loading the schedule
		// (GH #454). "" normalises to "full" = the pre-feature behaviour.
		Content:   models.NormalizeBackupContent(sched.Content),
		CreatedAt: time.Now().UTC(),
	}
	if err := s.deps.Jobs.Create(ctx, job); err != nil {
		logger.Error("create backup_job failed", "err", err)
		return false
	}
	return true
}

// enqueueSystemBackup creates one backup_jobs row in status=queued
// for a system backup against the given destination.
// userPackage resolves the tenant's hosting package for the retention cap
// check, or nil (no package / lookup error -> the caller treats it as uncapped,
// so a transient error never blocks a scheduled run) (GH #454).
func (s *Scheduler) userPackage(ctx context.Context, user *models.User) *models.HostingPackage {
	if user.PackageID == nil || s.deps.Packages == nil {
		return nil
	}
	pkg, err := s.deps.Packages.FindByID(ctx, *user.PackageID)
	if err != nil {
		return nil
	}
	return pkg
}

// atOrOverCap reports whether the user already holds >= maxBackups jobs.
func (s *Scheduler) atOrOverCap(ctx context.Context, userID string, maxBackups int) bool {
	_, total, err := s.deps.Jobs.ListForUser(ctx, userID, 1, 0)
	if err != nil {
		return false
	}
	return total >= int64(maxBackups)
}

// pruneOldestForUser forgets the user's OLDEST OWNED account_backup(s) until
// under maxBackups. Owner-scoped: OldestAccountBackupForUser filters by
// user_id, so a scheduled prune only ever touches the target tenant's own
// backups. Returns true once under the cap (GH #454).
func (s *Scheduler) pruneOldestForUser(ctx context.Context, user *models.User, maxBackups int) bool {
	for i := 0; i < 1000; i++ {
		if !s.atOrOverCap(ctx, user.ID, maxBackups) {
			return true
		}
		oldest, err := s.deps.Jobs.OldestAccountBackupForUser(ctx, user.ID)
		if err != nil || oldest == nil {
			return false
		}
		if s.deps.Agent != nil {
			fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, _ = s.deps.Agent.Call(fctx, "backup.forget", map[string]any{
				"job_id": oldest.ID, "user_id": oldest.UserID, "kind": oldest.Kind,
			})
			cancel()
		}
		if derr := s.deps.Jobs.Delete(ctx, oldest.ID); derr != nil {
			return false
		}
	}
	return false
}

// notifyBackupLimit fires backup.limit.reached to the tenant's inbox
// (Envelope.UserID) and broadcasts to admins (UserID empty), tailored to the
// outcome. Best-effort (GH #454).
func (s *Scheduler) notifyBackupLimit(ctx context.Context, user *models.User, outcome string, limit uint32) {
	if s.deps.Notify == nil {
		return
	}
	who := user.ID
	if user.Username != nil && *user.Username != "" {
		who = *user.Username
	}
	var tenantBody, adminBody string
	if outcome == "prune" {
		tenantBody = fmt.Sprintf("Your scheduled backup reached your plan's limit (%d); the oldest backup was automatically removed to make room.", limit)
		adminBody = fmt.Sprintf("Scheduled backup for tenant %s hit the backup limit (%d); the oldest was auto-pruned per the package retention policy.", who, limit)
	} else {
		tenantBody = fmt.Sprintf("Your scheduled backup was skipped: you reached your plan's backup limit (%d). Delete an old backup or ask your host to raise the limit.", limit)
		adminBody = fmt.Sprintf("Scheduled backup for tenant %s was skipped — backup limit (%d) reached (reject policy).", who, limit)
	}
	nctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _ = s.deps.Notify.Publish(nctx, notifications.Envelope{
		EventKind: "backup.limit.reached", Severity: "warning",
		Title: "Scheduled backup limit reached", Body: tenantBody, UserID: user.ID,
	})
	_, _ = s.deps.Notify.Publish(nctx, notifications.Envelope{
		EventKind: "backup.limit.reached", Severity: "warning",
		Title: "Tenant scheduled backup limit reached", Body: adminBody,
	})
}

func (s *Scheduler) enqueueSystemBackup(ctx context.Context, sched models.BackupSchedule, dest *models.BackupDestination, runID string) bool {
	logger := s.deps.Log.With("schedule_id", sched.ID, "kind", "system_backup", "destination_id", dest.ID, "run_id", runID)
	schedID := sched.ID
	rid := runID
	dID := dest.ID
	job := &models.BackupJob{
		ID:            ids.NewULID(),
		UserID:        "system",
		DestinationID: &dID,
		ScheduleID:    &schedID,
		RunID:         &rid,
		Kind:          models.BackupJobKindSystemBackup,
		Status:        models.BackupJobStatusQueued,
		CreatedAt:     time.Now().UTC(),
	}
	if err := s.deps.Jobs.Create(ctx, job); err != nil {
		logger.Error("create system backup_job failed", "err", err)
		return false
	}
	return true
}

// dispatchOne sends one queued job to the agent and flips it to
// running. Errors are logged + the row is marked failed so the
// finalizer doesn't leave it stuck in queued forever.
func (s *Scheduler) dispatchOne(ctx context.Context, j models.BackupJob) {
	switch j.Kind {
	case models.BackupJobKindAccountBackup:
		s.dispatchAccount(ctx, j)
	case models.BackupJobKindSystemBackup:
		s.dispatchSystem(ctx, j)
	default:
		s.deps.Log.Warn("dispatcher: unknown job kind, skipping",
			"job_id", j.ID, "kind", j.Kind)
	}
}

func (s *Scheduler) dispatchAccount(ctx context.Context, j models.BackupJob) {
	logger := s.deps.Log.With("job_id", j.ID, "user_id", j.UserID)
	user, err := s.deps.Users.FindByID(ctx, j.UserID)
	if err != nil || user == nil {
		_ = s.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusFailed,
			"", "", 0, 0, nil, nil, "user not found at dispatch time")
		logger.Warn("dispatcher: user lookup failed; marking failed", "err", err)
		return
	}
	dest, derr := s.loadDestination(ctx, j)
	if derr != nil {
		_ = s.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusFailed,
			"", "", 0, 0, nil, nil, "destination lookup: "+derr.Error())
		logger.Error("dispatcher: destination lookup failed", "err", derr)
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	dbs := userDatabases(callCtx, s.deps, user.ID, logger)
	pgDbs := userPostgresDatabases(callCtx, s.deps, user.ID, logger)
	mbs := userMailboxes(callCtx, s.deps, user.ID, logger)
	// Honour the schedule's content selection (GH #454): the agent skips the
	// home stage only for content=database but backs up whatever db/mailbox
	// lists it is handed, so trim the lists the content excludes here and pass
	// the selector through. Empty content -> "full" -> everything (unchanged).
	content := models.NormalizeBackupContent(j.Content)
	dbs, mbs, pgDbs = models.ApplyBackupContent(content, dbs, mbs, pgDbs)
	meta := buildScheduleMetadata(callCtx, s.deps, user, logger)
	scheduleID := ""
	if j.ScheduleID != nil {
		scheduleID = *j.ScheduleID
	}
	params := map[string]any{
		"job_id":             j.ID,
		"user_id":            user.ID,
		"username":           user.Username,
		"email":              user.Email,
		"is_admin":           user.IsAdmin,
		"databases":          dbs,
		"databases_postgres": pgDbs,
		"mailboxes":          mbs,
		"content":            content,
		"metadata":           meta,
		"schedule_id":        scheduleID,
	}
	for k, v := range destWireParams(dest) {
		params[k] = v
	}
	err = backupwrapperhelpers.WithDestPasswordFile(callCtx, dest, s.deps.Agent, s.deps.SSOKey,
		func(passwordFile string) error {
			if passwordFile != "" {
				params["password_file"] = passwordFile
			}
			_, callErr := s.deps.Agent.Call(callCtx, "backup.create", params)
			return callErr
		})
	if err != nil {
		if isAgentConflict(err) {
			_ = s.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusCancelled,
				"", "", 0, 0, nil, nil, "skipped: prior backup still running")
			return
		}
		_ = s.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusFailed,
			"", "", 0, 0, nil, nil, err.Error())
		logger.Error("agent backup.create failed", "err", err)
		return
	}
	_ = s.deps.Jobs.MarkStarted(ctx, j.ID)
}

func (s *Scheduler) dispatchSystem(ctx context.Context, j models.BackupJob) {
	logger := s.deps.Log.With("job_id", j.ID, "kind", "system_backup")
	dest, derr := s.loadDestination(ctx, j)
	if derr != nil {
		_ = s.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusFailed,
			"", "", 0, 0, nil, nil, "destination lookup: "+derr.Error())
		logger.Error("dispatcher: destination lookup failed", "err", derr)
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	scheduleID := ""
	if j.ScheduleID != nil {
		scheduleID = *j.ScheduleID
	}
	params := map[string]any{
		"job_id":           j.ID,
		"include_accounts": false,
		"schedule_id":      scheduleID,
	}
	for k, v := range destWireParams(dest) {
		params[k] = v
	}
	if _, err := s.deps.Agent.Call(callCtx, "system.backup", params); err != nil {
		if isAgentConflict(err) {
			_ = s.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusCancelled,
				"", "", 0, 0, nil, nil, "skipped: prior system backup still running")
			return
		}
		_ = s.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusFailed,
			"", "", 0, 0, nil, nil, err.Error())
		logger.Error("agent system.backup failed", "err", err)
		return
	}
	_ = s.deps.Jobs.MarkStarted(ctx, j.ID)
}

// isAgentConflict matches the agent's 409-equivalent NDJSON error string
// for "another job already running". Cheap heuristic — cleaner than a
// typed sentinel that would require coordinating both packages. False
// positives only delay one tick, no data loss.
func isAgentConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "already running") ||
		strings.Contains(msg, "conflict") ||
		strings.Contains(msg, "job_already_active")
}

// userDatabases returns the names of every hosted database belonging
// to the user. Errors are logged + an empty slice returned so a
// transient DB hiccup in the scheduler doesn't abort the whole tick.
//
// M37: returns mariadb-engine names only. Postgres siblings come back
// from userPostgresDatabases() and ride backup.create as a parallel
// `databases_postgres` field.
func userDatabases(ctx context.Context, deps Deps, userID string, logger *slog.Logger) []string {
	return userDatabasesByEngine(ctx, deps, userID, "mariadb", logger)
}

func userPostgresDatabases(ctx context.Context, deps Deps, userID string, logger *slog.Logger) []string {
	return userDatabasesByEngine(ctx, deps, userID, "postgres", logger)
}

func userDatabasesByEngine(ctx context.Context, deps Deps, userID, engine string, logger *slog.Logger) []string {
	if deps.Databases == nil {
		return nil
	}
	dbs, _, err := deps.Databases.ListByUserID(ctx, userID, repository.ListOptions{Limit: 10000})
	if err != nil {
		logger.Warn("list databases for backup failed", "err", err, "engine", engine)
		return nil
	}
	out := make([]string, 0, len(dbs))
	for _, d := range dbs {
		if d.Engine == engine || (engine == "mariadb" && d.Engine == "") {
			out = append(out, d.Name)
		}
	}
	return out
}

// buildScheduleMetadata is the scheduler's wrapper around the shared
// schema-v2 producer in panel-api/internal/backupmetadata. Keeps the
// scheduler in lockstep with the admin / user-shell handlers so every
// backup carries the same per-user state regardless of trigger path.
func buildScheduleMetadata(ctx context.Context, deps Deps, user *models.User, logger *slog.Logger) *internalbackup.AccountMetadata {
	return backupmetadata.Build(ctx, user, backupmetadata.Deps{
		Databases:      deps.Databases,
		DatabaseUsers:  deps.DatabaseUsers,
		DatabaseGrants: deps.DatabaseGrants,
		Domains:        deps.Domains,
		Mailboxes:      deps.Mailboxes,
		AppInstalls:    deps.AppInstalls,
		SSLCerts:       deps.SSLCerts,
		PHPPools:       deps.PHPPools,
		PHPPoolIni:     deps.PHPPoolIni,
		Forwarders:     deps.Forwarders,
		Autoresponders: deps.Autoresponders,
		MailboxShares:  deps.MailboxShares,
		DNSSECKeys:     deps.DNSSECKeys,
		DNSZones:       deps.DNSZones,
		DNSRecords:     deps.DNSRecords,
		SSHKeys:        deps.SSHKeys,
		CronJobs:       deps.CronJobs,
		LimitOverrides: deps.LimitOverrides,
		EgressPolicies: deps.EgressPolicies,
		EgressRequests: deps.EgressRequests,
		Log:            logger,
	})
}

// userMailboxes returns the email addresses of every mailbox under
// every domain owned by the user. Two-step: domain.list_by_user →
// mailbox.list_by_domain. Errors per-domain are tolerated.
func userMailboxes(ctx context.Context, deps Deps, userID string, logger *slog.Logger) []string {
	if deps.Domains == nil || deps.Mailboxes == nil {
		return nil
	}
	doms, _, err := deps.Domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: 10000})
	if err != nil {
		logger.Warn("list domains for backup failed", "err", err)
		return nil
	}
	var out []string
	for _, d := range doms {
		mbs, _, err := deps.Mailboxes.ListByDomainID(ctx, d.ID, repository.ListOptions{Limit: 10000})
		if err != nil {
			logger.Warn("list mailboxes for backup failed",
				"domain_id", d.ID, "err", err)
			continue
		}
		for _, m := range mbs {
			out = append(out, m.EmailCached)
		}
	}
	return out
}

func strDerefOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// loadDestination resolves the destination row a queued job points at.
// Legacy rows (pre-ADR-0080) carry a NULL destination_id and are
// rejected — the operator must re-create the schedule with explicit
// destinations linked.
func (s *Scheduler) loadDestination(ctx context.Context, j models.BackupJob) (*models.BackupDestination, error) {
	if j.DestinationID == nil || *j.DestinationID == "" {
		return nil, fmt.Errorf("legacy job without destination_id (pre-ADR-0080); re-create the schedule")
	}
	d, err := s.deps.Destinations.Get(ctx, *j.DestinationID)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, fmt.Errorf("destination %s not found", *j.DestinationID)
	}
	if !d.Enabled {
		return nil, fmt.Errorf("destination %s disabled", d.ID)
	}
	return d, nil
}

// destWireParams projects a destination into the JSON keys the agent
// backup commands accept. Centralised so account+system dispatch stay
// in lockstep.
func destWireParams(d *models.BackupDestination) map[string]any {
	out := map[string]any{
		"repo_url":         d.URL,
		"destination_kind": d.Kind,
		"extra_options":    backupwrapperhelpers.ResticOptionsFor(d),
	}
	if d.CredentialsRef != nil {
		out["credentials_ref"] = *d.CredentialsRef
	}
	if d.Kind == models.BackupDestinationKindSFTP {
		if s := d.ExtraOptionsTyped().SFTP; s != nil {
			out["sftp"] = map[string]any{
				"host":     s.Host,
				"user":     s.User,
				"port":     s.Port,
				"path":     s.Path,
				"auth":     s.Auth,
				"key_path": s.KeyPath,
			}
		}
	}
	return out
}
