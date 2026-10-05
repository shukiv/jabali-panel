// Package reconciler — M47 Wave 3 outbound-throttle convergence.
//
// Reads mail_outbound_policy on each tick and makes Stalwart's
// MtaOutboundThrottle objects match, through Stalwart's management API with
// the panel's admin token (stalwartadmin.Throttles). Each row has an hourly
// and a daily window, and each window is its own state machine:
//
//	enabled && cap > 0   → apply: create the object, update it, or leave it
//	                       alone, and keep the id it owns
//	otherwise, id != ''  → delete, clear the id
//	otherwise            → no-op
//
// A failure keeps the window's id and stamps last_error, so the next tick
// retries. Self-healing.
//
// After the rows, a sweep removes the panel's throttles (description starts
// with mailthrottle.OwnedPrefix) that no row references any more.
package reconciler

import (
	"context"
	"errors"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/mailthrottle"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ThrottleApplier pushes throttle windows into Stalwart. The panel's
// implementation is stalwartadmin.Throttles; tests inject a fake.
type ThrottleApplier interface {
	Apply(ctx context.Context, req mailthrottle.ApplyRequest) (mailthrottle.ApplyResult, error)
	Delete(ctx context.Context, stalwartID string) error
	List(ctx context.Context) ([]mailthrottle.ListItem, error)
}

const mailThrottleCallTimeout = 30 * time.Second

// reconcileMailThrottles converges every mail_outbound_policy row.
// Called from the main reconcile loop on each tick. Self-disables
// when the policy repo or the applier isn't wired.
func (r *Reconciler) reconcileMailThrottles(ctx context.Context) {
	if r.outboundPolicies == nil || r.mailThrottles == nil {
		return
	}
	// GH #357: a box without the mail module has no Stalwart to hold
	// throttles; every pass logged a failed token read. Skip on a positive
	// "mail off" reading only — an unreadable settings row keeps the pass.
	if r.serverSettings != nil {
		sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
		srv, err := r.settingsGet(sctx)
		scancel()
		if err == nil && srv != nil && !srv.MailEnabled {
			return
		}
	}
	// The admin reconcile endpoint can start a full pass while the ticker's
	// is still running. Two throttle passes at once could each create a
	// window's throttle, or one could sweep a throttle the other created but
	// has not stamped yet.
	if !r.mailThrottleMu.TryLock() {
		return
	}
	defer r.mailThrottleMu.Unlock()
	rows, err := r.outboundPolicies.List(ctx)
	if err != nil {
		r.log.Warn("mail-throttle: list failed", "err", err)
		return
	}
	stamped := true
	for i := range rows {
		if !r.reconcileMailThrottleOne(ctx, &rows[i]) {
			stamped = false
		}
	}
	// A throttle whose id could not be stamped is referenced by no row yet;
	// sweeping now would delete it and the next tick would create it again.
	if stamped {
		r.sweepMailThrottles(ctx)
	}
}

// sweepMailThrottles removes the panel's Stalwart throttles that no row
// references: one created this tick for a row an admin deleted meanwhile,
// or one an earlier release left behind. It reads the rows again, after the
// apply phase, so the first case is caught in the same tick. It does nothing
// if either list fails, because an unreadable table must not look like an
// empty one. Throttles whose description lacks mailthrottle.OwnedPrefix are
// someone else's and are never touched.
func (r *Reconciler) sweepMailThrottles(ctx context.Context) {
	rows, err := r.outboundPolicies.List(ctx)
	if err != nil {
		r.log.Warn("mail-throttle: sweep skipped, list rows failed", "err", err)
		return
	}
	referenced := map[string]bool{}
	for _, row := range rows {
		referenced[row.StalwartID] = true
		referenced[row.StalwartIDDaily] = true
	}
	cctx, cancel := context.WithTimeout(ctx, mailThrottleCallTimeout)
	defer cancel()
	items, err := r.mailThrottles.List(cctx)
	if err != nil {
		r.log.Warn("mail-throttle: sweep skipped, list Stalwart throttles failed", "err", err)
		return
	}
	for _, it := range items {
		if referenced[it.StalwartID] || !strings.HasPrefix(it.Description, mailthrottle.OwnedPrefix) {
			continue
		}
		if err := r.mailThrottles.Delete(cctx, it.StalwartID); err != nil {
			r.log.Warn("mail-throttle: removing unreferenced throttle failed", "stalwart_id", it.StalwartID, "err", err)
			continue
		}
		r.log.Info("mail-throttle: removed unreferenced throttle", "stalwart_id", it.StalwartID, "description", it.Description)
	}
}

// reconcileMailThrottleOne converges both windows of one row, then stamps
// both ids and one last_error for the row. Stamping once per row keeps a
// failing window's error from being wiped by the other window's success.
// Nothing is written when nothing changed. It reports false when a stamp
// failed.
func (r *Reconciler) reconcileMailThrottleOne(ctx context.Context, row *models.MailOutboundPolicy) bool {
	hourID, hourErr := r.reconcileThrottleWindow(ctx, row, mailthrottle.WindowHour)
	dayID, dayErr := r.reconcileThrottleWindow(ctx, row, mailthrottle.WindowDay)

	var lastErr *string
	if err := errors.Join(hourErr, dayErr); err != nil {
		msg := err.Error()
		lastErr = &msg
		r.log.Warn("mail-throttle: apply failed", "row", row.ID, "err", err)
	}
	if hourID == row.StalwartID && dayID == row.StalwartIDDaily && lastErr == nil && row.LastError == nil {
		return true
	}
	ok := true
	if err := r.outboundPolicies.UpdateApplyState(ctx, row.ID, hourID, lastErr); err != nil {
		r.log.Warn("mail-throttle: state stamp failed", "row", row.ID, "window", mailthrottle.WindowHour, "err", err)
		ok = false
	}
	if err := r.outboundPolicies.UpdateApplyStateDaily(ctx, row.ID, dayID, lastErr); err != nil {
		r.log.Warn("mail-throttle: state stamp failed", "row", row.ID, "window", mailthrottle.WindowDay, "err", err)
		ok = false
	}
	return ok
}

// reconcileThrottleWindow brings one window in line and returns the Stalwart
// id the window owns afterwards. On error the id is the one it had.
func (r *Reconciler) reconcileThrottleWindow(ctx context.Context, row *models.MailOutboundPolicy, window string) (string, error) {
	limit, currentID := uint64(row.MaxPerHour), row.StalwartID
	if window == mailthrottle.WindowDay {
		limit, currentID = uint64(row.MaxPerDay), row.StalwartIDDaily
	}
	cctx, cancel := context.WithTimeout(ctx, mailThrottleCallTimeout)
	defer cancel()

	if row.Enabled && limit > 0 {
		res, err := r.mailThrottles.Apply(cctx, throttleRequest(row, window, limit, currentID))
		if err != nil {
			return currentID, errors.New(window + ": " + err.Error())
		}
		if res.Changed {
			r.log.Info("mail-throttle: applied", "row", row.ID, "window", window, "stalwart_id", res.StalwartID)
		}
		return res.StalwartID, nil
	}
	if currentID == "" {
		return "", nil
	}
	if err := r.mailThrottles.Delete(cctx, currentID); err != nil {
		return currentID, errors.New(window + ": " + err.Error())
	}
	r.log.Info("mail-throttle: removed", "row", row.ID, "window", window, "stalwart_id", currentID)
	return "", nil
}

// throttleRequest is what the applier needs to build one window's object.
// scope_ref was validated by the API handler and is validated again by the
// applier, because it ends up inside a Stalwart expression.
func throttleRequest(row *models.MailOutboundPolicy, window string, limit uint64, currentID string) mailthrottle.ApplyRequest {
	req := mailthrottle.ApplyRequest{
		StalwartID: currentID,
		Scope:      row.Scope,
		Window:     window,
		Limit:      limit,
	}
	if row.Scope != models.OutboundScopeGlobal && row.ScopeRef != nil {
		req.ScopeRef = *row.ScopeRef
	}
	return req
}
