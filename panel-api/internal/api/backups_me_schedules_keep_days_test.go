package api

// GH #2300: an account's "Days to keep" is keep_within_days, every backup of
// the last N days. The account may keep as many days as fit in its plan's
// backup limit at the schedule's frequency; the API clamps to that.

import (
	"encoding/json"
	"net/http"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func onlyRow(t *testing.T, store *schedStore) *models.BackupSchedule {
	t.Helper()
	if len(store.rows) != 1 {
		t.Fatalf("want 1 stored schedule, got %d", len(store.rows))
	}
	for _, r := range store.rows {
		return r
	}
	return nil
}

func keepDaysOf(r *models.BackupSchedule) int {
	if r.KeepWithinDays == nil {
		return -1
	}
	return *r.KeepWithinDays
}

func TestMeSchedules_KeepWithinDays_StoredAndClampedToThePlan(t *testing.T) {
	for _, c := range []struct {
		cadence    string
		maxBackups uint32
		days, want int
	}{
		{"every_6h", 28, 7, 7}, // 28 backups
		{"every_6h", 5, 7, 1},  // 5 backups hold one day
		{"daily", 5, 9, 5},
		{"hourly", 5, 3, 1}, // at least one day
	} {
		store := newSchedStore()
		users := &usersMap{m: map[string]*models.User{"userA": pkgUser("userA", "pkg1")}}
		pkg := schedTestPkg(3)
		pkg.MaxBackups = c.maxBackups
		r := newSchedRouter(newSchedHandler(store, users, pkg, nil), "userA")

		body := `{"enabled":false,"content":"full","cadence":"` + c.cadence + `","keep_within_days":` + itoa(c.days) + `}`
		if rec := doReq(t, r, http.MethodPost, "/me/backup-schedules", body); rec.Code != http.StatusCreated {
			t.Fatalf("%s: create = %d body=%s", c.cadence, rec.Code, rec.Body.String())
		}
		row := onlyRow(t, store)
		if got := keepDaysOf(row); got != c.want {
			t.Errorf("%s, %d backups, %d days asked: stored %d days, want %d", c.cadence, c.maxBackups, c.days, got, c.want)
		}
		if row.KeepDaily != nil {
			t.Errorf("%s: keep_daily = %d, want unset", c.cadence, *row.KeepDaily)
		}

		rec := doReq(t, r, http.MethodGet, "/me/backup-schedules", "")
		var list struct {
			Data struct {
				Schedules []struct {
					KeepWithinDays *int `json:"keep_within_days"`
				} `json:"schedules"`
			} `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &list)
		if len(list.Data.Schedules) != 1 || list.Data.Schedules[0].KeepWithinDays == nil || *list.Data.Schedules[0].KeepWithinDays != c.want {
			t.Errorf("%s: list did not return keep_within_days %d: %s", c.cadence, c.want, rec.Body.String())
		}
	}
}

// The single-schedule card runs on the admin's cron (here daily at 03:00), so
// the limit is counted from that, and the view tells the card the maximum.
func TestMePutSchedule_KeepWithinDays_ClampedByTheAdminCron(t *testing.T) {
	store := newSchedStore()
	dest := &models.BackupDestination{ID: "d1", Kind: "local", Enabled: true}
	h := newPutSchedHandler(store, dest) // plan of 5 backups
	r := newPutSchedRouter(h, "userA")
	r.GET("/me/backup-schedule", h.getSchedule)

	rec := doReq(t, r, http.MethodPut, "/me/backup-schedule", `{"enabled":true,"content":"full","destination_id":"d1","keep_within_days":9}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := keepDaysOf(onlyRow(t, store)); got != 5 {
		t.Errorf("stored %d days, want 5 (5 daily backups)", got)
	}

	rec = doReq(t, r, http.MethodGet, "/me/backup-schedule", "")
	var view struct {
		Data struct {
			KeepWithinDays    *int `json:"keep_within_days"`
			KeepWithinDaysMax int  `json:"keep_within_days_max"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if view.Data.KeepWithinDays == nil || *view.Data.KeepWithinDays != 5 || view.Data.KeepWithinDaysMax != 5 {
		t.Errorf("view = %s, want keep_within_days 5 and keep_within_days_max 5", rec.Body.String())
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
