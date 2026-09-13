package api

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The legacy single-schedule tenant card (PUT /me/backup-schedule) predates the
// multi-schedule surface (GH #454 7B). It upserts the caller's own account
// schedule and was the last tenant upsert door still doing three sequential
// writes (Create/Update + ReplaceUsers + ReplaceDestinations). These tests pin it
// to the same atomic primitives the multi-schedule create/update handlers use, so
// a mid-sequence failure can never leave a runnable row with partial links
// (JAB-307).

// newPutSchedHandler mirrors newSchedHandler but gives the settings a valid
// admin-owned cron: the card is cron-governed (not window-governed), so
// putSchedule reads settings.TenantBackupCron for NextFire.
func newPutSchedHandler(store *schedStore, dest *models.BackupDestination) *meBackupHandler {
	users := &usersMap{m: map[string]*models.User{"userA": pkgUser("userA", "pkg1")}}
	h := newSchedHandler(store, users, schedTestPkg(3), dest)
	h.cfg.Settings = &fakeSettingsRepo{s: &models.ServerSettings{ID: 1, TenantBackupCron: "0 3 * * *"}}
	return h
}

func newPutSchedRouter(h *meBackupHandler, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: false})
		c.Next()
	})
	r.PUT("/me/backup-schedule", h.putSchedule)
	return r
}

// No existing schedule → the create branch must persist the row plus the user
// and destination links in ONE atomic primitive, not three sequential writes.
// Also pins the (dests, users) argument order.
func TestMePutSchedule_CreateRoutesThroughAtomicMemberships(t *testing.T) {
	store := newSchedStore()
	dest := &models.BackupDestination{ID: "d1", Kind: "local", Enabled: true}
	h := newPutSchedHandler(store, dest)
	r := newPutSchedRouter(h, "userA")

	rec := doReq(t, r, http.MethodPut, "/me/backup-schedule", `{"enabled":true,"content":"full","destination_id":"d1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d body=%s", rec.Code, rec.Body.String())
	}
	if store.cwmCalls != 1 {
		t.Fatalf("CreateWithMemberships calls = %d, want 1 (must route through the atomic primitive)", store.cwmCalls)
	}
	if store.createCalls != 0 || store.replaceUsersCalls != 0 || store.replaceDestCalls != 0 {
		t.Fatalf("three sequential writes not collapsed: create=%d replaceUsers=%d replaceDest=%d", store.createCalls, store.replaceUsersCalls, store.replaceDestCalls)
	}
	if len(store.lastCWMUsers) != 1 || store.lastCWMUsers[0] != "userA" {
		t.Fatalf("users slot = %v, want [userA] (dests/users arg order swapped?)", store.lastCWMUsers)
	}
	if len(store.lastCWMDests) != 1 || store.lastCWMDests[0] != "d1" {
		t.Fatalf("dests slot = %v, want [d1] (dests/users arg order swapped?)", store.lastCWMDests)
	}
}

// An existing owned schedule → the update branch must persist the field changes
// plus both membership sets in ONE atomic primitive. Also pins the (dests, users)
// argument order.
func TestMePutSchedule_UpdateRoutesThroughAtomicMemberships(t *testing.T) {
	store := newSchedStore()
	dest := &models.BackupDestination{ID: "d1", Kind: "local", Enabled: true}
	h := newPutSchedHandler(store, dest)
	r := newPutSchedRouter(h, "userA")

	// Seed the caller's own schedule through the create branch (disabled, no
	// destination is a legal save), then reset counters so the update assertions
	// are unambiguous about the second PUT alone.
	if rec := doReq(t, r, http.MethodPut, "/me/backup-schedule", `{"enabled":false,"content":"full"}`); rec.Code != http.StatusOK {
		t.Fatalf("seed put = %d body=%s", rec.Code, rec.Body.String())
	}
	if store.cwmCalls != 1 || len(store.rows) != 1 {
		t.Fatalf("seed did not create exactly one row via CWM: cwm=%d rows=%d", store.cwmCalls, len(store.rows))
	}
	store.cwmCalls, store.uwmCalls, store.createCalls, store.updateCalls, store.replaceUsersCalls, store.replaceDestCalls = 0, 0, 0, 0, 0, 0

	rec := doReq(t, r, http.MethodPut, "/me/backup-schedule", `{"enabled":true,"content":"database","destination_id":"d1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update put = %d body=%s", rec.Code, rec.Body.String())
	}
	if store.uwmCalls != 1 {
		t.Fatalf("UpdateWithMemberships calls = %d, want 1", store.uwmCalls)
	}
	if store.updateCalls != 0 || store.replaceUsersCalls != 0 || store.replaceDestCalls != 0 {
		t.Fatalf("three sequential writes not collapsed: update=%d replaceUsers=%d replaceDest=%d", store.updateCalls, store.replaceUsersCalls, store.replaceDestCalls)
	}
	if len(store.lastUWMUsers) != 1 || store.lastUWMUsers[0] != "userA" {
		t.Fatalf("users slot = %v, want [userA] (dests/users arg order swapped?)", store.lastUWMUsers)
	}
	if len(store.lastUWMDests) != 1 || store.lastUWMDests[0] != "d1" {
		t.Fatalf("dests slot = %v, want [d1] (dests/users arg order swapped?)", store.lastUWMDests)
	}
}

// A failed atomic persist on the create branch must surface as 500 db_create,
// leave no separate membership write around it, and store no row (JAB-307).
func TestMePutSchedule_CreateAtomicFailReturns500(t *testing.T) {
	store := newSchedStore()
	h := newPutSchedHandler(store, nil)
	r := newPutSchedRouter(h, "userA")

	store.failCWM = repository.ErrConflict
	rec := doReq(t, r, http.MethodPut, "/me/backup-schedule", `{"enabled":false,"content":"full"}`)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "db_create") {
		t.Fatalf("atomic persist failure must 500 db_create: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.replaceUsersCalls != 0 || store.replaceDestCalls != 0 {
		t.Fatalf("partial membership writes around a failed atomic create: replaceUsers=%d replaceDest=%d", store.replaceUsersCalls, store.replaceDestCalls)
	}
	if len(store.rows) != 0 {
		t.Fatalf("failed atomic create left %d rows", len(store.rows))
	}
}

// Source-pin: the legacy card body routes both its branches through the atomic
// primitives and no longer hand-rolls the three separate writes. Scoped to the
// putSchedule body so a file-wide check does not catch the sibling handlers.
func TestMePutSchedule_RoutesThroughAtomicPrimitives_Source(t *testing.T) {
	raw, err := os.ReadFile("backups.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	body := scheduleFuncBody(t, string(raw), "func (h *meBackupHandler) putSchedule(")
	for _, want := range []string{"CreateWithMemberships(", "UpdateWithMemberships("} {
		if !strings.Contains(body, want) {
			t.Errorf("putSchedule must route through %s", want)
		}
	}
	for _, bad := range []string{"Schedules.Create(", "Schedules.Update(", "ReplaceUsers(", "ReplaceDestinations("} {
		if strings.Contains(body, bad) {
			t.Errorf("putSchedule still calls %s (must be one atomic write per branch)", bad)
		}
	}
}
