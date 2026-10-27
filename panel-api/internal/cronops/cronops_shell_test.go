package cronops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2290: a tenant job may run the account's own .sh script when the
// account's hosting package includes SSH; root jobs keep the closed list.

type fakePkgs map[string]*models.HostingPackage

func (f fakePkgs) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if p, ok := f[id]; ok {
		return p, nil
	}
	return nil, repository.ErrNotFound
}

func shellUser(pkgID string) *models.User {
	u := &models.User{ID: "u1", Username: uname("alice")}
	if pkgID != "" {
		u.PackageID = &pkgID
	}
	return u
}

var shellPkgs = fakePkgs{
	"ssh":   {ID: "ssh", SSHEnabled: true},
	"nossh": {ID: "nossh"},
}

func shellDeps(u *models.User, ag *fakeAgent, cr *fakeCronRepo) Deps {
	d := deps(u, ag, cr)
	d.Packages = shellPkgs
	return d
}

const shellCmd = "bash /home/alice/bin/backup.sh"

func isShellRefusal(err error) bool {
	var ve *cronvalidate.ValidationError
	return errors.Is(err, ErrCommandInvalid) && errors.As(err, &ve) && ve.Code == cronvalidate.ErrCodeShellNeedsSSH
}

func TestCreate_ShellScriptWithSSH(t *testing.T) {
	ag, cr := &fakeAgent{}, &fakeCronRepo{}
	if _, err := Create(context.Background(), shellDeps(shellUser("ssh"), ag, cr), CreateInput{
		UserID: "u1", Name: "backup", Schedule: "0 3 * * *", Command: shellCmd, Enabled: true,
	}); err != nil {
		t.Fatalf("create with SSH: %v", err)
	}
	p, ok := ag.lastParams.(applyParams)
	if !ok || !p.AllowShellScripts {
		t.Errorf("cron.apply params = %+v, want allow_shell_scripts", ag.lastParams)
	}
}

func TestCreate_ShellScriptRefusedWithoutSSH(t *testing.T) {
	cases := map[string]Deps{
		"package without SSH": shellDeps(shellUser("nossh"), &fakeAgent{}, &fakeCronRepo{}),
		"no package":          shellDeps(shellUser(""), &fakeAgent{}, &fakeCronRepo{}),
		"package not found":   shellDeps(shellUser("gone"), &fakeAgent{}, &fakeCronRepo{}),
		"no package lookup":   deps(shellUser("ssh"), &fakeAgent{}, &fakeCronRepo{}),
	}
	for name, d := range cases {
		_, err := Create(context.Background(), d, CreateInput{
			UserID: "u1", Name: "backup", Schedule: "0 3 * * *", Command: shellCmd, Enabled: true,
		})
		if !isShellRefusal(err) {
			t.Errorf("%s: got %v, want %s", name, err, cronvalidate.ErrCodeShellNeedsSSH)
		}
		if d.Agent.(*fakeAgent).called {
			t.Errorf("%s: agent called for a refused job", name)
		}
	}
}

// A root job keeps the closed list, even for an admin whose package has SSH.
func TestCreate_RootJobNeverRunsAShellScript(t *testing.T) {
	admin := &models.User{ID: "u1", Username: uname("admin"), IsAdmin: true, PackageID: func() *string { s := "ssh"; return &s }()}
	_, err := Create(context.Background(), shellDeps(admin, &fakeAgent{}, &fakeCronRepo{}), CreateInput{
		UserID: "u1", Name: "backup", Schedule: "0 3 * * *", Command: "bash /root/bin/backup.sh", Enabled: true, RunAsRoot: true,
	})
	if !isShellRefusal(err) {
		t.Fatalf("root shell job: got %v, want %s", err, cronvalidate.ErrCodeShellNeedsSSH)
	}
}

// A package that lost SSH: turning the job on, or editing it, is refused
// before anything is saved; the reconciler has already stopped its timer.
func TestUpdate_ShellJobNeedsSSHToBeOn(t *testing.T) {
	ag, cr := &fakeAgent{}, &fakeCronRepo{}
	seedJob(cr, &models.CronJob{ID: "j1", UserID: "u1", Name: "backup", Command: shellCmd, Schedule: "0 3 * * *", Enabled: false})
	on := true
	_, err := Update(context.Background(), shellDeps(shellUser("nossh"), ag, cr), "j1", UpdatePatch{Enabled: &on})
	if !isShellRefusal(err) {
		t.Fatalf("enable without SSH: got %v, want %s", err, cronvalidate.ErrCodeShellNeedsSSH)
	}
	if cr.created.Enabled || ag.called {
		t.Errorf("refused enable was saved (%v) or applied (%v)", cr.created.Enabled, ag.called)
	}
	// Turning it off stays possible.
	off := false
	cr.created.Enabled = true
	if _, err := Update(context.Background(), shellDeps(shellUser("nossh"), ag, cr), "j1", UpdatePatch{Enabled: &off}); err != nil {
		t.Fatalf("disable without SSH: %v", err)
	}
	// With SSH back, it turns on and the agent is told.
	if _, err := Update(context.Background(), shellDeps(shellUser("ssh"), ag, cr), "j1", UpdatePatch{Enabled: &on}); err != nil {
		t.Fatalf("enable with SSH: %v", err)
	}
	if p, ok := ag.lastParams.(applyParams); !ok || !p.AllowShellScripts {
		t.Errorf("cron.apply params = %+v, want allow_shell_scripts", ag.lastParams)
	}
}

func TestShellPolicy(t *testing.T) {
	ctx := context.Background()
	if !ShellPolicy(ctx, fakeUsers{u: shellUser("ssh")}, shellPkgs, "u1", false).ShellScripts {
		t.Error("package with SSH: want shell scripts")
	}
	if ShellPolicy(ctx, fakeUsers{u: shellUser("ssh")}, shellPkgs, "u1", true).ShellScripts {
		t.Error("root job: want the closed list")
	}
	if ShellPolicy(ctx, fakeUsers{u: shellUser("ssh")}, nil, "u1", false).ShellScripts {
		t.Error("no package lookup: want the closed list")
	}
	if ShellPolicy(ctx, fakeUsers{u: shellUser("ssh")}, shellPkgs, "other", false).ShellScripts {
		t.Error("unknown account: want the closed list")
	}
}
