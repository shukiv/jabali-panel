package main

// JAB-363: tearDownDRFeed disables the DR feed schedule + destination
// non-destructively, with a shared-destination guard.

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeSchedRepo struct {
	repository.BackupScheduleRepository
	schedules []models.BackupSchedule
	dests     map[string][]models.BackupDestination // scheduleID -> its destinations
	updated   map[string]bool                       // scheduleID -> enabled value written
}

func (f *fakeSchedRepo) List(context.Context) ([]models.BackupSchedule, error) {
	return f.schedules, nil
}
func (f *fakeSchedRepo) GetDestinations(_ context.Context, id string) ([]models.BackupDestination, error) {
	return f.dests[id], nil
}
func (f *fakeSchedRepo) Update(_ context.Context, s *models.BackupSchedule) error {
	if f.updated == nil {
		f.updated = map[string]bool{}
	}
	f.updated[s.ID] = s.Enabled
	return nil
}

type fakeDestRepo struct {
	repository.BackupDestinationRepository
	dest         *models.BackupDestination
	wroteEnabled *bool
}

func (f *fakeDestRepo) Get(context.Context, string) (*models.BackupDestination, error) {
	return f.dest, nil
}
func (f *fakeDestRepo) Update(_ context.Context, d *models.BackupDestination) error {
	v := d.Enabled
	f.wroteEnabled = &v
	return nil
}

func sysSched(id string, enabled bool) models.BackupSchedule {
	return models.BackupSchedule{ID: id, Kind: models.BackupScheduleKindSystem, Enabled: enabled}
}
func acctSched(id string, enabled bool) models.BackupSchedule {
	return models.BackupSchedule{ID: id, Kind: models.BackupScheduleKindAccount, Enabled: enabled}
}

func TestTearDownDRFeed_DisablesScheduleAndDestination(t *testing.T) {
	sr := &fakeSchedRepo{
		schedules: []models.BackupSchedule{sysSched("s1", true)},
		dests:     map[string][]models.BackupDestination{"s1": {{ID: "d1"}}},
	}
	dr := &fakeDestRepo{dest: &models.BackupDestination{ID: "d1", Enabled: true}}

	res, err := tearDownDRFeed(context.Background(), sr, dr, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if res.scheduleID != "s1" || !res.scheduleDisabled {
		t.Fatalf("feed schedule not disabled: %+v", res)
	}
	if sr.updated["s1"] != false {
		t.Fatalf("schedule s1 should be written disabled")
	}
	if res.destShared || !res.destDisabled {
		t.Fatalf("dedicated destination should be disabled: %+v", res)
	}
	if dr.wroteEnabled == nil || *dr.wroteEnabled != false {
		t.Fatalf("destination should be written disabled")
	}
}

func TestTearDownDRFeed_SharedDestinationLeftEnabled(t *testing.T) {
	// Another ENABLED schedule (an account backup) still targets d1 → the feed
	// schedule is disabled but the destination is left enabled.
	sr := &fakeSchedRepo{
		schedules: []models.BackupSchedule{sysSched("s1", true), acctSched("s2", true)},
		dests: map[string][]models.BackupDestination{
			"s1": {{ID: "d1"}},
			"s2": {{ID: "d1"}},
		},
	}
	dr := &fakeDestRepo{dest: &models.BackupDestination{ID: "d1", Enabled: true}}

	res, err := tearDownDRFeed(context.Background(), sr, dr, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.scheduleDisabled {
		t.Fatalf("feed schedule should still be disabled: %+v", res)
	}
	if !res.destShared || res.destDisabled {
		t.Fatalf("shared destination must stay enabled: %+v", res)
	}
	if dr.wroteEnabled != nil {
		t.Fatalf("destination must not be written when shared")
	}
}

func TestTearDownDRFeed_AlreadyDisabledSchedule(t *testing.T) {
	sr := &fakeSchedRepo{
		schedules: []models.BackupSchedule{sysSched("s1", false)},
		dests:     map[string][]models.BackupDestination{"s1": {{ID: "d1"}}},
	}
	dr := &fakeDestRepo{dest: &models.BackupDestination{ID: "d1", Enabled: true}}

	res, err := tearDownDRFeed(context.Background(), sr, dr, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if res.scheduleID != "s1" || res.scheduleDisabled {
		t.Fatalf("already-disabled schedule should report not-newly-disabled: %+v", res)
	}
	if _, wrote := sr.updated["s1"]; wrote {
		t.Fatalf("already-disabled schedule must not be re-written")
	}
	// The disabled feed does not count as an enabled targeter, so the dest is disabled.
	if res.destShared || !res.destDisabled {
		t.Fatalf("destination should be disabled: %+v", res)
	}
}
