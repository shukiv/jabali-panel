package eventsources

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes ---

// fakeUserLister embeds the wide repository.UserRepository and overrides
// only List — the one method diskQuotaPass touches. The embedding keeps
// the fake to the slice under test without hand-writing the whole
// interface (feedback: interface-embedding fakes).
type fakeUserLister struct {
	repository.UserRepository
	users []models.User
}

func (f fakeUserLister) List(_ context.Context, _ repository.ListOptions) ([]models.User, int64, error) {
	return f.users, int64(len(f.users)), nil
}

// failAgent hard-fails the test if disk_quota ever calls the agent. AC #3
// requires the alert path to consume the persisted snapshot and make zero
// Agent calls — this turns that from a claim into a guard.
type failAgent struct{ t *testing.T }

func (a failAgent) Call(_ context.Context, method string, _ any) (json.RawMessage, error) {
	a.t.Fatalf("disk_quota must not call the agent (AC #3); got Call(%q)", method)
	return nil, nil
}

func diskUser(username string, usedKB, limitKB uint64, checkedAt *time.Time) models.User {
	return models.User{
		ID:            "u-" + username,
		Username:      strPtr(username),
		DiskUsedKB:    usedKB,
		DiskLimitKB:   limitKB,
		DiskCheckedAt: checkedAt,
	}
}

func diskQuotaDeps(t *testing.T, now time.Time, users []models.User) (Deps, *capturingPublisher) {
	pub := &capturingPublisher{}
	return Deps{
		Queue:      pub,
		History:    &fakeHistory{},
		Users:      fakeUserLister{users: users},
		Agent:      failAgent{t: t}, // must never be called
		QuotaMount: "/home",
		Log:        slog.New(slog.DiscardHandler),
		Now:        func() time.Time { return now },
	}, pub
}

// --- tests ---

func TestDiskQuota_FreshOverThreshold_Fires(t *testing.T) {
	now := fixedNow()
	fresh := tsPtr(now.Add(-5 * time.Minute))
	// 95 GB / 100 GB = 95% ≥ 90% floor.
	d, pub := diskQuotaDeps(t, now, []models.User{
		diskUser("alice", 95*1024*1024, 100*1024*1024, fresh),
	})
	diskQuotaPass(context.Background(), d)
	require.Equal(t, 1, pub.Count(), "fresh over-threshold snapshot must fire")
	env := pub.Last()
	require.Equal(t, "disk.quota.warn", env.EventKind)
	require.Equal(t, "u-alice", env.UserID)
	require.Contains(t, env.Body, "user:alice")
}

func TestDiskQuota_NeverSwept_Refused(t *testing.T) {
	now := fixedNow()
	// DiskCheckedAt nil — no observation yet. Even though used/limit
	// would breach, there is nothing to alert on.
	d, pub := diskQuotaDeps(t, now, []models.User{
		diskUser("alice", 99*1024*1024, 100*1024*1024, nil),
	})
	diskQuotaPass(context.Background(), d)
	require.Equal(t, 0, pub.Count(), "never-swept snapshot must be refused")
}

func TestDiskQuota_StaleSnapshot_Refused(t *testing.T) {
	now := fixedNow()
	// Swept, but the snapshot is older than the freshness ceiling — the
	// sweeper is lagging or wedged; last-good is too old to alert on.
	stale := tsPtr(now.Add(-(diskQuotaMaxAge + time.Minute)))
	d, pub := diskQuotaDeps(t, now, []models.User{
		diskUser("alice", 99*1024*1024, 100*1024*1024, stale),
	})
	diskQuotaPass(context.Background(), d)
	require.Equal(t, 0, pub.Count(), "snapshot older than diskQuotaMaxAge must be refused")
}

func TestDiskQuota_JustWithinMaxAge_Fires(t *testing.T) {
	now := fixedNow()
	// Exactly at the ceiling minus a second — still fresh enough.
	edge := tsPtr(now.Add(-(diskQuotaMaxAge - time.Second)))
	d, pub := diskQuotaDeps(t, now, []models.User{
		diskUser("alice", 91*1024*1024, 100*1024*1024, edge),
	})
	diskQuotaPass(context.Background(), d)
	require.Equal(t, 1, pub.Count(), "snapshot within max age must fire")
}

func TestDiskQuota_ZeroLimit_Skipped(t *testing.T) {
	now := fixedNow()
	fresh := tsPtr(now.Add(-time.Minute))
	// LimitKB 0 = unlimited/unconfigured — no percentage, nothing to alert.
	d, pub := diskQuotaDeps(t, now, []models.User{
		diskUser("alice", 500*1024*1024, 0, fresh),
	})
	diskQuotaPass(context.Background(), d)
	require.Equal(t, 0, pub.Count(), "zero hard limit must be skipped (no division)")
}

func TestDiskQuota_UnderThreshold_Skipped(t *testing.T) {
	now := fixedNow()
	fresh := tsPtr(now.Add(-time.Minute))
	// 50% — well under the 90% floor.
	d, pub := diskQuotaDeps(t, now, []models.User{
		diskUser("alice", 50*1024*1024, 100*1024*1024, fresh),
	})
	diskQuotaPass(context.Background(), d)
	require.Equal(t, 0, pub.Count(), "under-threshold usage must not fire")
}

func TestDiskQuota_AdminNoUsername_Skipped(t *testing.T) {
	now := fixedNow()
	fresh := tsPtr(now.Add(-time.Minute))
	// Admin row: nil username. Would breach if considered, but disk
	// quota only applies to hosting accounts.
	admin := models.User{ID: "admin", Username: nil, DiskUsedKB: 99, DiskLimitKB: 100, DiskCheckedAt: fresh}
	d, pub := diskQuotaDeps(t, now, []models.User{admin})
	diskQuotaPass(context.Background(), d)
	require.Equal(t, 0, pub.Count(), "row without a username must be skipped")
}

func TestDiskQuota_CooloffDeduped(t *testing.T) {
	now := fixedNow()
	fresh := tsPtr(now.Add(-time.Minute))
	d, pub := diskQuotaDeps(t, now, []models.User{
		diskUser("alice", 95*1024*1024, 100*1024*1024, fresh),
	})
	// Pre-seed history with a recent fire carrying the dedupe tag →
	// shouldFire returns false, second pass must not double-fire.
	hist := d.History.(*fakeHistory)
	hist.recordFired("disk.quota.warn", "... (user:alice)", now.Add(-1*time.Hour))
	diskQuotaPass(context.Background(), d)
	require.Equal(t, 0, pub.Count(), "within 6h cooloff must not re-fire")
}
