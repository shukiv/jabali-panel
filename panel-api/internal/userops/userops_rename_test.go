package userops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- minimal fakes (embed the interface; only the used methods are real) ---

type renameUsers struct {
	repository.UserRepository
	existing     map[string]*models.User // by username, for FindByUsername
	renamedTo    string
	renameCalled bool
}

func (f *renameUsers) FindByUsername(_ context.Context, u string) (*models.User, error) {
	if m, ok := f.existing[u]; ok {
		return m, nil
	}
	return nil, repository.ErrNotFound
}
func (f *renameUsers) UpdateUsername(_ context.Context, _ string, username string) error {
	f.renameCalled = true
	f.renamedTo = username
	return nil
}

type renameDomains struct {
	repository.DomainRepository
	rewroteFrom, rewroteTo string
	rewriteCalled          bool
}

func (f *renameDomains) RewriteDocRootPrefix(_ context.Context, _ string, oldP, newP string) (int64, error) {
	f.rewriteCalled = true
	f.rewroteFrom, f.rewroteTo = oldP, newP
	return 1, nil
}
func (f *renameDomains) ListByUserID(_ context.Context, _ string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	return nil, 0, nil
}

type renameAgent struct {
	called     bool
	lastMethod string
	lastParams map[string]any
	returnErr  error
}

func (a *renameAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.called = true
	a.lastMethod = method
	if m, ok := params.(map[string]any); ok {
		a.lastParams = m
	}
	if a.returnErr != nil {
		return nil, a.returnErr
	}
	return json.RawMessage(`{}`), nil
}

type renameFtp struct {
	repository.FtpAccountRepository
	count int64
}

func (f *renameFtp) CountByUserID(_ context.Context, _ string) (int64, error) { return f.count, nil }

type renamePython struct {
	repository.PythonAppRepository
	apps []*models.PythonApp
}

func (f *renamePython) ListByUser(_ context.Context, _ string) ([]*models.PythonApp, error) {
	return f.apps, nil
}

func uidptr(u uint32) *uint32 { return &u }

func baseTarget() *models.User {
	return &models.User{ID: "u1", Username: strptr("alice"), LinuxUID: uidptr(1001)}
}

func TestRenameUser_HappyPath(t *testing.T) {
	users := &renameUsers{existing: map[string]*models.User{}}
	doms := &renameDomains{}
	ag := &renameAgent{}
	d := Deps{Users: users, Domains: doms, Agent: ag}
	rd := RenameDeps{FtpAccounts: &renameFtp{}, PythonApps: &renamePython{}}
	target := baseTarget()

	if err := RenameUser(context.Background(), d, rd, target, "bob"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !ag.called || ag.lastMethod != "user.rename" {
		t.Fatalf("agent user.rename not called; called=%v method=%q", ag.called, ag.lastMethod)
	}
	if ag.lastParams["old_username"] != "alice" || ag.lastParams["new_username"] != "bob" {
		t.Errorf("agent params wrong: %v", ag.lastParams)
	}
	if !users.renameCalled || users.renamedTo != "bob" {
		t.Errorf("UpdateUsername not applied: called=%v to=%q", users.renameCalled, users.renamedTo)
	}
	if !doms.rewriteCalled || doms.rewroteFrom != "/home/alice" || doms.rewroteTo != "/home/bob" {
		t.Errorf("docroot rewrite wrong: called=%v %q->%q", doms.rewriteCalled, doms.rewroteFrom, doms.rewroteTo)
	}
	if target.Username == nil || *target.Username != "bob" {
		t.Errorf("target.Username not updated: %v", target.Username)
	}
}

func TestRenameUser_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Deps, *RenameDeps, **models.User)
		want   string // substring
	}{
		{"name taken", func(d *Deps, _ *RenameDeps, _ **models.User) {
			d.Users.(*renameUsers).existing["bob"] = &models.User{ID: "u2"}
		}, "already taken"},
		{"has ftp subaccounts", func(_ *Deps, rd *RenameDeps, _ **models.User) {
			rd.FtpAccounts.(*renameFtp).count = 3
		}, "FTP/SFTP subaccount"},
		{"has python apps", func(_ *Deps, rd *RenameDeps, _ **models.User) {
			rd.PythonApps.(*renamePython).apps = []*models.PythonApp{{}}
		}, "Python app"},
		{"admin", func(_ *Deps, _ *RenameDeps, tp **models.User) {
			(*tp).IsAdmin = true
		}, "tenant accounts"},
		{"same name", func(_ *Deps, _ *RenameDeps, tp **models.User) {
			(*tp).Username = strptr("bob")
		}, "same as the current"},
		{"no linux_uid", func(_ *Deps, _ *RenameDeps, tp **models.User) {
			(*tp).LinuxUID = nil
		}, "no linux_uid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			users := &renameUsers{existing: map[string]*models.User{}}
			ag := &renameAgent{}
			d := Deps{Users: users, Domains: &renameDomains{}, Agent: ag}
			rd := RenameDeps{FtpAccounts: &renameFtp{}, PythonApps: &renamePython{}}
			target := baseTarget()
			tc.mutate(&d, &rd, &target)

			err := RenameUser(context.Background(), d, rd, target, "bob")
			if err == nil {
				t.Fatalf("expected refusal, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.want)
			}
			if ag.called {
				t.Errorf("agent must NOT be called on a refused rename (box must stay untouched)")
			}
		})
	}
}

func TestRenameUser_AgentFailureStopsBeforeDB(t *testing.T) {
	users := &renameUsers{existing: map[string]*models.User{}}
	ag := &renameAgent{returnErr: errors.New("usermod boom")}
	d := Deps{Users: users, Domains: &renameDomains{}, Agent: ag}
	rd := RenameDeps{FtpAccounts: &renameFtp{}, PythonApps: &renamePython{}}

	if err := RenameUser(context.Background(), d, rd, baseTarget(), "bob"); err == nil {
		t.Fatal("expected agent failure to propagate")
	}
	if users.renameCalled {
		t.Error("DB username must NOT be updated when the agent rename failed")
	}
}

// GH #1238 stub.
func (f *renameUsers) UpdateShadowDBUsernames(context.Context, string, *string, *string) error {
	return nil
}
