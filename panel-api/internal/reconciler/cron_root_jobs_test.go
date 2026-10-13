package reconciler

// JAB-293 root jobs are system units that run as root, owned by an admin. The
// cron pass skipped every admin owner (no Linux account), so a root job was
// never re-applied — a unit kept whatever it was first written with — and its
// Last run was never harvested. Root jobs of an admin are now converged
// through their system units; a root row owned by a tenant must never be
// applied as root.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type cronCall struct {
	method    string
	username  string
	jobID     string
	runAsRoot bool
}

type cronRecordingAgent struct {
	mu    sync.Mutex
	calls []cronCall
}

func (a *cronRecordingAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, _ := params.(map[string]any)
	c := cronCall{method: method}
	c.username, _ = p["username"].(string)
	c.jobID, _ = p["job_id"].(string)
	c.runAsRoot, _ = p["run_as_root"].(bool)
	a.calls = append(a.calls, c)
	if method == "cron.list" {
		return json.RawMessage(`{"unit_files":[]}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (a *cronRecordingAgent) find(method, jobID string) (cronCall, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.calls {
		if c.method == method && c.jobID == jobID {
			return c, true
		}
	}
	return cronCall{}, false
}

func TestReconcileCron_AdminRootJobsGoThroughTheirSystemUnits(t *testing.T) {
	rootJob := &models.CronJob{ID: "01JR00TJ0B0000000000000001", UserID: "u-admin", Enabled: true, RunAsRoot: true, Command: "python3 /root/report.py"}
	rootOff := &models.CronJob{ID: "01JR00TJ0B0000000000000002", UserID: "u-admin", Enabled: false, RunAsRoot: true}
	adminPlain := &models.CronJob{ID: "01JR00TJ0B0000000000000003", UserID: "u-admin", Enabled: true}
	tenantRoot := &models.CronJob{ID: "01JR00TJ0B0000000000000004", UserID: "u-alice", Enabled: true, RunAsRoot: true}
	jobs := []*models.CronJob{rootJob, rootOff, adminPlain, tenantRoot}
	byID := map[string]*models.CronJob{}
	for _, j := range jobs {
		byID[j.ID] = j
	}
	ag := &cronRecordingAgent{}
	r := newCronSweepReconciler(nil, &fakeCronRepo{listed: jobs, byID: byID},
		cronSweepUser("u-admin", "", true), cronSweepUser("u-alice", "alice", false))
	r.agent = ag

	r.reconcileCronJobs(context.Background())

	for _, want := range []cronCall{
		{"cron.apply", "root", rootJob.ID, true},
		{"cron.status", "root", rootJob.ID, true},
		{"cron.remove", "root", rootOff.ID, true},
	} {
		got, ok := ag.find(want.method, want.jobID)
		if !ok || got != want {
			t.Errorf("%s %s: got %+v (found=%v), want %+v", want.method, want.jobID, got, ok, want)
		}
	}
	if c, ok := ag.find("cron.apply", adminPlain.ID); ok {
		t.Errorf("an admin's non-root job has no account to run in and must stay skipped; got %+v", c)
	}
	// A root row owned by a tenant breaks cronops' invariant; it must never
	// be applied as root.
	if c, ok := ag.find("cron.apply", tenantRoot.ID); ok && (c.runAsRoot || c.username != "alice") {
		t.Fatalf("a tenant-owned root row was applied as %+v; it must never run as root", c)
	}
}
