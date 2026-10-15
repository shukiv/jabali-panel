package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// `jabali docker-app stop|start|restart|rebuild` ran the agent verb and never
// wrote the app's status, so the row kept its prior status until the
// reconciler's next status poll. Meanwhile `jabali docker-app edit` was
// refused right after a stop (row still running), and allowed right after a
// start (row still stopped) while the container was up.
func TestRecordDockerAppLifecycle_WritesTheVerbsStatus(t *testing.T) {
	for verb, want := range map[string]string{
		"stop":    models.DockerAppStatusStopped,
		"start":   models.DockerAppStatusRunning,
		"restart": models.DockerAppStatusRunning,
		"rebuild": models.DockerAppStatusRunning,
	} {
		repo := &fakeStatusRepo{}
		if err := recordDockerAppLifecycle(context.Background(), repo, "app-x", verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		if !repo.called || repo.lastID != "app-x" || repo.lastStatus != want {
			t.Errorf("%s wrote (called=%v id=%q status=%q), want status %q for app-x",
				verb, repo.called, repo.lastID, repo.lastStatus, want)
		}
		if repo.lastMsg != nil {
			t.Errorf("%s changed last_error to %q; the API door leaves it as it is", verb, *repo.lastMsg)
		}
	}
}

func TestRecordDockerAppLifecycle_UnknownVerbWritesNothing(t *testing.T) {
	repo := &fakeStatusRepo{}
	if err := recordDockerAppLifecycle(context.Background(), repo, "app-x", "logs"); err != nil {
		t.Fatal(err)
	}
	if repo.called {
		t.Fatalf("an unknown verb wrote status %q", repo.lastStatus)
	}
}

type failingStatusRepo struct{}

func (failingStatusRepo) UpdateStatus(context.Context, string, string, *string) error {
	return errors.New("db gone")
}

// A status that couldn't be written is reported, not swallowed: swallowing it
// is the bug this fixes.
func TestRecordDockerAppLifecycle_ReportsAFailedWrite(t *testing.T) {
	err := recordDockerAppLifecycle(context.Background(), failingStatusRepo{}, "app-x", "stop")
	if err == nil || !strings.Contains(err.Error(), "db gone") {
		t.Fatalf("want the write error, got %v", err)
	}
}

// cmd/server has no DB/agent fixture (see docker_app_cmd_effectiveslug_test.go),
// so pin the wiring in source: the lifecycle RunE records the status only
// after the agent verb succeeded.
func TestDockerAppLifecycleCmd_RecordsStatusAfterTheAgentVerb(t *testing.T) {
	b, err := os.ReadFile("docker_app_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	start := strings.Index(src, "func newDockerAppLifecycleCmd(")
	if start < 0 {
		t.Fatal("newDockerAppLifecycleCmd not found")
	}
	body := src[start:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	call := strings.Index(body, `raw, err := sharedAgent.Call(ctx, "docker_app."+verb, params)`)
	if call < 0 {
		t.Fatal("lifecycle RunE lost its agent call")
	}
	after := body[call:]
	fail := strings.Index(after, "if err != nil {\n\t\t\t\treturn err\n\t\t\t}")
	record := strings.Index(after, "recordDockerAppLifecycle(ctx, repo, app.ID, verb)")
	if fail < 0 || record < 0 || record < fail {
		t.Fatalf("the status must be recorded after the agent verb succeeded: error return at %d, status record at %d (after the call)", fail, record)
	}
}
