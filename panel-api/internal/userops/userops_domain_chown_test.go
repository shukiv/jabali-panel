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
	// owned is what ListByUserID returns for the old owner; listErr fails it.
	owned   []models.Domain
	listErr error
}

func (f *chownDomains) ListByUserID(_ context.Context, _ string, _ repository.ListOptions) ([]models.Domain, int64, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.owned, int64(len(f.owned)), nil
}

type chownFtp struct {
	accts []models.FtpAccount
	err   error
}

func (f *chownFtp) ListByUserID(_ context.Context, _ string) ([]models.FtpAccount, error) {
	return f.accts, f.err
}

func (f *chownFtp) List(_ context.Context) ([]models.FtpAccount, error) { return f.accts, f.err }

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
	domain := &models.Domain{ID: "d1", Name: "site.example", UserID: "old", DocRoot: "/home/alice/domains/site.example/public_html"}
	// The old owner's domains include the one changing owner (its row is the
	// same one, so ListByUserID returns it too).
	doms := &chownDomains{owned: []models.Domain{{ID: "d1", Name: "site.example", UserID: "old", DocRoot: domain.DocRoot}}}
	ag := &chownAgent{}
	d := Deps{Users: users, Domains: doms, Agent: ag, FtpAccounts: &chownFtp{}}
	cd := ChownDeps{AppInstalls: &chownApps{}}
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
	// The whole domain folder moves (GH #2541); the docroot keeps its place
	// inside it.
	wantNew := "/home/bob/domains/site.example/public_html"
	if ag.lastParams["old_doc_root"] != "/home/alice/domains/site.example" || ag.lastParams["new_doc_root"] != "/home/bob/domains/site.example" {
		t.Errorf("agent move wrong: %v", ag.lastParams)
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

// GH #2541: when the docroot is the domain's folder or inside it, the whole
// folder moves, so nothing next to the docroot (a Laravel app's code next to
// public_html/public, logs, .env) is left behind under the old owner. A
// docroot outside it moves on its own. Nothing is left empty to prune.
func TestChangeDomainOwner_WhatMoves(t *testing.T) {
	for _, tc := range []struct {
		docRoot, from, to, newDocRoot string
	}{
		{"/home/alice/domains/site.example/public_html",
			"/home/alice/domains/site.example", "/home/bob/domains/site.example",
			"/home/bob/domains/site.example/public_html"},
		{"/home/alice/domains/site.example/public_html/public",
			"/home/alice/domains/site.example", "/home/bob/domains/site.example",
			"/home/bob/domains/site.example/public_html/public"},
		{"/home/alice/domains/site.example",
			"/home/alice/domains/site.example", "/home/bob/domains/site.example",
			"/home/bob/domains/site.example"},
		// A folder whose name only starts with the domain's is not its folder.
		{"/home/alice/domains/site.example-old/public_html",
			"/home/alice/domains/site.example-old/public_html", "/home/bob/domains/site.example-old/public_html",
			"/home/bob/domains/site.example-old/public_html"},
		{"/home/alice/public_html/site.example",
			"/home/alice/public_html/site.example", "/home/bob/public_html/site.example",
			"/home/bob/public_html/site.example"},
		{"/home/alice/custom/www",
			"/home/alice/custom/www", "/home/bob/custom/www",
			"/home/bob/custom/www"},
	} {
		d, cd, domain, newOwner, doms, ag := chownFixture()
		domain.DocRoot = tc.docRoot
		doms.owned[0].DocRoot = tc.docRoot
		if err := ChangeDomainOwner(context.Background(), d, cd, domain, newOwner); err != nil {
			t.Fatalf("%s: chown: %v", tc.docRoot, err)
		}
		if ag.lastParams["old_doc_root"] != tc.from || ag.lastParams["new_doc_root"] != tc.to {
			t.Errorf("%s: moved %v -> %v, want %s -> %s", tc.docRoot, ag.lastParams["old_doc_root"], ag.lastParams["new_doc_root"], tc.from, tc.to)
		}
		if got, set := ag.lastParams["prune_empty_dir"]; set {
			t.Errorf("%s: prune_empty_dir = %v, want none", tc.docRoot, got)
		}
		if doms.newDocRoot != tc.newDocRoot || domain.DocRoot != tc.newDocRoot {
			t.Errorf("%s: new docroot = %q (row) / %q (domain), want %q", tc.docRoot, doms.newDocRoot, domain.DocRoot, tc.newDocRoot)
		}
	}
}

// An FTP/SFTP subaccount homed outside the tree that moves doesn't stop it:
// its home stays where it is.
func TestChangeDomainOwner_FtpAccountOutsideTheFolderIsFine(t *testing.T) {
	d, cd, domain, newOwner, doms, _ := chownFixture()
	d.FtpAccounts = &chownFtp{accts: []models.FtpAccount{
		{Username: "alice_all", HomePath: "/home/alice"},
		{Username: "alice_domains", HomePath: "/home/alice/domains"},
		{Username: "alice_other", HomePath: "/home/alice/domains/site.example-old"},
	}}
	if err := ChangeDomainOwner(context.Background(), d, cd, domain, newOwner); err != nil {
		t.Fatalf("chown: %v", err)
	}
	if !doms.transferCalled {
		t.Errorf("owner not changed")
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
		// GH #2541: the folder that moves holds another domain's site.
		{"another domain's site inside the folder", func(d *Deps, _ *ChownDeps, _ *models.Domain, _ *models.User) {
			doms := d.Domains.(*chownDomains)
			doms.owned = append(doms.owned, models.Domain{ID: "d2", Name: "blog.example", UserID: "old", DocRoot: "/home/alice/domains/site.example/public_html/blog"})
		}, `"blog.example"`},
		{"listing the owner's domains fails", func(d *Deps, _ *ChownDeps, _ *models.Domain, _ *models.User) {
			d.Domains.(*chownDomains).listErr = errors.New("db down")
		}, "could not list"},
		// GH #2541: an FTP/SFTP subaccount homed in the folder that moves.
		{"ftp account homed in the folder", func(d *Deps, _ *ChownDeps, _ *models.Domain, _ *models.User) {
			d.FtpAccounts = &chownFtp{accts: []models.FtpAccount{{Username: "alice_site", HomePath: "/home/alice/domains/site.example"}}}
		}, "alice_site"},
		{"ftp account homed under the docroot", func(d *Deps, _ *ChownDeps, _ *models.Domain, _ *models.User) {
			d.FtpAccounts = &chownFtp{accts: []models.FtpAccount{{Username: "alice_up", HomePath: "/home/alice/domains/site.example/public_html/uploads"}}}
		}, "alice_up"},
		{"listing ftp accounts fails", func(d *Deps, _ *ChownDeps, _ *models.Domain, _ *models.User) {
			d.FtpAccounts = &chownFtp{err: errors.New("db down")}
		}, "FTP subaccounts"},
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
