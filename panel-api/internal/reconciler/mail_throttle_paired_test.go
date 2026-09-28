package reconciler

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailthrottle"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// When both hourly + daily caps are set on one policy row, the
// reconciler must apply TWO Stalwart MtaOutboundThrottle objects
// (one per window). v1 collapsed them into one (hourly wins, daily
// logged-only); v3 fixes that.
func TestReconcileMailThrottles_PairedCreatesTwoObjects(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal,
		MaxPerHour: 100, MaxPerDay: 5000, Enabled: true,
	}
	r.reconcileMailThrottles(context.Background())

	require.Len(t, cl.applies, 2, "expect one apply per active rate window")
	assert.Equal(t, mailthrottle.ApplyRequest{Scope: "global", Window: "hour", Limit: 100}, cl.applies[0])
	assert.Equal(t, mailthrottle.ApplyRequest{Scope: "global", Window: "day", Limit: 5000}, cl.applies[1])
	assert.NotEmpty(t, repo.rows["row1"].StalwartID, "hourly id stamped")
	assert.NotEmpty(t, repo.rows["row1"].StalwartIDDaily, "daily id stamped")
	assert.NotEqual(t, repo.rows["row1"].StalwartID, repo.rows["row1"].StalwartIDDaily)
}

func TestReconcileMailThrottles_OnlyHourlyWhenDailyZero(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal,
		MaxPerHour: 100, MaxPerDay: 0, Enabled: true,
	}
	r.reconcileMailThrottles(context.Background())
	require.Len(t, cl.applies, 1)
	assert.Equal(t, mailthrottle.WindowHour, cl.applies[0].Window)
	assert.NotEmpty(t, repo.rows["row1"].StalwartID)
	assert.Empty(t, repo.rows["row1"].StalwartIDDaily)
}

func TestReconcileMailThrottles_OnlyDailyWhenHourlyZero(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal,
		MaxPerHour: 0, MaxPerDay: 5000, Enabled: true,
	}
	r.reconcileMailThrottles(context.Background())
	require.Len(t, cl.applies, 1)
	assert.Equal(t, mailthrottle.WindowDay, cl.applies[0].Window)
	assert.Empty(t, repo.rows["row1"].StalwartID)
	assert.NotEmpty(t, repo.rows["row1"].StalwartIDDaily)
}

func TestReconcileMailThrottles_RemoveDailyWhenZeroedOut(t *testing.T) {
	r, repo, cl := throttleRecForTest(t)
	// Start with both populated upstream ids; row now has daily=0.
	repo.rows["row1"] = &models.MailOutboundPolicy{
		ID: "row1", Scope: models.OutboundScopeGlobal,
		MaxPerHour: 100, MaxPerDay: 0, Enabled: true,
		StalwartID: "stw-hourly-existing", StalwartIDDaily: "stw-daily-stale",
	}
	r.reconcileMailThrottles(context.Background())
	// Hourly applied in place, daily deleted.
	require.Len(t, cl.applies, 1, "hourly apply")
	assert.Equal(t, "stw-hourly-existing", cl.applies[0].StalwartID)
	require.Equal(t, []string{"stw-daily-stale"}, cl.deletes, "daily delete")
	assert.Equal(t, "stw-hourly-existing", repo.rows["row1"].StalwartID)
	assert.Empty(t, repo.rows["row1"].StalwartIDDaily, "stale daily id cleared")
}
