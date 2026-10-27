package api

import (
	"net/http"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2300: saving a schedule without changing how often it runs keeps its run
// times, as the admin edit does (GH #2246). Changing the cadence times it
// again, and turning a schedule back on doesn't start a backup at once.
func TestMeSchedules_EditKeepsTheRunTimes(t *testing.T) {
	store := newSchedStore()
	users := &usersMap{m: map[string]*models.User{"userA": pkgUser("userA", "pkg1")}}
	dest := &models.BackupDestination{ID: "d1", Kind: "local", Enabled: true}
	h := newSchedHandler(store, users, schedTestPkg(3), dest)
	r := newSchedRouter(h, "userA")

	if rec := doReq(t, r, http.MethodPost, "/me/backup-schedules", `{"enabled":true,"content":"database","cadence":"every_6h","destination_id":"d1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("seed create = %d body=%s", rec.Code, rec.Body.String())
	}
	var row *models.BackupSchedule
	for _, v := range store.rows {
		row = v
	}
	now := time.Now().UTC()
	planned := now.Add(3*time.Hour + 17*time.Minute).Truncate(time.Minute)
	row.NextRunAt = &planned

	// Same cadence, other fields changed: the run time stays.
	if rec := doReq(t, r, http.MethodPut, "/me/backup-schedules/"+row.ID, `{"enabled":true,"content":"full","cadence":"every_6h","keep_daily":3,"destination_id":"d1"}`); rec.Code != http.StatusOK {
		t.Fatalf("update = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := store.rows[row.ID].NextRunAt; got == nil || !got.Equal(planned) {
		t.Errorf("next run after an edit that keeps the cadence = %v, want %s", got, planned)
	}

	// Off, with its run time passed while it was off: turning it on moves it to
	// the next of its times, not to now.
	off := store.rows[row.ID]
	off.Enabled = false
	past := now.Add(-5 * time.Hour).Truncate(time.Minute)
	off.NextRunAt = &past
	if rec := doReq(t, r, http.MethodPut, "/me/backup-schedules/"+row.ID, `{"enabled":true,"content":"full","cadence":"every_6h","destination_id":"d1"}`); rec.Code != http.StatusOK {
		t.Fatalf("re-enable = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := store.rows[row.ID].NextRunAt; got == nil || !got.Equal(past.Add(6*time.Hour)) {
		t.Errorf("next run after turning it on = %v, want %s (its next time after now)", got, past.Add(6*time.Hour))
	}

	// A new cadence times the schedule again.
	if rec := doReq(t, r, http.MethodPut, "/me/backup-schedules/"+row.ID, `{"enabled":true,"content":"full","cadence":"daily","destination_id":"d1"}`); rec.Code != http.StatusOK {
		t.Fatalf("cadence change = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := store.rows[row.ID].NextRunAt; got == nil || got.Before(now.Add(23*time.Hour)) {
		t.Errorf("next run after changing to daily = %v, want about a day from now", got)
	}
}
