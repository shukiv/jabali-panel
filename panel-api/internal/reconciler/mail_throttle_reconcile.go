// Package reconciler — M47 Wave 3 outbound-throttle convergence.
//
// Reads mail_outbound_policy on each tick and makes Stalwart's
// MtaOutboundThrottle objects match. The panel cannot reach Stalwart's admin
// API (its credential is not readable by the panel user, JAB-357), so every
// change goes through the agent's mail.throttle.* verbs. Each row has an
// hourly and a daily window, and each window is its own state machine:
//
//	enabled && cap > 0   → apply: the agent creates the object, updates it,
//	                       or leaves it alone, and returns the id it owns
//	otherwise, id != ''  → delete, clear the id
//	otherwise            → no-op
//
// A failure keeps the window's id and stamps last_error, so the next tick
// retries. Self-healing.
package reconciler

import (
	"context"
	"errors"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailthrottle"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ThrottleApplier pushes throttle windows into Stalwart. The panel's
// implementation is agent.MailThrottles; tests inject a fake.
type ThrottleApplier interface {
	Apply(ctx context.Context, req mailthrottle.ApplyRequest) (mailthrottle.ApplyResult, error)
	Delete(ctx context.Context, stalwartID string) error
}

const mailThrottleCallTimeout = 30 * time.Second

// reconcileMailThrottles converges every mail_outbound_policy row.
// Called from the main reconcile loop on each tick. Self-disables
// when the policy repo or the applier isn't wired.
func (r *Reconciler) reconcileMailThrottles(ctx context.Context) {
	if r.outboundPolicies == nil || r.mailThrottles == nil {
		return
	}
	rows, err := r.outboundPolicies.List(ctx)
	if err != nil {
		r.log.Warn("mail-throttle: list failed", "err", err)
		return
	}
	for i := range rows {
		r.reconcileMailThrottleOne(ctx, &rows[i])
	}
}

// reconcileMailThrottleOne converges both windows of one row, then stamps
// both ids and one last_error for the row. Stamping once per row keeps a
// failing window's error from being wiped by the other window's success.
// Nothing is written when nothing changed.
func (r *Reconciler) reconcileMailThrottleOne(ctx context.Context, row *models.MailOutboundPolicy) {
	hourID, hourErr := r.reconcileThrottleWindow(ctx, row, mailthrottle.WindowHour)
	dayID, dayErr := r.reconcileThrottleWindow(ctx, row, mailthrottle.WindowDay)

	var lastErr *string
	if err := errors.Join(hourErr, dayErr); err != nil {
		msg := err.Error()
		lastErr = &msg
		r.log.Warn("mail-throttle: apply failed", "row", row.ID, "err", err)
	}
	if hourID == row.StalwartID && dayID == row.StalwartIDDaily && lastErr == nil && row.LastError == nil {
		return
	}
	if err := r.outboundPolicies.UpdateApplyState(ctx, row.ID, hourID, lastErr); err != nil {
		r.log.Warn("mail-throttle: state stamp failed", "row", row.ID, "window", mailthrottle.WindowHour, "err", err)
	}
	if err := r.outboundPolicies.UpdateApplyStateDaily(ctx, row.ID, dayID, lastErr); err != nil {
		r.log.Warn("mail-throttle: state stamp failed", "row", row.ID, "window", mailthrottle.WindowDay, "err", err)
	}
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

// throttleRequest is what the agent needs to build one window's object.
// scope_ref was validated by the API handler and is validated again by the
// agent, because it ends up inside a Stalwart expression.
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
