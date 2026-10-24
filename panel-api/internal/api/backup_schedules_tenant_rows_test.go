package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2246: a schedule an account made on its own Backups page (content +
// cadence, no cron) opened in the admin Schedules tab as "Accounts, Daily at
// 03:00", because the admin API returned neither field and the form fell back
// to its default. Saving that form then sent cron_expr and moved the
// schedule's next run to 03:00.

// adminSchedStore adds the two reads the admin list needs to the tenant fake.
type adminSchedStore struct{ *schedStore }

func (s adminSchedStore) List(_ context.Context) ([]models.BackupSchedule, error) {
	var out []models.BackupSchedule
	for _, r := range s.rows {
		out = append(out, *r)
	}
	return out, nil
}

func (s adminSchedStore) GetUserIDs(_ context.Context, id string) ([]string, error) {
	return s.users[id], nil
}

func newAdminSchedRouter(store *schedStore, users *usersMap) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin", IsAdmin: true})
		c.Next()
	})
	RegisterBackupScheduleRoutes(r.Group(""), BackupSchedulesConfig{
		Schedules:    adminSchedStore{store},
		Destinations: fakeDestRepo{},
		Users:        users,
		Settings:     &fakeSettingsRepo{s: &models.ServerSettings{ID: 1}},
	})
	return r
}

func TestAdminSchedules_TenantIntervalSchedule(t *testing.T) {
	store := newSchedStore()
	users := &usersMap{m: map[string]*models.User{"userA": pkgUser("userA", "pkg1")}}
	tenant := newSchedRouter(newSchedHandler(store, users, schedTestPkg(3), nil), "userA")
	admin := newAdminSchedRouter(store, users)

	rec := doReq(t, tenant, http.MethodPost, "/me/backup-schedules", `{"enabled":false,"content":"database","cadence":"every_6h"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	nextBefore := *store.rows[created.ID].NextRunAt

	// The admin list shows what the account set.
	rec = doReq(t, admin, http.MethodGet, "/admin/backup-schedules", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Data []scheduleDTO `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Data, 1)
	row := list.Data[0]
	require.Equal(t, "database", row.Content)
	require.Equal(t, "every_6h", row.Cadence)
	require.Empty(t, row.CronExpr)
	require.NotNil(t, row.UserID)
	require.Equal(t, "userA", *row.UserID)
	require.Equal(t, []string{"userA"}, row.UserIDs)

	path := "/admin/backup-schedules/" + created.ID

	// A cron time no longer re-times it.
	rec = doReq(t, admin, http.MethodPatch, path, `{"cron_expr":"0 3 * * *","enabled":false}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"interval_schedule"`)
	require.Equal(t, nextBefore, *store.rows[created.ID].NextRunAt)
	require.Empty(t, store.rows[created.ID].CronExpr)

	// Nor can it be widened beyond the account.
	rec = doReq(t, admin, http.MethodPatch, path, `{"user_ids":[]}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"account_schedule"`)

	// What the account can change, the admin can change.
	rec = doReq(t, admin, http.MethodPatch, path, `{"content":"files","cadence":"weekly","keep_daily":2,"enabled":false}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var got scheduleDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "files", got.Content)
	require.Equal(t, "weekly", got.Cadence)
	stored := store.rows[created.ID]
	require.Equal(t, "files", stored.Content)
	require.Equal(t, "weekly", stored.Cadence)
	require.Empty(t, stored.CronExpr)
	require.NotEqual(t, nextBefore, *stored.NextRunAt, "a new cadence re-times the schedule")
	require.Equal(t, []string{"userA"}, store.users[created.ID])

	// And the account's own page shows the admin's change.
	rec = doReq(t, tenant, http.MethodGet, "/me/backup-schedules", "")
	var mine struct {
		Data meSchedulesView `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &mine))
	require.Len(t, mine.Data.Schedules, 1)
	require.Equal(t, "files", mine.Data.Schedules[0].Content)
	require.Equal(t, "weekly", mine.Data.Schedules[0].Cadence)
}

// An admin schedule records what it backs up too; the scheduler already
// passes a schedule's content to each account job.
func TestAdminSchedules_CreateWithContent(t *testing.T) {
	store := newSchedStore()
	users := &usersMap{m: map[string]*models.User{"userA": pkgUser("userA", "pkg1")}}
	admin := newAdminSchedRouter(store, users)

	rec := doReq(t, admin, http.MethodPost, "/admin/backup-schedules", `{"kind":"account_backup","cron_expr":"0 3 * * *","content":"database","user_ids":["userA"]}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got scheduleDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, "database", got.Content)
	require.Empty(t, got.Cadence)
	require.Equal(t, "database", store.rows[got.ID].Content)

	rec = doReq(t, admin, http.MethodPost, "/admin/backup-schedules", `{"kind":"account_backup","cron_expr":"0 3 * * *","content":"everything"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"invalid_content"`)

	// A cron schedule has no cadence to change.
	rec = doReq(t, admin, http.MethodPatch, "/admin/backup-schedules/"+got.ID, `{"cadence":"hourly"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"cron_schedule"`)
}
