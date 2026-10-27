package userops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type chownUsers struct {
	repository.UserRepository
	byID map[string]*models.User
}

func (f *chownUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

type chownDomains struct {
	repository.DomainRepository
	transferredTo, newDocRoot string
	transferCalled            bool
	// agent, when set, has its calls so far counted at TransferOwner.
	agent           *chownAgent
	callsAtTransfer int
}

func (f *chownDomains) TransferOwner(_ context.Context, _ string, newUserID, newDocRoot string) error {
	f.transferCalled = true
	f.transferredTo, f.newDocRoot = newUserID, newDocRoot
	if f.agent != nil {
		f.callsAtTransfer = len(f.agent.calls)
	}
	return nil
}

type chownAgent struct {
	called     bool
	lastMethod string
	lastParams map[string]any
	// calls is every call as "method" or "method:app_id".
	calls []string
	// failMethod makes calls to that method fail.
	failMethod string
}

func (a *chownAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.called = true
	a.lastMethod = method
	call := method
	if m, ok := params.(map[string]any); ok {
		a.lastParams = m
		if id, ok := m["app_id"].(string); ok {
			call += ":" + id
		}
	}
	a.calls = append(a.calls, call)
	if method == a.failMethod {
		return nil, errors.New("agent: " + method + " failed")
	}
	return json.RawMessage(`{}`), nil
}

type chownPyApps struct {
	repository.PythonAppRepository
	apps []*models.PythonApp
	err  error
}

func (f *chownPyApps) ListByDomain(_ context.Context, _ string) ([]*models.PythonApp, error) {
	return f.apps, f.err
}

type chownApps struct {
	repository.ApplicationInstallRepository
	install *models.ApplicationInstall
}

func (f *chownApps) FindByDomainID(_ context.Context, _ string) (*models.ApplicationInstall, error) {
	if f.install != nil {
		return f.install, nil
	}
	return nil, repository.ErrNotFound
}

func cuidptr(u uint32) *uint32 { return &u }

func chownFixture() (Deps, ChownDeps, *models.Domain, *models.User, *chownDomains, *chownAgent) {
	oldOwner := &models.User{ID: "old", Username: strptr("alice"), LinuxUID: cuidptr(1001)}
	newOwner := &models.User{ID: "new", Username: strptr("bob"), LinuxUID: cuidptr(1002)}
	users := &chownUsers{byID: map[string]*models.User{"old": oldOwner, "new": newOwner}}
	doms := &chownDomains{}
	ag := &chownAgent{}
	d := Deps{Users: users, Domains: doms, Agent: ag}
	cd := ChownDeps{AppInstalls: &chownApps{}}
	domain := &models.Domain{ID: "d1", Name: "site.example", UserID: "old", DocRoot: "/home/alice/domains/site.example/public_html"}
	return d, cd, domain, newOwner, doms, ag
}

func TestChangeDomainOwner_HappyPath(t *testing.T) {
	d, cd, domain, newOwner, doms, ag := chownFixture()

	if err := ChangeDomainOwner(context.Background(), d, cd, domain, newOwner); err != nil {
		t.Fatalf("chown: %v", err)
	}
	if !ag.called || ag.lastMethod != "domain.reown" {
		t.Fatalf("agent domain.reown not called; called=%v method=%q", ag.called, ag.lastMethod)
	}
	wantNew := "/home/bob/domains/site.example/public_html"
	if ag.lastParams["old_doc_root"] != "/home/alice/domains/site.example/public_html" || ag.lastParams["new_doc_root"] != wantNew {
		t.Errorf("agent docroots wrong: %v", ag.lastParams)
	}
	if ag.lastParams["new_uid"] != 1002 {
		t.Errorf("agent new_uid = %v, want 1002", ag.lastParams["new_uid"])
	}
	if !doms.transferCalled || doms.transferredTo != "new" || doms.newDocRoot != wantNew {
		t.Errorf("TransferOwner wrong: called=%v to=%q docroot=%q", doms.transferCalled, doms.transferredTo, doms.newDocRoot)
	}
	if domain.UserID != "new" || domain.DocRoot != wantNew {
		t.Errorf("domain not updated in place: owner=%q docroot=%q", domain.UserID, domain.DocRoot)
	}
}

func TestChangeDomainOwner_Refusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Deps, *ChownDeps, *models.Domain, *models.User)
		want   string
	}{
		{"app install present", func(_ *Deps, cd *ChownDeps, _ *models.Domain, _ *models.User) {
			cd.AppInstalls.(*chownApps).install = &models.ApplicationInstall{AppType: "wordpress"}
		}, "app install"},
		{"new owner is admin", func(_ *Deps, _ *ChownDeps, _ *models.Domain, no *models.User) {
			no.IsAdmin = true
		}, "must be a tenant"},
		{"already owned", func(_ *Deps, _ *ChownDeps, dom *models.Domain, no *models.User) {
			dom.UserID = no.ID
		}, "already owned"},
		{"docroot not under owner home", func(_ *Deps, _ *ChownDeps, dom *models.Domain, _ *models.User) {
			dom.DocRoot = "/home/someoneelse/x/public_html"
		}, "not under the current owner"},
		{"new owner no uid", func(_ *Deps, _ *ChownDeps, _ *models.Domain, no *models.User) {
			no.LinuxUID = nil
		}, "no linux_uid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, cd, domain, newOwner, _, ag := chownFixture()
			tc.mutate(&d, &cd, domain, newOwner)

			err := ChangeDomainOwner(context.Background(), d, cd, domain, newOwner)
			if err == nil {
				t.Fatalf("expected refusal, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.want)
			}
			if ag.called {
				t.Errorf("agent must NOT be called on a refused owner-change (files must stay put)")
			}
		})
	}
}

