package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: an admin restore from an uploaded file runs the agent in
// mode=upload, with the target account's own databases, domains and docker
// apps and everyone else's databases and docker apps.

type ucDBs struct {
	repository.DatabaseRepository
	rows []models.Database
	err  error
}

func (r *ucDBs) List(context.Context, repository.ListOptions) ([]models.Database, int64, error) {
	return r.rows, int64(len(r.rows)), r.err
}

type ucDomains struct {
	repository.DomainRepository
	rows []models.Domain
}

func (r *ucDomains) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	var out []models.Domain
	for _, d := range r.rows {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, int64(len(out)), nil
}

type ucApps struct {
	repository.DockerAppRepository
	rows []*models.DockerApp
}

func (r *ucApps) ListAll(context.Context) ([]*models.DockerApp, error) { return r.rows, nil }

func ucConfig() BackupHandlerConfig {
	t, bob := "T", "B"
	return BackupHandlerConfig{
		Databases: &ucDBs{rows: []models.Database{{UserID: t, Name: "alice_wp"}, {UserID: bob, Name: "bob_shop"}}},
		Domains:   &ucDomains{rows: []models.Domain{{UserID: t, Name: "alice.org"}, {UserID: bob, Name: "bob.org"}}},
		DockerApps: &ucApps{rows: []*models.DockerApp{
			{UserID: &t, Slug: "n8n", InstanceSlug: "n8n-1"},
			{UserID: &bob, Slug: "gitea"},
			{Slug: "uptime-kuma"}, // server-level
		}},
	}
}

func strs(v any) []string {
	s, _ := v.([]string)
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}

func TestUploadRestoreParams_SplitsTheAccountsOwnFromEveryoneElses(t *testing.T) {
	p := map[string]any{}
	if err := ucConfig().uploadRestoreParams(context.Background(), "T", p); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"allowed_db_names":     {"alice_wp"},
		"foreign_db_names":     {"bob_shop"},
		"allowed_mail_domains": {"alice.org"},
		"owned_docker_slugs":   {"n8n-1"},
		"foreign_docker_slugs": {"gitea", "uptime-kuma"},
	}
	if p["mode"] != "upload" {
		t.Fatalf("mode = %v, want upload", p["mode"])
	}
	for k, w := range want {
		if got := strs(p[k]); strings.Join(got, ",") != strings.Join(w, ",") {
			t.Errorf("%s = %v, want %v", k, got, w)
		}
	}
}

// A partial "everyone else" list would let the file write into whatever it
// left out: a failed lookup, or a repo that isn't wired, refuses the restore.
func TestUploadRestoreParams_RefusesWhenItCantList(t *testing.T) {
	cfg := ucConfig()
	cfg.Databases = &ucDBs{err: errors.New("db down")}
	if err := cfg.uploadRestoreParams(context.Background(), "T", map[string]any{}); err == nil {
		t.Fatal("a failed database lookup must refuse the restore")
	}
	cfg = ucConfig()
	cfg.DockerApps = nil
	if err := cfg.uploadRestoreParams(context.Background(), "T", map[string]any{}); err == nil {
		t.Fatal("an unwired docker app repo must refuse the restore")
	}
}

func TestAgentHasCapability(t *testing.T) {
	reply := func(body string, err error) *mockAgent {
		return &mockAgent{callFn: func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
			if cmd != "agent.version" {
				return nil, fmt.Errorf("unexpected %s", cmd)
			}
			return json.RawMessage(body), err
		}}
	}
	ctx := context.Background()
	if !agentHasCapability(ctx, reply(`{"version":"x","capabilities":["restore_upload_confinement"]}`, nil), capRestoreUploadConfinement) {
		t.Error("an agent listing the capability must pass")
	}
	for name, ag := range map[string]*mockAgent{
		"older agent":  reply(`{"version":"x"}`, nil),
		"call failed":  reply(``, errors.New("socket")),
		"garbage body": reply(`{`, nil),
	} {
		if agentHasCapability(ctx, ag, capRestoreUploadConfinement) {
			t.Errorf("%s: must not pass", name)
		}
	}
	if agentHasCapability(ctx, nil, capRestoreUploadConfinement) {
		t.Error("no agent must not pass")
	}
}

func ucUpload(t *testing.T, reply string) (*backupHandler, uploadRestoreArgs, *map[string]any) {
	t.Helper()
	dir := t.TempDir()
	tar := filepath.Join(dir, "up.tar.zst")
	if err := os.WriteFile(tar, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	sent := &map[string]any{}
	ag := &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		if cmd != "backup.restore_from_tar" {
			return nil, fmt.Errorf("unexpected %s", cmd)
		}
		*sent = params.(map[string]any)
		return json.RawMessage(reply), nil
	}}
	cfg := ucConfig()
	cfg.Agent = ag
	return &backupHandler{cfg: cfg}, uploadRestoreArgs{path: tar, outcomePath: filepath.Join(dir, "o.json"), username: "alice", targetID: "T"}, sent
}

func TestRunUploadRestore_RunsTheAgentInUploadMode(t *testing.T) {
	h, a, sent := ucUpload(t, `{"applied":["home → /home/alice"],"upload_confinement_enforced":true}`)
	h.runUploadRestore(a)

	if (*sent)["mode"] != "upload" || strings.Join(strs((*sent)["foreign_db_names"]), ",") != "bob_shop" {
		t.Fatalf("restore_from_tar params = %v, want mode=upload with the lists", *sent)
	}
	o, err := readRestoreUploadOutcome(a.outcomePath)
	if err != nil || o.Status != "done" {
		t.Fatalf("outcome = %+v err=%v, want done", o, err)
	}
}

func TestRunUploadRestore_UnconfirmedConfinementFails(t *testing.T) {
	h, a, _ := ucUpload(t, `{"applied":["home → /home/alice"]}`)
	h.runUploadRestore(a)

	o, err := readRestoreUploadOutcome(a.outcomePath)
	if err != nil || o.Status != "failed" || !strings.Contains(o.Error, "did not confirm") {
		t.Fatalf("outcome = %+v err=%v, want failed: the agent didn't confirm the confinement", o, err)
	}
}

func TestRunUploadRestore_RefusesWhenItCantList(t *testing.T) {
	h, a, sent := ucUpload(t, `{"upload_confinement_enforced":true}`)
	h.cfg.Databases = &ucDBs{err: errors.New("db down")}
	h.runUploadRestore(a)

	if len(*sent) != 0 {
		t.Fatalf("the agent ran with %v; it must not run without the lists", *sent)
	}
	o, err := readRestoreUploadOutcome(a.outcomePath)
	if err != nil || o.Status != "failed" {
		t.Fatalf("outcome = %+v err=%v, want failed", o, err)
	}
}
