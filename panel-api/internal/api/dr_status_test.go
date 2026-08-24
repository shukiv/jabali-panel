package api

import (
	"context"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestBuildDRStatus(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	tp := func(d time.Duration) *time.Time { x := now.Add(d); return &x }
	destID := "dest1"

	t.Run("standby stalled when last sync is old", func(t *testing.T) {
		s := &models.ServerSettings{
			ServerRole: models.ServerRoleStandby, DRDestinationID: &destID,
			DRPairedAt: tp(-2 * time.Hour), DRLastSyncAt: tp(-30 * time.Minute),
		}
		got := buildDRStatus(context.Background(), s, nil, now)
		if !got.IsStandby || !got.Paired || !got.Stalled {
			t.Fatalf("want standby+paired+stalled, got %+v", got)
		}
		if got.SyncAgeSeconds != int64((30 * time.Minute).Seconds()) {
			t.Errorf("age = %d, want %d", got.SyncAgeSeconds, int64((30 * time.Minute).Seconds()))
		}
	})

	t.Run("standby fresh when last sync is recent", func(t *testing.T) {
		s := &models.ServerSettings{
			ServerRole: models.ServerRoleStandby, DRDestinationID: &destID,
			DRPairedAt: tp(-2 * time.Hour), DRLastSyncAt: tp(-1 * time.Minute),
		}
		got := buildDRStatus(context.Background(), s, nil, now)
		if got.Stalled {
			t.Errorf("1m-old sync should not be stalled: %+v", got)
		}
	})

	t.Run("before first sync uses pairing time", func(t *testing.T) {
		s := &models.ServerSettings{
			ServerRole: models.ServerRoleStandby, DRDestinationID: &destID,
			DRPairedAt: tp(-30 * time.Minute), // never synced
		}
		got := buildDRStatus(context.Background(), s, nil, now)
		if !got.Stalled || got.SyncAgeSeconds != int64((30*time.Minute).Seconds()) {
			t.Errorf("want stalled from pairing baseline, got %+v", got)
		}
	})

	t.Run("primary never stalled and no baseline", func(t *testing.T) {
		s := &models.ServerSettings{ServerRole: models.ServerRolePrimary}
		got := buildDRStatus(context.Background(), s, nil, now)
		if got.IsStandby || got.Stalled || got.Paired || got.SyncAgeSeconds != -1 {
			t.Fatalf("primary/unpaired should be inert, got %+v", got)
		}
		if got.Role != models.ServerRolePrimary {
			t.Errorf("role = %q", got.Role)
		}
	})
}
