package drsync

import (
	"context"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// GH #1169 (deferred #331 blueprint step 5) — dr.sync.stalled.
//
// A DR standby that silently stops applying snapshots is worthless at promote
// time, and today its staleness is visible only via `jabali dr status` on the
// standby itself. This raises the dr.sync.stalled notification the moment the
// replica falls behind, so the operator learns about it BEFORE the disaster.

// staleBaseline picks the reference time a standby's freshness is measured from:
// the last applied snapshot, or — before the first one ever lands — when it was
// paired. Returns (time, true) when a baseline exists.
func staleBaseline(s *models.ServerSettings) (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	if s.DRLastSyncAt != nil {
		return *s.DRLastSyncAt, true
	}
	if s.DRPairedAt != nil {
		return *s.DRPairedAt, true
	}
	return time.Time{}, false
}

// isStalled reports whether `now` is more than `threshold` past the baseline.
// Pure, so the staleness decision is unit-tested without a clock or DB.
func isStalled(baseline time.Time, hasBaseline bool, now time.Time, threshold time.Duration) bool {
	if !hasBaseline || threshold <= 0 {
		return false
	}
	return now.Sub(baseline) > threshold
}

// checkStall evaluates staleness after a tick and publishes dr.sync.stalled once
// per episode (reset when a fresh sync brings the replica back under threshold).
// Best-effort: a settings-read or publish error is logged, never fatal — the
// loop must keep converging.
func (s *Syncer) checkStall(ctx context.Context) {
	if s.deps.Notify == nil {
		return
	}
	settings, err := s.deps.Settings.Get(ctx)
	if err != nil || settings == nil || !settings.IsStandby() {
		return
	}
	baseline, ok := staleBaseline(settings)
	if !isStalled(baseline, ok, time.Now(), s.stalledAfter) {
		s.stalledAlerted = false // fresh (or no baseline) → re-arm the alert
		return
	}
	if s.stalledAlerted {
		return // already alerted this episode
	}
	age := time.Since(baseline).Round(time.Second)
	peer := settings.DRPeerLabel
	if peer == "" {
		peer = "unlabelled"
	}
	env := notifications.Envelope{
		EventKind: "dr.sync.stalled",
		Severity:  "error",
		Title:     "DR standby sync stalled",
		Body: fmt.Sprintf("This DR standby (peer %s) has not applied a fresh snapshot in %s "+
			"(threshold %s). The replica is going stale — check the DR destination and "+
			"`jabali dr status` before you need to promote.", peer, age, s.stalledAfter),
		Deeplink: "/jabali-admin/settings",
	}
	if _, perr := s.deps.Notify.Publish(ctx, env); perr != nil {
		s.deps.Log.Warn("DR stall alert publish failed", "err", perr)
		return
	}
	s.stalledAlerted = true
	s.deps.Log.Warn("DR sync stalled — alert published", "age", age.String(), "threshold", s.stalledAfter.String())
}
