package reconciler

// GH #2290: a tenant job may run the account's own .sh script while the
// account's package includes SSH. The reconciler tells the agent so on
// cron.apply; when the package has no SSH (taken away, or never there) the
// job's timer is removed instead, so the script stops running.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type cronShellPkgs struct {
	repository.PackageRepository
	byID map[string]*models.HostingPackage
}

func (f *cronShellPkgs) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if p, ok := f.byID[id]; ok {
		return p, nil
	}
	return nil, repository.ErrNotFound
}

type cronParamsAgent struct {
	mu    sync.Mutex
	calls map[string]map[string]any // method + " " + job_id
}

func (a *cronParamsAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, _ := params.(map[string]any)
	id, _ := p["job_id"].(string)
	if a.calls == nil {
		a.calls = map[string]map[string]any{}
	}
	a.calls[method+" "+id] = p
	if method == "cron.list" {
		return json.RawMessage(`{"unit_files":[]}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (a *cronParamsAgent) call(method, jobID string) (map[string]any, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, ok := a.calls[method+" "+jobID]
	return p, ok
}

func TestReconcileCron_ShellScriptNeedsSSH(t *testing.T) {
	withSSH, noSSH := "p-ssh", "p-nossh"
	alice := cronSweepUser("u-alice", "alice", false)
	alice.PackageID = &withSSH
	bob := cronSweepUser("u-bob", "bob", false)
	bob.PackageID = &noSSH
	carol := cronSweepUser("u-carol", "carol", false) // no package

	aliceShell := &models.CronJob{ID: "01JR00TJ0B00000000000SHEL1", UserID: "u-alice", Enabled: true, Command: "bash /home/alice/bin/backup.sh"}
	bobShell := &models.CronJob{ID: "01JR00TJ0B00000000000SHEL2", UserID: "u-bob", Enabled: true, Command: "/home/bob/bin/backup.sh"}
	bobPHP := &models.CronJob{ID: "01JR00TJ0B00000000000PHP02", UserID: "u-bob", Enabled: true, Command: "php /home/bob/public_html/cron.php"}
	carolShell := &models.CronJob{ID: "01JR00TJ0B00000000000SHEL3", UserID: "u-carol", Enabled: true, Command: "sh /home/carol/run.sh"}
	jobs := []*models.CronJob{aliceShell, bobShell, bobPHP, carolShell}
	byID := map[string]*models.CronJob{}
	for _, j := range jobs {
		byID[j.ID] = j
	}
	ag := &cronParamsAgent{}
	r := newCronSweepReconciler(nil, &fakeCronRepo{listed: jobs, byID: byID}, alice, bob, carol).
		WithPackages(&cronShellPkgs{byID: map[string]*models.HostingPackage{
			withSSH: {ID: withSSH, SSHEnabled: true},
			noSSH:   {ID: noSSH},
		}})
	r.agent = ag

	r.reconcileCronJobs(context.Background())

	p, ok := ag.call("cron.apply", aliceShell.ID)
	if !ok || p["allow_shell_scripts"] != true {
		t.Errorf("package with SSH: cron.apply %v (sent=%v), want allow_shell_scripts", p, ok)
	}
	for _, j := range []*models.CronJob{bobShell, carolShell} {
		if p, ok := ag.call("cron.apply", j.ID); ok {
			t.Errorf("job %s: applied without SSH: %v", j.ID, p)
		}
		if _, ok := ag.call("cron.remove", j.ID); !ok {
			t.Errorf("job %s: its timer must be removed while the package has no SSH", j.ID)
		}
	}
	// The account's other jobs carry on, without the shell permission.
	p, ok = ag.call("cron.apply", bobPHP.ID)
	if !ok {
		t.Fatalf("job %s: not applied", bobPHP.ID)
	}
	if p["allow_shell_scripts"] == true {
		t.Errorf("package without SSH: cron.apply %v carries allow_shell_scripts", p)
	}
}

// A root job keeps the closed list: the agent is never told it may run a
// shell script, even when the admin's package has SSH.
func TestReconcileCron_RootJobNeverGetsShellScripts(t *testing.T) {
	withSSH := "p-ssh"
	admin := cronSweepUser("u-admin", "", true)
	admin.PackageID = &withSSH
	rootJob := &models.CronJob{ID: "01JR00TJ0B00000000000ROOT1", UserID: "u-admin", Enabled: true, RunAsRoot: true, Command: "python3 /root/report.py"}
	ag := &cronParamsAgent{}
	r := newCronSweepReconciler(nil, &fakeCronRepo{listed: []*models.CronJob{rootJob}, byID: map[string]*models.CronJob{rootJob.ID: rootJob}}, admin).
		WithPackages(&cronShellPkgs{byID: map[string]*models.HostingPackage{withSSH: {ID: withSSH, SSHEnabled: true}}})
	r.agent = ag

	r.reconcileCronJobs(context.Background())

	p, ok := ag.call("cron.apply", rootJob.ID)
	if !ok {
		t.Fatalf("root job not applied")
	}
	if p["allow_shell_scripts"] == true {
		t.Errorf("root job: cron.apply %v carries allow_shell_scripts", p)
	}
}
