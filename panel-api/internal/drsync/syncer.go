// Package drsync implements the DR standby pull-restore loop (GH #331 Step 2).
//
// A DR standby is a one-way async replica: it never mutates the primary and
// never holds a primary-mutating credential (least privilege). Its whole job is
// to keep its own control plane converged with the primary's, so that a manual
// `jabali dr promote` (Step 4) brings a near-current box online.
//
// The loop runs on every box but is INERT unless server_settings.server_role is
// 'standby' — it re-reads the role each tick, so pairing/unpairing at runtime
// takes effect without a restart, and a primary pays only one settings read per
// tick. On a standby it:
//
//  1. lists the primary's system_backup manifest snapshots on the read-only DR
//     backup destination (system.restore_list_manifests);
//  2. if the newest snapshot differs from the one it last applied, runs
//     system.restore {apply:true, include_accounts:false} — which loads panel_db
//     + rsyncs panel_config + rsyncs tls, and leaves the heavier account-data
//     stages staged-not-applied (those converge at promote time);
//  3. records the outcome on server_settings (DRLastSync*) for `jabali dr status`.
//
// SECURITY: the destination the standby reads is the same restic repo the
// primary ships to; the standby only ever lists + restores from it. It issues no
// write to the primary and mints no token. A restore OVERWRITES this box's panel
// DB/config/TLS every time a new snapshot lands — which is exactly the point of a
// replica, and why StandbyReadOnly (Step 1) refuses tenant writes here: anything
// written to a standby would be silently discarded by the next converge.
package drsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// settingsStore is the subset of repository.ServerSettingsRepository the loop
// needs. Declared here (accept interfaces) so tests can supply a fake.
type settingsStore interface {
	Get(ctx context.Context) (*models.ServerSettings, error)
	RecordDRSync(ctx context.Context, snapshotID, status, syncErr string) error
	ReassertDRPairing(ctx context.Context, destinationID, peerLabel string, pairedAt *time.Time) error
}

// destinationStore is the subset of repository.BackupDestinationRepository the
// loop needs. GetByName remaps the DR channel after a restore replaced the
// destinations table with the primary's rows (see reassertPairing).
type destinationStore interface {
	Get(ctx context.Context, id string) (*models.BackupDestination, error)
	GetByName(ctx context.Context, name string) (*models.BackupDestination, error)
}

// Deps are the syncer's collaborators. Settings, Destinations, and Agent are
// required; New returns nil without them so serve.go logs and skips the loop.
type Deps struct {
	Settings     settingsStore
	Destinations destinationStore
	Agent        agent.AgentInterface
	// SSOKey unseals a per-destination restic password. May be nil — only
	// needed for destinations that carry a sealed password (WithDestPasswordFile
	// falls back to the agent's shared password file otherwise).
	SSOKey *ssokey.Key
	Log    *slog.Logger

	// Interval between ticks. Zero → DefaultInterval. Ticks are sequential
	// (a slow restore delays the next tick; they never overlap).
	Interval time.Duration
	// TickTimeout bounds one whole SyncOnce (list + restore). Zero →
	// DefaultTickTimeout. system.restore over a slow remote can take minutes,
	// so this is generous; the agent client honours the ctx deadline.
	TickTimeout time.Duration
}

const (
	// DefaultInterval is the standby's convergence cadence. A minute keeps the
	// replica fresh without hammering the destination; restic dedup makes a
	// no-op tick cheap and system.restore is skipped when already current.
	DefaultInterval = 60 * time.Second
	// DefaultTickTimeout bounds one list+restore cycle.
	DefaultTickTimeout = 15 * time.Minute
)

// Syncer runs the standby pull-restore loop.
type Syncer struct {
	deps        Deps
	interval    time.Duration
	tickTimeout time.Duration
	// reassertRetryDelay spaces the ReassertDRPairing attempts after an
	// applied restore (the DB was just reloaded, so one transient failure is
	// plausible). Tests shrink it.
	reassertRetryDelay time.Duration
}

// New returns a Syncer, or nil if a required dependency is missing (so the
// caller can log-and-skip exactly like the backup scheduler does).
func New(deps Deps) *Syncer {
	if deps.Settings == nil || deps.Destinations == nil || deps.Agent == nil {
		return nil
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	interval := deps.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	tt := deps.TickTimeout
	if tt <= 0 {
		tt = DefaultTickTimeout
	}
	return &Syncer{deps: deps, interval: interval, tickTimeout: tt, reassertRetryDelay: 2 * time.Second}
}

// Start runs the loop until ctx is cancelled. Ticks are sequential: the next
// tick waits for the current SyncOnce to return, so a long restore never
// overlaps the next convergence.
func (s *Syncer) Start(ctx context.Context) {
	s.deps.Log.Info("DR sync loop started", "interval", s.interval)
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.interval):
		}
	}
}

