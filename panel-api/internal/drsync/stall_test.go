package drsync

import (
	"context"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

func TestIsStalled(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	base := now.Add(-10 * time.Minute)
	if !isStalled(base, true, now, 5*time.Minute) {
		t.Error("10m old vs 5m threshold should be stalled")
	}
	if isStalled(now.Add(-1*time.Minute), true, now, 5*time.Minute) {
		t.Error("1m old vs 5m threshold should NOT be stalled")
	}
	if isStalled(base, false, now, 5*time.Minute) {
		t.Error("no baseline → never stalled")
	}
	if isStalled(base, true, now, 0) {
		t.Error("zero threshold → never stalled")
	}
}

func TestStaleBaseline(t *testing.T) {
	last := time.Date(2026, 8, 24, 11, 0, 0, 0, time.UTC)
	paired := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	if b, ok := staleBaseline(&models.ServerSettings{DRLastSyncAt: &last, DRPairedAt: &paired}); !ok || !b.Equal(last) {
		t.Errorf("want last-sync baseline, got %v ok=%v", b, ok)
	}
	if b, ok := staleBaseline(&models.ServerSettings{DRPairedAt: &paired}); !ok || !b.Equal(paired) {
		t.Errorf("want paired-at baseline, got %v ok=%v", b, ok)
	}
	if _, ok := staleBaseline(&models.ServerSettings{}); ok {
		t.Error("no last-sync/paired → no baseline")
	}
}

type stallNotify struct{ n int }

func (f *stallNotify) Publish(context.Context, notifications.Envelope) (string, error) {
	f.n++
	return "id", nil
}

func TestCheckStall_AlertsOncePerEpisode(t *testing.T) {
	old := time.Now().Add(-30 * time.Minute)
	settings := &models.ServerSettings{ServerRole: models.ServerRoleStandby, DRLastSyncAt: &old}
	notify := &stallNotify{}
	s := New(Deps{
		Settings:     &fakeSettings{s: settings},
		Destinations: &fakeDests{},
		Agent:        &fakeAgent{},
		Notify:       notify,
		StalledAfter: time.Minute,
	})
	s.checkStall(context.Background())
	s.checkStall(context.Background())
	if notify.n != 1 {
		t.Fatalf("stalled: want 1 alert, got %d", notify.n)
	}
	fresh := time.Now()
	settings.DRLastSyncAt = &fresh
	s.checkStall(context.Background()) // fresh → re-arm
	settings.DRLastSyncAt = &old
	s.checkStall(context.Background()) // stalled again → 2nd alert
	if notify.n != 2 {
		t.Fatalf("re-armed: want 2 alerts, got %d", notify.n)
	}
}

func TestCheckStall_NilNotifyIsNoop(t *testing.T) {
	old := time.Now().Add(-30 * time.Minute)
	s := New(Deps{
		Settings:     &fakeSettings{s: &models.ServerSettings{ServerRole: models.ServerRoleStandby, DRLastSyncAt: &old}},
		Destinations: &fakeDests{}, Agent: &fakeAgent{}, StalledAfter: time.Minute,
	})
	s.checkStall(context.Background()) // must not panic with nil Notify
}
