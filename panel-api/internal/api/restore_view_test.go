package api

import (
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestRestoreContentFromSelective(t *testing.T) {
	cases := []struct {
		name       string
		databases  []string
		mailboxes  []string
		dnsDomains []string
		domains    []string
		home       bool
		want       string
	}{
		{"databases only", []string{"db1"}, nil, nil, nil, false, models.BackupContentDatabase},
		{"multiple databases only", []string{"db1", "db2"}, nil, nil, nil, false, models.BackupContentDatabase},
		{"home only", nil, nil, nil, nil, true, models.BackupContentFiles},
		{"domains only -> files", nil, nil, nil, []string{"x.com"}, false, models.BackupContentFiles},
		{"db + home -> full", []string{"db1"}, nil, nil, nil, true, models.BackupContentFull},
		{"db + domains -> full", []string{"db1"}, nil, nil, []string{"x.com"}, false, models.BackupContentFull},
		{"mailboxes only -> full", nil, []string{"a@x.com"}, nil, nil, false, models.BackupContentFull},
		{"dns only -> full", nil, nil, []string{"x.com"}, nil, false, models.BackupContentFull},
		{"empty -> full", nil, nil, nil, nil, false, models.BackupContentFull},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := restoreContentFromSelective(tc.databases, tc.mailboxes, tc.dnsDomains, tc.domains, tc.home); got != tc.want {
				t.Errorf("restoreContentFromSelective = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsRestoreKind(t *testing.T) {
	for kind, want := range map[string]bool{
		models.BackupJobKindAccountRestore: true,
		models.BackupJobKindSystemRestore:  true,
		models.BackupJobKindAccountBackup:  false,
		models.BackupJobKindSystemBackup:   false,
	} {
		if got := isRestoreKind(kind); got != want {
			t.Errorf("isRestoreKind(%q) = %v, want %v", kind, got, want)
		}
	}
}

func TestFormatRestoreLog_SelectiveOutcome(t *testing.T) {
	out, _ := json.Marshal(selectiveRestoreOutcome{
		Applied:  []string{"database db1 restored (3,000,000 rows)"},
		Skipped:  []string{"mailbox a@x.com not owned"},
		Warnings: []string{"overwrite=false: DNS not applied"},
	})
	job := &models.BackupJob{Kind: models.BackupJobKindAccountRestore, WarningsJSON: out}

	got := formatRestoreLog(job)
	for _, want := range []string{
		"[APPLIED] database db1 restored (3,000,000 rows)",
		"[SKIPPED] mailbox a@x.com not owned",
		"[WARNING] overwrite=false: DNS not applied",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("restore log missing %q\n---\n%s", want, got)
		}
	}
}

func TestFormatRestoreLog_FullStages(t *testing.T) {
	manifest, _ := json.Marshal(map[string]any{
		"stages": []map[string]any{
			{"name": "databases", "status": "succeeded"},
			{"name": "home", "status": "failed", "error": "disk full"},
		},
	})
	job := &models.BackupJob{Kind: models.BackupJobKindAccountRestore, ManifestJSON: manifest}

	got := formatRestoreLog(job)
	if !strings.Contains(got, "[SUCCEEDED] databases") {
		t.Errorf("missing succeeded stage:\n%s", got)
	}
	if !strings.Contains(got, "[FAILED] home: disk full") {
		t.Errorf("missing failed stage w/ error:\n%s", got)
	}
}

func TestFormatRestoreLog_EmptyIsExplicit(t *testing.T) {
	job := &models.BackupJob{Kind: models.BackupJobKindAccountRestore}
	got := formatRestoreLog(job)
	if !strings.Contains(got, "no recorded outcome") {
		t.Errorf("empty restore result must be explicit, got: %q", got)
	}
	// Must NOT borrow the agent's backup-retention message.
	if strings.Contains(got, "expired by retention") {
		t.Errorf("must not use the backup-retention message for a restore: %q", got)
	}
}

func TestFormatRestoreLog_ErrorFallback(t *testing.T) {
	job := &models.BackupJob{Kind: models.BackupJobKindAccountRestore, ErrorText: "snapshot not found"}
	got := formatRestoreLog(job)
	if !strings.Contains(got, "snapshot not found") {
		t.Errorf("error_text should surface when no structured result: %q", got)
	}
}
