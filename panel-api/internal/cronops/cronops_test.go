package cronops

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes ---

type fakeUsers struct{ u *models.User }

func (f fakeUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if f.u == nil || f.u.ID != id {
		return nil, repository.ErrNotFound
	}
	return f.u, nil
}

type fakeDomains struct{ docroots []string }

func (f fakeDomains) ListByUserID(_ context.Context, _ string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	ds := make([]models.Domain, 0, len(f.docroots))
	for _, d := range f.docroots {
		ds = append(ds, models.Domain{DocRoot: d})
	}
	return ds, int64(len(ds)), nil
}

type fakeCronRepo struct {
	created   *models.CronJob
	deleted   string
	deleteErr error // when set, Delete fails (metadata-failure case)
}

func (f *fakeCronRepo) Create(_ context.Context, j *models.CronJob) error { f.created = j; return nil }
func (f *fakeCronRepo) Delete(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = id
	return nil
}
func (f *fakeCronRepo) FindByID(_ context.Context, id string) (*models.CronJob, error) {
	if f.created != nil && f.created.ID == id {
		return f.created, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeCronRepo) Update(_ context.Context, j *models.CronJob) error { f.created = j; return nil }

type fakeAgent struct {
	called     bool
	method     string
	fail       bool
	lastParams any
}

func (f *fakeAgent) Call(_ context.Context, m string, p any) (json.RawMessage, error) {
	f.called, f.method, f.lastParams = true, m, p
	if f.fail {
		return nil, errors.New("agent boom")
	}
	return json.RawMessage(`{}`), nil
}

func uname(s string) *string { return &s }

func deps(u *models.User, agent *fakeAgent, cr *fakeCronRepo) Deps {
	return Deps{
		Users:    fakeUsers{u: u},
		Domains:  fakeDomains{docroots: []string{"/var/www/site"}},
		CronJobs: cr,
		Agent:    agent,
	}
}

// Drift reproducer (ADR-0101): Cron Job Intake must agent-apply
// synchronously for an enabled job AND roll the row back if apply
// fails — the contract REST had and the CLI silently lacked.
func TestCreate_EnabledAppliesAndRollsBackOnAgentFail(t *testing.T) {
	u := &models.User{ID: "u1", Username: uname("alice")}

	// happy path: enabled → agent cron.apply called, row persisted
	ag := &fakeAgent{}
	cr := &fakeCronRepo{}
	job, err := Create(context.Background(), deps(u, ag, cr), CreateInput{
		UserID: "u1", Name: "nightly", Schedule: "*/5 * * * *",
		Command: "/usr/bin/php -v", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !ag.called || ag.method != "cron.apply" {
		t.Fatalf("enabled create must agent-apply cron.apply, got called=%v method=%q", ag.called, ag.method)
	}
	if cr.created == nil || job == nil || cr.created.ID != job.ID {
		t.Fatal("job not persisted")
	}

	// agent failure → row rolled back, ErrAgentFailed
	ag2 := &fakeAgent{fail: true}
	cr2 := &fakeCronRepo{}
	_, err = Create(context.Background(), deps(u, ag2, cr2), CreateInput{
		UserID: "u1", Name: "nightly", Schedule: "*/5 * * * *",
		Command: "/usr/bin/php -v", Enabled: true,
	})
	if !errors.Is(err, ErrAgentFailed) {
		t.Fatalf("agent fail must return ErrAgentFailed, got %v", err)
	}
	if cr2.deleted != cr2.created.ID || cr2.created == nil {
		t.Fatalf("row must be rolled back on agent failure (deleted=%q)", cr2.deleted)
	}
}

func TestCreate_ValidationAndLinuxGate(t *testing.T) {
	withName := &models.User{ID: "u1", Username: uname("alice")}
	noLinux := &models.User{ID: "u1"} // Username nil

	for _, tc := range []struct {
		name string
		u    *models.User
		in   CreateInput
		want error
	}{
		{"bad name", withName, CreateInput{UserID: "u1", Name: "bad\x00", Schedule: "*/5 * * * *", Command: "/usr/bin/php -v"}, ErrNameInvalid},
		{"no linux account", noLinux, CreateInput{UserID: "u1", Name: "ok", Schedule: "*/5 * * * *", Command: "/usr/bin/php -v"}, cronvalidate.ErrNoLinuxAccount},
		{"bad schedule", withName, CreateInput{UserID: "u1", Name: "ok", Schedule: "nope", Command: "/usr/bin/php -v"}, ErrScheduleInvalid},
		{"bad command", withName, CreateInput{UserID: "u1", Name: "ok", Schedule: "*/5 * * * *", Command: "rm -rf /"}, ErrCommandInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag := &fakeAgent{}
			_, err := Create(context.Background(), deps(tc.u, ag, &fakeCronRepo{}), tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want errors.Is %v, got %v", tc.want, err)
			}
			if ag.called {
				t.Fatal("must not agent-apply when validation fails")
			}
		})
	}
}

func TestCreate_DisabledDoesNotApply(t *testing.T) {
	u := &models.User{ID: "u1", Username: uname("alice")}
	ag := &fakeAgent{}
	_, err := Create(context.Background(), deps(u, ag, &fakeCronRepo{}), CreateInput{
		UserID: "u1", Name: "nightly", Schedule: "*/5 * * * *",
		Command: "/usr/bin/php -v", Enabled: false,
	})
	if err != nil {
		t.Fatalf("create disabled: %v", err)
	}
	if ag.called {
		t.Fatal("disabled job must NOT agent-apply at intake")
	}
}

// Wire-contract guard for the canonical cron.apply / cron.remove
// structs (relocated here from api per ADR-0101). panel-agent
// cron.* parse these exact JSON keys — drift = silent runtime
// validation failure (feedback_cross_boundary_contracts). Change
// this AND panel-agent/internal/commands/cron_*.go together.
// Regression: a typed-nil agent (CLI path that ran requireDB instead
// of requireDBAndAgent → Agent: (*fakeAgent)(nil), boxed non-nil in the
// AgentCaller interface) must return ErrDeps, NOT SIGSEGV on Call.
// Guards both the missing-PreRunE wiring and the typed-nil interface trap.
func TestCreateUpdate_TypedNilAgent_ReturnsErrDeps(t *testing.T) {
	u := &models.User{ID: "u1", Username: uname("alice")}
	var nilAgent *fakeAgent // typed-nil; (AgentCaller)(nilAgent) != nil
	d := deps(u, nilAgent, &fakeCronRepo{})

	_, err := Create(context.Background(), d, CreateInput{
		UserID: "u1", Name: "t", Schedule: "*/15 * * * *",
		Command: "/usr/bin/php -v", Enabled: true,
	})
	if !errors.Is(err, ErrDeps) {
		t.Fatalf("Create typed-nil agent: want ErrDeps, got %v", err)
	}

	_, err = Update(context.Background(), d, "job1", UpdatePatch{})
	if !errors.Is(err, ErrDeps) {
		t.Fatalf("Update typed-nil agent: want ErrDeps, got %v", err)
	}
}

func TestCronWireShape(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload any
		want    []string
	}{
		{"cron.apply", applyParams{UserID: "u", Username: "s", JobID: "j", Name: "n", Command: "wp cron", Schedule: "0 * * * *", OwnedDocroots: []string{"/x"}},
			[]string{"command", "job_id", "name", "owned_docroots", "owned_domains", "schedule", "user_id", "username"}},
		{"cron.remove", removeParams{UserID: "u", Username: "s", JobID: "j"},
			[]string{"job_id", "user_id", "username"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.payload)
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := make([]string, 0, len(m))
			for k := range m {
				got = append(got, k)
			}
			sort.Strings(got)
			if len(got) != len(tc.want) {
				t.Fatalf("key count: got %v want %v", got, tc.want)
			}
			for i, k := range got {
				if k != tc.want[i] {
					t.Fatalf("key[%d]=%q want %q", i, k, tc.want[i])
				}
			}
		})
	}
}

