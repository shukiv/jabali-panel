package main

import (
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #746: a FAILED import must not have its source secret reaped immediately,
// or the operator's retry has to re-upload creds AND re-pull. It gets the same
// age grace as staging; done/degraded/cancelled reap at once.
func TestSecretReapDue_GH746(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	grace := 7 * 24 * time.Hour
	recent := now.Add(-1 * time.Hour)
	old := now.Add(-8 * 24 * time.Hour)

	cases := []struct {
		name    string
		state   string
		ended   *time.Time
		updated time.Time
		want    bool
	}{
		{"failed within grace -> keep", models.MigrationStateFailed, &recent, recent, false},
		{"failed past grace -> reap", models.MigrationStateFailed, &old, old, true},
		{"failed, nil endedAt falls back to updatedAt (recent) -> keep", models.MigrationStateFailed, nil, recent, false},
		{"done -> reap now", models.MigrationStateDone, &recent, recent, true},
		{"cancelled -> reap now", models.MigrationStateCancelled, &recent, recent, true},
		{"degraded -> reap now", models.MigrationStateDegraded, &recent, recent, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := secretReapDue(tc.state, tc.ended, tc.updated, grace, now); got != tc.want {
				t.Errorf("secretReapDue(%s) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}
