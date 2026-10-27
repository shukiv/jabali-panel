package reconciler

import (
	"context"
	"encoding/json"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// pythonAppOrphanGrace is how long an app unit must have had no app row
// before the sweep removes it. Only the reconciler writes a unit, from a row,
// so a unit with no row belongs to a deleted app; the grace keeps the sweep
// clear of a row list read just before another pass's first apply of a new
// app (the admin reconcile endpoint runs a pass alongside the loop's).
const pythonAppOrphanGrace = 2 * time.Minute

const pythonAppOrphanCallTimeout = 30 * time.Second

// pythonAppOrphan is an app unit the sweep found with no app row.
type pythonAppOrphan struct {
	since  time.Time // the first pass that found it so
	warned bool      // a failed removal was already logged at Warn
}

// pythonAppUnitList is the answer of app.python.list_units.
type pythonAppUnitList struct {
	AppIDs []string `json:"app_ids"`
}

// sweepPythonAppUnits removes the unit of every app that has no row (GH
// #2289). Such a unit outlived its app's delete: the delete and an apply
// crossed, or the remove failed. It kept running the deleted app and holding
// its loopback port, so the next app given that port failed with "Address
// already in use" while nginx sent that app's domain to the old process.
//
// apps is every row of python_apps, whatever its runtime or status, so no
// app that has a row loses its unit. When the unit list can't be had the
// pass removes nothing.
func (r *Reconciler) sweepPythonAppUnits(ctx context.Context, apps []*models.PythonApp) {
	lctx, cancel := context.WithTimeout(ctx, pythonAppOrphanCallTimeout)
	raw, err := r.agent.Call(lctx, "app.python.list_units", map[string]any{})
	cancel()
	if err != nil {
		r.log.Debug("pyapp: listing app units failed — sweep skipped", "err", err)
		return
	}
	var units pythonAppUnitList
	if err := json.Unmarshal(raw, &units); err != nil {
		r.log.Warn("pyapp: unreadable app unit list — sweep skipped", "err", err)
		return
	}
	rows := make(map[string]bool, len(apps))
	for _, app := range apps {
		rows[app.ID] = true
	}
	orphans := map[string]bool{}
	for _, id := range units.AppIDs {
		if rows[id] {
			continue
		}
		orphans[id] = true
		if !r.pythonAppOrphanDue(id, time.Now()) {
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, pythonAppOrphanCallTimeout)
		_, err := r.agent.Call(rctx, "app.python.remove", map[string]any{"app_id": id})
		cancel()
		if err != nil {
			if r.pythonAppOrphanFailed(id) {
				r.log.Warn("pyapp: removing the unit of a deleted app failed — will retry", "app_id", id, "err", err)
			} else {
				r.log.Debug("pyapp: removing the unit of a deleted app failed — will retry", "app_id", id, "err", err)
			}
			continue
		}
		r.forgetPythonAppOrphan(id)
		r.log.Info("pyapp: removed the unit of a deleted app", "app_id", id)
	}
	r.keepPythonAppOrphans(orphans)
}

// pythonAppOrphanDue records id as found without an app row and reports
// whether it has been so for pythonAppOrphanGrace.
func (r *Reconciler) pythonAppOrphanDue(id string, now time.Time) bool {
	r.pythonAppOrphanMu.Lock()
	defer r.pythonAppOrphanMu.Unlock()
	if r.pythonAppOrphanSeen == nil {
		r.pythonAppOrphanSeen = map[string]*pythonAppOrphan{}
	}
	o := r.pythonAppOrphanSeen[id]
	if o == nil {
		r.pythonAppOrphanSeen[id] = &pythonAppOrphan{since: now}
		return false
	}
	return now.Sub(o.since) >= pythonAppOrphanGrace
}

// pythonAppOrphanFailed reports whether a failed removal of id is the first
// one to log at Warn; later ones log at Debug.
func (r *Reconciler) pythonAppOrphanFailed(id string) bool {
	r.pythonAppOrphanMu.Lock()
	defer r.pythonAppOrphanMu.Unlock()
	o := r.pythonAppOrphanSeen[id]
	if o == nil || o.warned {
		return false
	}
	o.warned = true
	return true
}

func (r *Reconciler) forgetPythonAppOrphan(id string) {
	r.pythonAppOrphanMu.Lock()
	defer r.pythonAppOrphanMu.Unlock()
	delete(r.pythonAppOrphanSeen, id)
}

// keepPythonAppOrphans drops every recorded id not in orphans, so a unit that
// had a row again, or was gone, in between starts its grace again.
func (r *Reconciler) keepPythonAppOrphans(orphans map[string]bool) {
	r.pythonAppOrphanMu.Lock()
	defer r.pythonAppOrphanMu.Unlock()
	for id := range r.pythonAppOrphanSeen {
		if !orphans[id] {
			delete(r.pythonAppOrphanSeen, id)
		}
	}
}
