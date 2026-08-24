package main

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestIsDRFeedSchedule(t *testing.T) {
	tests := []struct {
		name string
		s    models.BackupSchedule
		want bool
	}{
		{"legacy system_backup", models.BackupSchedule{Kind: models.BackupScheduleKindSystem}, true},
		{"account + include_system (DR combined)", models.BackupSchedule{Kind: models.BackupScheduleKindAccount, IncludeSystemBackup: true}, true},
		{"plain tenant account schedule", models.BackupSchedule{Kind: models.BackupScheduleKindAccount, IncludeSystemBackup: false}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDRFeedSchedule(&tt.s); got != tt.want {
				t.Errorf("isDRFeedSchedule(%+v) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}
