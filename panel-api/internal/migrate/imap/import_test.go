package imap

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

func TestImportOrchestration(t *testing.T) {
	orig := fetchFn
	defer func() { fetchFn = orig }()

	var gotStaging string
	fetchFn = func(_ context.Context, _ ProbeConfig, _ []Folder, stagingDir string) (*FetchResult, error) {
		gotStaging = stagingDir
		return &FetchResult{TotalMessages: 2, TotalBytes: 100, Skipped: []string{"[Gmail]/All Mail"}}, nil
	}

	mock := agent.NewMockClient()
	mock.On("migration.import_mailboxes", agentImportMailboxesResult{
		MailboxesProcessed: 2,
		MessagesImported:   2,
		BytesImported:      100,
		Skipped:            []string{"oversized:x"},
	})

	res, err := Import(context.Background(), ImportConfig{
		Account:     ProbeConfig{Username: "user@example.com", Password: "app-pass", Host: "imap.example.com"},
		TargetEmail: "dest@local.test",
		JobID:       "job-123",
		StagingBase: "/var/lib/jabali/migrations",
	}, mock)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Staging Maildir path: <base>/<job>/imap/<account>/Maildir.
	wantStaging := "/var/lib/jabali/migrations/job-123/imap/user@example.com/Maildir"
	if gotStaging != wantStaging {
		t.Errorf("staging dir = %q, want %q", gotStaging, wantStaging)
	}

	calls := mock.Calls()
	if len(calls) != 1 {
		t.Fatalf("agent calls = %d, want 1", len(calls))
	}
	if calls[0].Command != "migration.import_mailboxes" {
		t.Errorf("command = %q, want migration.import_mailboxes", calls[0].Command)
	}
	var params struct {
		JobID      string `json:"job_id"`
		SrcMailDir string `json:"src_mail_dir"`
		OwnerEmail string `json:"owner_email"`
	}
	if err := json.Unmarshal(calls[0].Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params.JobID != "job-123" {
		t.Errorf("job_id = %q, want job-123", params.JobID)
	}
	if params.SrcMailDir != wantStaging {
		t.Errorf("src_mail_dir = %q, want %q", params.SrcMailDir, wantStaging)
	}
	if params.OwnerEmail != "dest@local.test" {
		t.Errorf("owner_email = %q, want dest@local.test", params.OwnerEmail)
	}

	if res.MessagesImported != 2 || res.BytesImported != 100 {
		t.Errorf("result = %d msgs/%d bytes, want 2/100", res.MessagesImported, res.BytesImported)
	}
	// Skipped merges fetch-side + agent-side notes.
	if len(res.Skipped) != 2 {
		t.Errorf("skipped = %v, want the fetch + agent notes", res.Skipped)
	}
}

func TestImportValidation(t *testing.T) {
	mock := agent.NewMockClient()
	base := ImportConfig{
		Account:     ProbeConfig{Username: "u@x", Password: "p", Host: "h"},
		TargetEmail: "dest@local",
		JobID:       "j1",
		StagingBase: "/var/lib/jabali/migrations",
	}
	cases := map[string]func(*ImportConfig){
		"missing target":  func(c *ImportConfig) { c.TargetEmail = "" },
		"missing job":     func(c *ImportConfig) { c.JobID = "" },
		"missing staging": func(c *ImportConfig) { c.StagingBase = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := Import(context.Background(), cfg, mock); err == nil {
				t.Errorf("Import(%s) = nil error, want validation error", name)
			}
		})
	}

	t.Run("nil agent", func(t *testing.T) {
		if _, err := Import(context.Background(), base, nil); err == nil {
			t.Error("Import(nil agent) = nil error, want error")
		}
	})
}

func TestImportAgentError(t *testing.T) {
	orig := fetchFn
	defer func() { fetchFn = orig }()
	fetchFn = func(_ context.Context, _ ProbeConfig, _ []Folder, _ string) (*FetchResult, error) {
		return &FetchResult{}, nil
	}

	mock := agent.NewMockClient()
	mock.OnError("migration.import_mailboxes", errors.New("agent down"))

	_, err := Import(context.Background(), ImportConfig{
		Account:     ProbeConfig{Username: "u@x", Password: "p", Host: "h"},
		TargetEmail: "dest@local",
		JobID:       "j1",
		StagingBase: "/var/lib/jabali/migrations",
	}, mock)
	if err == nil {
		t.Fatal("Import with failing agent = nil error, want error")
	}
}