// GH #2289: the change of owner deletes the domain's Python apps, and their
// units are stopped once it has, not before: with the rows still there an
// apply would write them back.
func TestChangeDomainOwner_StopsThePythonApps(t *testing.T) {
	d, cd, domain, newOwner, doms, ag := chownFixture()
	cd.PythonApps = &chownPyApps{apps: []*models.PythonApp{{ID: "app1"}, {ID: "app2"}}}
	doms.agent = ag

	if err := ChangeDomainOwner(context.Background(), d, cd, domain, newOwner); err != nil {
		t.Fatalf("chown: %v", err)
	}
	want := []string{"domain.reown", "app.python.remove:app1", "app.python.remove:app2"}
	if !reflect.DeepEqual(ag.calls, want) {
		t.Errorf("agent calls = %v, want %v", ag.calls, want)
	}
	if doms.callsAtTransfer != 1 {
		t.Errorf("TransferOwner ran after %d agent calls, want 1 (the move)", doms.callsAtTransfer)
	}
}

// A failed listing stops the change before anything moves.
func TestChangeDomainOwner_PythonAppListFailureMovesNothing(t *testing.T) {
	d, cd, domain, newOwner, doms, ag := chownFixture()
	cd.PythonApps = &chownPyApps{err: errors.New("db down")}

	err := ChangeDomainOwner(context.Background(), d, cd, domain, newOwner)
	if err == nil || !strings.Contains(err.Error(), "Python apps") {
		t.Fatalf("err = %v, want the listing failure", err)
	}
	if ag.called || doms.transferCalled {
		t.Errorf("agent called=%v, TransferOwner called=%v; want neither", ag.called, doms.transferCalled)
	}
}

// A unit that fails to stop doesn't fail the change, which already happened:
// the reconciler's sweep removes the unit.
func TestChangeDomainOwner_PythonAppRemoveFailureIsNotFatal(t *testing.T) {
	d, cd, domain, newOwner, doms, ag := chownFixture()
	d.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	cd.PythonApps = &chownPyApps{apps: []*models.PythonApp{{ID: "app1"}, {ID: "app2"}}}
	ag.failMethod = "app.python.remove"

	if err := ChangeDomainOwner(context.Background(), d, cd, domain, newOwner); err != nil {
		t.Fatalf("chown: %v", err)
	}
	if !doms.transferCalled || domain.UserID != "new" {
		t.Errorf("owner change not kept: TransferOwner=%v owner=%q", doms.transferCalled, domain.UserID)
	}
	want := []string{"domain.reown", "app.python.remove:app1", "app.python.remove:app2"}
	if !reflect.DeepEqual(ag.calls, want) {
		t.Errorf("agent calls = %v, want %v", ag.calls, want)
	}
}
