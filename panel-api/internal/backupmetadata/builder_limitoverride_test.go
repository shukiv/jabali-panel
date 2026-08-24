package backupmetadata

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeLimitOverrides embeds the wide UserLimitOverrideRepository and overrides
// only the two methods this path touches. ListAll fails the test: JAB-374 AC#5
// is that Build no longer scans the whole user_limit_overrides table, so any
// call to ListAll here is a regression, not a claim to re-verify.
type fakeLimitOverrides struct {
	repository.UserLimitOverrideRepository
	t         *testing.T
	byUser    map[string]*models.UserLimitOverride
	findCalls int
}

func (f *fakeLimitOverrides) FindByUserID(_ context.Context, userID string) (*models.UserLimitOverride, error) {
	f.findCalls++
	if lo, ok := f.byUser[userID]; ok {
		return lo, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeLimitOverrides) ListAll(context.Context) ([]models.UserLimitOverride, error) {
	f.t.Fatalf("Build must not ListAll() the whole user_limit_overrides table (JAB-374 AC#5)")
	return nil, nil
}

func u32(v uint32) *uint32 { return &v }

func TestBuild_LimitOverride_UserScopedLookup(t *testing.T) {
	user := &models.User{ID: "u-alice", Email: "alice@example.com"}
	lo := &models.UserLimitOverride{
		UserID:          "u-alice",
		DiskQuotaMB:     u32(2048),
		CPUQuotaPercent: u32(150),
		MemoryLimitMB:   u32(1024),
		IOReadMbps:      u32(50),
		IOWriteMbps:     u32(40),
		MaxTasks:        u32(200),
	}
	f := &fakeLimitOverrides{t: t, byUser: map[string]*models.UserLimitOverride{"u-alice": lo}}

	// Only LimitOverrides wired; every other section skips (nil repo).
	m := Build(context.Background(), user, Deps{LimitOverrides: f})

	if f.findCalls != 1 {
		t.Fatalf("expected exactly one FindByUserID call, got %d", f.findCalls)
	}
	if m.LimitOverride == nil {
		t.Fatal("expected LimitOverride populated for a user with an override")
	}
	got := m.LimitOverride
	if got.DiskQuotaMB == nil || *got.DiskQuotaMB != 2048 ||
		got.CPUQuotaPercent == nil || *got.CPUQuotaPercent != 150 ||
		got.MemoryLimitMB == nil || *got.MemoryLimitMB != 1024 ||
		got.IOReadMbps == nil || *got.IOReadMbps != 50 ||
		got.IOWriteMbps == nil || *got.IOWriteMbps != 40 ||
		got.MaxTasks == nil || *got.MaxTasks != 200 {
		t.Fatalf("override values not mapped correctly: %+v", got)
	}
}

func TestBuild_LimitOverride_NoneForUser(t *testing.T) {
	user := &models.User{ID: "u-bob", Email: "bob@example.com"}
	// Store holds an override for a DIFFERENT user; FindByUserID returns
	// ErrNotFound for bob → no LimitOverride section (identical to the old
	// loop finding no matching row).
	f := &fakeLimitOverrides{t: t, byUser: map[string]*models.UserLimitOverride{
		"u-carol": {UserID: "u-carol", DiskQuotaMB: u32(512)},
	}}

	m := Build(context.Background(), user, Deps{LimitOverrides: f})

	if m.LimitOverride != nil {
		t.Fatalf("expected no LimitOverride for a user without one, got %+v", m.LimitOverride)
	}
}