// tick runs one SyncOnce under the per-tick timeout and logs the outcome. It
// never propagates — the loop must survive a transient failure.
func (s *Syncer) tick(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, s.tickTimeout)
	defer cancel()
	res, err := s.SyncOnce(ctx)
	if err != nil {
		s.deps.Log.Warn("DR sync tick failed", "err", err)
		return
	}
	if res.Skipped {
		return // primary — inert, no log noise
	}
	switch res.Status {
	case models.DRSyncStatusOK:
		s.deps.Log.Info("DR sync applied a newer snapshot", "snapshot_id", res.SnapshotID)
	case models.DRSyncStatusError:
		s.deps.Log.Warn("DR sync error", "detail", res.Detail)
	default:
		s.deps.Log.Debug("DR sync tick", "status", res.Status, "snapshot_id", res.SnapshotID)
	}
}

// Result reports what one SyncOnce did, for tests and the tick logger.
type Result struct {
	// Skipped is true when the box is not a standby — the loop did nothing and
	// recorded nothing.
	Skipped bool
	// Status is one of models.DRSyncStatus* when a tick ran on a standby.
	Status string
	// SnapshotID is the manifest snapshot applied (ok) or already current.
	SnapshotID string
	// Detail carries the error string for a status=error tick.
	Detail string
}

// SyncOnce runs a single convergence step. It is the testable core of the loop.
// On a primary (or unseeded settings) it returns Result{Skipped:true} and does
// nothing. On a standby it lists + (conditionally) restores, records the
// outcome, and returns it. It returns a non-nil error ONLY for an unexpected
// settings-read failure the caller should log; recorded per-tick errors
// (destination down, restore failure) come back as Result{Status:error}, nil so
// the loop keeps running.
func (s *Syncer) SyncOnce(ctx context.Context) (Result, error) {
	settings, err := s.deps.Settings.Get(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("read server settings: %w", err)
	}
	if settings == nil || !settings.IsStandby() {
		return Result{Skipped: true}, nil
	}
	if settings.DRDestinationID == nil || *settings.DRDestinationID == "" {
		// pair requires a destination, so this is a corrupted pairing.
		return s.recordResult(ctx, Result{
			Status: models.DRSyncStatusError,
			Detail: "standby has no DR destination configured; re-run `jabali dr pair`",
		}), nil
	}
	dest, err := s.deps.Destinations.Get(ctx, *settings.DRDestinationID)
	if err != nil || dest == nil {
		detail := fmt.Sprintf("DR destination %s not found", *settings.DRDestinationID)
		if err != nil {
			detail = fmt.Sprintf("load DR destination %s: %v", *settings.DRDestinationID, err)
		}
		return s.recordResult(ctx, Result{Status: models.DRSyncStatusError, Detail: detail}), nil
	}
	if !dest.Enabled {
		return s.recordResult(ctx, Result{
			Status: models.DRSyncStatusError,
			Detail: fmt.Sprintf("DR destination %s is disabled", dest.ID),
		}), nil
	}

	res := s.pullRestore(ctx, settings, dest)
	return s.recordResult(ctx, res), nil
}

// pullRestore does the two agent calls under a single per-destination password
// tempfile: list the manifests, and (only if the newest differs from the one we
// last applied) restore it with apply=true.
func (s *Syncer) pullRestore(ctx context.Context, settings *models.ServerSettings, dest *models.BackupDestination) Result {
	var res Result
	err := backupwrapperhelpers.WithDestPasswordFile(ctx, dest, s.deps.Agent, s.deps.SSOKey,
		func(passwordFile string) error {
			newest, lerr := s.latestManifest(ctx, dest, passwordFile)
			if lerr != nil {
				if isRepoNotInitialized(lerr) {
					// A DR destination the primary has never shipped to
					// (restic exit 10, "repository does not exist") means
					// the same thing as an initialized-but-empty repo:
					// nothing to pull yet. Surface it as WAITING, not error.
					res = Result{Status: models.DRSyncStatusWaiting}
					return nil
				}
				return lerr
			}
			if newest == "" {
				// Fresh pairing — the primary has shipped no system backup yet.
				res = Result{Status: models.DRSyncStatusWaiting}
				return nil
			}
			if newest == settings.DRLastSnapshotID {
				res = Result{Status: models.DRSyncStatusCurrent, SnapshotID: newest}
				return nil
			}
			if rerr := s.restore(ctx, dest, passwordFile, newest); rerr != nil {
				return rerr
			}
			if perr := s.reassertPairing(ctx, settings, dest); perr != nil {
				return fmt.Errorf("restore %s applied but re-asserting the standby pairing failed "+
					"(this box may now read as a primary — re-run `jabali dr pair`): %w", newest, perr)
			}
			res = Result{Status: models.DRSyncStatusOK, SnapshotID: newest}
			return nil
		})
	if err != nil {
		return Result{Status: models.DRSyncStatusError, Detail: err.Error()}
	}
	return res
}