// JAB-293: Delete synchronously removes the host timer and drops the row only
// on success; agent failure keeps the row for retry; a missing owner skips the
// (impossible) agent call; a root job carries RunAsRoot in the remove params.
func seedJob(cr *fakeCronRepo, j *models.CronJob) { cr.created = j }

func TestDelete_HappyRemovesTimerThenRow(t *testing.T) {
	u := &models.User{ID: "u1", Username: uname("alice")}
	cr := &fakeCronRepo{}
	seedJob(cr, &models.CronJob{ID: "j1", UserID: "u1", Name: "nightly", Schedule: "*/5 * * * *"})
	ag := &fakeAgent{}

	if err := Delete(context.Background(), deps(u, ag, cr), "j1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !ag.called || ag.method != "cron.remove" {
		t.Fatalf("delete must synchronously agent cron.remove; called=%v method=%q", ag.called, ag.method)
	}
	if cr.deleted != "j1" {
		t.Fatalf("row must be deleted after host success; deleted=%q", cr.deleted)
	}
}

func TestDelete_AgentFailureKeepsRow(t *testing.T) {
	// The critical fix: a failed host removal must NOT delete the row (a last-job
	// orphan would otherwise leave a live timer the reconciler never sweeps).
	u := &models.User{ID: "u1", Username: uname("alice")}
	cr := &fakeCronRepo{}
	seedJob(cr, &models.CronJob{ID: "j1", UserID: "u1", Name: "nightly", Schedule: "*/5 * * * *"})
	ag := &fakeAgent{fail: true}

	err := Delete(context.Background(), deps(u, ag, cr), "j1")
	if !errors.Is(err, ErrAgentFailed) {
		t.Fatalf("agent failure must return ErrAgentFailed, got %v", err)
	}
	if cr.deleted != "" {
		t.Fatalf("row must be KEPT on agent failure (retry handle); deleted=%q", cr.deleted)
	}
}

