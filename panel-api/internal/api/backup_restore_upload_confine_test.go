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
	rows    []models.Database
	err     error
	created []string
}

func (r *ucDBs) List(context.Context, repository.ListOptions) ([]models.Database, int64, error) {
	return r.rows, int64(len(r.rows)), r.err
}

func (r *ucDBs) Create(_ context.Context, d *models.Database) error {
	r.created = append(r.created, d.Name)
	return nil
}

// ucUsers answers the restore's lookups of the target account T (alice).
type ucUsers struct{ repository.UserRepository }

func (ucUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	alice := "alice"
	return &models.User{ID: id, Username: &alice}, nil
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
	h, a, calls := ucUploadPasses(t, func(int) string { return reply })
	sent := &map[string]any{}
	h.cfg.Agent.(*mockAgent).callFn = func(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
		*sent = params.(map[string]any)
		*calls = append(*calls, *sent)
		return json.RawMessage(reply), nil
	}
	return h, a, sent
}

// ucUploadPasses answers the n-th backup.restore_from_tar call (from 0) with
// reply(n) and records every call's params.
func ucUploadPasses(t *testing.T, reply func(n int) string) (*backupHandler, uploadRestoreArgs, *[]map[string]any) {
	t.Helper()
	dir := t.TempDir()
	tar := filepath.Join(dir, "up.tar.zst")
	if err := os.WriteFile(tar, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := &[]map[string]any{}
	ag := &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		if cmd != "backup.restore_from_tar" {
			return nil, fmt.Errorf("unexpected %s", cmd)
		}
		*calls = append(*calls, params.(map[string]any))
		return json.RawMessage(reply(len(*calls) - 1)), nil
	}}
	cfg := ucConfig()
	cfg.Agent = ag
	cfg.Users = ucUsers{}
	return &backupHandler{cfg: cfg}, uploadRestoreArgs{path: tar, outcomePath: filepath.Join(dir, "o.json"), username: "alice", targetID: "T"}, calls
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

// The agent restores mail only for the account's own domains, and a restore
// into a fresh account rebuilds those domains from the file's metadata after
// the agent ran. So mail is a second pass, with the domains looked up again.
func TestRunUploadRestore_RestoresMailAfterTheDomains(t *testing.T) {
	var h *backupHandler
	h, a, calls := ucUploadPasses(t, func(n int) string {
		if n == 0 {
			// The panel rebuilds the file's domains between the passes.
			doms := h.cfg.Domains.(*ucDomains)
			doms.rows = append(doms.rows, models.Domain{UserID: "T", Name: "new.org"})
			return `{"applied":["home → /home/alice"],"stages":[{"name":"home"},{"name":"mail"}],"upload_confinement_enforced":true}`
		}
		return `{"applied":["mail → alice (3 messages in 1 mailboxes)"],"upload_confinement_enforced":true}`
	})
	h.runUploadRestore(a)

	if len(*calls) != 2 {
		t.Fatalf("restore_from_tar ran %d times (%v), want 2: everything but mail, then mail", len(*calls), *calls)
	}
	first, second := (*calls)[0], (*calls)[1]
	if strings.Join(strs(first["skip_components"]), ",") != "mail" {
		t.Errorf("first pass skip_components = %v, want [mail]", first["skip_components"])
	}
	if strings.Join(strs(second["components"]), ",") != "mail" || second["mode"] != "upload" {
		t.Errorf("second pass = %v, want components [mail] in mode=upload", second)
	}
	if got := strings.Join(strs(second["allowed_mail_domains"]), ","); got != "alice.org,new.org" {
		t.Errorf("second pass allowed_mail_domains = %s, want alice.org,new.org (looked up after the domains were rebuilt)", got)
	}
	o, err := readRestoreUploadOutcome(a.outcomePath)
	if err != nil || o.Status != "done" || len(o.Applied) != 2 {
		t.Fatalf("outcome = %+v err=%v, want done with both passes' items", o, err)
	}
}

// No mail pass when the archive has no mail or the admin didn't select it.
func TestRunUploadRestore_NoMailPassWithoutMail(t *testing.T) {
	for name, c := range map[string]struct {
		components []string
		reply      string
	}{
		"archive without mail": {nil, `{"stages":[{"name":"home"}],"upload_confinement_enforced":true}`},
		"mail not selected":    {[]string{"home"}, `{"stages":[{"name":"home"},{"name":"mail"}],"upload_confinement_enforced":true}`},
	} {
		h, a, calls := ucUploadPasses(t, func(int) string { return c.reply })
		a.components = c.components
		h.runUploadRestore(a)
		if len(*calls) != 1 {
			t.Errorf("%s: restore_from_tar ran %d times, want 1", name, len(*calls))
		}
		if c.components != nil && (*calls)[0]["skip_components"] != nil {
			t.Errorf("%s: skip_components = %v, want none when mail isn't selected", name, (*calls)[0]["skip_components"])
		}
	}
}

// Apply registers database rows from the file only for the databases the
// agent restored into the account.
func TestRunUploadRestore_RegistersOnlyTheDatabasesTheAgentRestored(t *testing.T) {
	h, a, _ := ucUploadPasses(t, func(int) string {
		return `{"upload_confinement_enforced":true,"restored_databases":["alice_new"],"metadata":` +
			`{"user":{"id":"SRC","username":"alice"},"databases":[{"id":"d1","name":"alice_new","engine":"mariadb"},{"id":"d2","name":"alice_orphan","engine":"mariadb"}]}}`
	})
	h.runUploadRestore(a)

	dbs := h.cfg.Databases.(*ucDBs)
	if strings.Join(dbs.created, ",") != "alice_new" {
		t.Errorf("created database rows %v, want only alice_new", dbs.created)
	}
	o, _ := readRestoreUploadOutcome(a.outcomePath)
	if o == nil || !strings.Contains(strings.Join(o.Warnings, "|"), "alice_orphan): not restored: the restore didn't load its data") {
		t.Errorf("outcome %+v should say why alice_orphan was not registered", o)
	}
}