// isRepoNotInitialized matches the restic signatures for "no repository at
// this location yet" — the not-yet-fed DR channel. Mirrors the missing-repo
// signatures in the agent's classifyRepoProbe. Never matches the unopenable
// cases (wrong password, damaged config), which stay hard errors.
func isRepoNotInitialized(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "repository does not exist") ||
		strings.Contains(msg, "unable to open config file") ||
		strings.Contains(msg, "is there a repository at")
}

// latestManifest asks the agent for the newest system_backup manifest snapshot
// on the destination. Empty string (nil error) means the repo holds none yet.
func (s *Syncer) latestManifest(ctx context.Context, dest *models.BackupDestination, passwordFile string) (string, error) {
	params := destWireParams(dest)
	if passwordFile != "" {
		params["password_file"] = passwordFile
	}
	raw, err := s.deps.Agent.Call(ctx, "system.restore_list_manifests", params)
	if err != nil {
		return "", fmt.Errorf("list manifests: %w", err)
	}
	var resp struct {
		Manifests []struct {
			SnapshotID string `json:"snapshot_id"`
		} `json:"manifests"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", fmt.Errorf("parse manifests: %w", err)
	}
	if resp.Total == 0 || len(resp.Manifests) == 0 {
		return "", nil
	}
	// The agent returns manifests newest-first.
	return resp.Manifests[0].SnapshotID, nil
}

// reassertPairing restores this box's standby identity after an applied
// restore. system.restore loads the PRIMARY's panel DB wholesale, replacing
// server_settings (a row that says role=primary with no pairing) and
// backup_destinations (the primary's rows) — without this step the standby
// silently self-demotes after its first successful sync: the loop reads
// role=primary and goes inert, the reconciler wakes, and StandbyReadOnly
// stops refusing writes. Everything else the restore brought over is
// deliberately kept (that IS the replication); only the four-column DR
// identity block is re-stamped.
//
// The destination is remapped BY NAME: this box's own pre-restore row (and
// its creds env file — panel_config rsyncs with --delete-after) is gone, but
// the shared DR channel exists in the primary's replicated rows under the
// same name. The runbook makes same-name-on-both-boxes a pairing
// prerequisite. If the name lookup misses, the pre-restore ID is kept so the
// next tick surfaces "destination not found" instead of a silent demotion.
func (s *Syncer) reassertPairing(ctx context.Context, prev *models.ServerSettings, prevDest *models.BackupDestination) error {
	destID := prevDest.ID
	if d, err := s.deps.Destinations.GetByName(ctx, prevDest.Name); err == nil && d != nil {
		destID = d.ID
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if lastErr = s.deps.Settings.ReassertDRPairing(ctx, destID, prev.DRPeerLabel, prev.DRPairedAt); lastErr == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return lastErr
		case <-time.After(s.reassertRetryDelay):
		}
	}
	return lastErr
}

// restore runs system.restore for one snapshot with apply=true and
// include_accounts=false (panel_db + panel_config + tls only; account data
// converges at promote time).
func (s *Syncer) restore(ctx context.Context, dest *models.BackupDestination, passwordFile, snapshotID string) error {
	params := destWireParams(dest)
	params["job_id"] = ids.NewULID()
	params["manifest_snapshot_id"] = snapshotID
	params["include_accounts"] = false
	params["apply"] = true
	if passwordFile != "" {
		params["password_file"] = passwordFile
	}
	if _, err := s.deps.Agent.Call(ctx, "system.restore", params); err != nil {
		return fmt.Errorf("system.restore %s: %w", snapshotID, err)
	}
	return nil
}

// recordResult persists a non-skipped Result to server_settings and returns it
// unchanged. Recording uses a fresh short context so an expired per-tick
// deadline (e.g. after a slow restore) can't stop the outcome being written.
func (s *Syncer) recordResult(_ context.Context, res Result) Result {
	rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Only stamp the applied snapshot on success; a failed/waiting tick keeps
	// the last-known applied snapshot intact.
	snap := ""
	if res.Status == models.DRSyncStatusOK {
		snap = res.SnapshotID
	}
	if err := s.deps.Settings.RecordDRSync(rctx, snap, res.Status, res.Detail); err != nil {
		s.deps.Log.Warn("DR sync: record sync state failed", "err", err)
	}
	return res
}

// destWireParams projects a destination into the agent JSON keys. Mirrors
// backupscheduler.destWireParams but stays local so the two packages don't
// couple; SFTPWireParams already yields the full typed SFTP block.
func destWireParams(d *models.BackupDestination) map[string]any {
	out := map[string]any{
		"repo_url":         d.URL,
		"destination_kind": d.Kind,
		"sftp":             backupwrapperhelpers.SFTPWireParams(d),
	}
	if d.CredentialsRef != nil {
		out["credentials_ref"] = *d.CredentialsRef
	}
	return out
}