func TestDelete_MissingOwnerSkipsAgentDeletesRow(t *testing.T) {
	// Owner has no Linux account (deleted user): nothing user-scoped to remove,
	// so skip the agent and drop the row.
	u := &models.User{ID: "u1", Username: nil}
	cr := &fakeCronRepo{}
	seedJob(cr, &models.CronJob{ID: "j1", UserID: "u1", Name: "nightly", Schedule: "*/5 * * * *"})
	ag := &fakeAgent{}

	if err := Delete(context.Background(), deps(u, ag, cr), "j1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if ag.called {
		t.Fatal("missing-owner delete must not call the agent")
	}
	if cr.deleted != "j1" {
		t.Fatalf("row must still be deleted for a userless job; deleted=%q", cr.deleted)
	}
}

func TestDelete_RootJobCarriesRunAsRootInRemoveParams(t *testing.T) {
	cr := &fakeCronRepo{}
	seedJob(cr, &models.CronJob{ID: "j1", UserID: "u1", Name: "sysjob", Schedule: "0 * * * *", RunAsRoot: true})
	ag := &fakeAgent{}

	if err := Delete(context.Background(), deps(&models.User{ID: "u1"}, ag, cr), "j1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rp, ok := ag.lastParams.(removeParams)
	if !ok {
		t.Fatalf("remove params type = %T", ag.lastParams)
	}
	if !rp.RunAsRoot || rp.Username != "root" {
		t.Fatalf("root job must remove with RunAsRoot + username=root; got %+v", rp)
	}
	if cr.deleted != "j1" {
		t.Fatalf("root job row not deleted; deleted=%q", cr.deleted)
	}
}

func TestDelete_NotFound(t *testing.T) {
	err := Delete(context.Background(), deps(&models.User{ID: "u1"}, &fakeAgent{}, &fakeCronRepo{}), "nope")
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("want ErrJobNotFound, got %v", err)
	}
}

func TestDelete_MetadataFailureAfterRemove(t *testing.T) {
	// Host timer removed, but the row delete fails → ErrInternal (the timer is
	// gone; cron.remove is idempotent on the next retry).
	u := &models.User{ID: "u1", Username: uname("alice")}
	cr := &fakeCronRepo{deleteErr: errors.New("db down")}
	seedJob(cr, &models.CronJob{ID: "j1", UserID: "u1", Name: "nightly", Schedule: "*/5 * * * *"})
	ag := &fakeAgent{}

	err := Delete(context.Background(), deps(u, ag, cr), "j1")
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("row-delete failure must return ErrInternal, got %v", err)
	}
	if !ag.called {
		t.Fatal("agent remove should have run before the row delete")
	}
}
