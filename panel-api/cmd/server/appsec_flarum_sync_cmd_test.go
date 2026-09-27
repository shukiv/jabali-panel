package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/appsecops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fsExcl struct{ rows []models.CRSRuleExclusion }

func (s *fsExcl) List(context.Context) ([]models.CRSRuleExclusion, error) { return s.rows, nil }
func (s *fsExcl) Create(_ context.Context, e *models.CRSRuleExclusion) error {
	s.rows = append(s.rows, *e)
	return nil
}
func (s *fsExcl) DeleteByID(context.Context, string) error { return repository.ErrNotFound }

type fsModes struct{}

func (fsModes) List(context.Context) ([]models.CRSHostMode, error) { return nil, nil }

type fsInstalls map[string]*models.ApplicationInstall

func (f fsInstalls) FindByID(_ context.Context, id string) (*models.ApplicationInstall, error) {
	if in, ok := f[id]; ok {
		return in, nil
	}
	return nil, repository.ErrNotFound
}

type fsDomains struct{}

func (fsDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	return &models.Domain{ID: id, Name: id + ".example.com"}, nil
}

type fsAgent struct{ calls int }

func (a *fsAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	if cmd != appseccfg.OperatorApplyVerb {
		return nil, errors.New("unexpected verb " + cmd)
	}
	a.calls++
	return json.RawMessage(`{"changed":true,"reloaded":true}`), nil
}

func fsDeps(ag *fsAgent, excl *fsExcl) appsecops.Deps {
	return appsecops.Deps{
		Agent: ag, Exclusions: excl, HostModes: fsModes{}, Domains: fsDomains{},
		Installs: fsInstalls{
			"i1": {ID: "i1", DomainID: "a", AppType: "flarum", Subdirectory: "forum"},
			"i2": {ID: "i2", DomainID: "b", AppType: "flarum"},
		},
	}
}

// Every ready Flarum install gets its exclusion, with one apply for the run,
// and the report names what was added.
func TestRunFlarumSync_AddsForEveryInstall(t *testing.T) {
	ag, excl := &fsAgent{}, &fsExcl{}
	var gotType string
	rep, err := runFlarumSync(context.Background(), func(_ context.Context, appType string) ([]string, error) {
		gotType = appType
		return []string{"i1", "i2"}, nil
	}, fsDeps(ag, excl))
	if err != nil {
		t.Fatal(err)
	}
	if gotType != "flarum" {
		t.Fatalf("listed app type %q, want flarum", gotType)
	}
	if rep.Installs != 2 || len(rep.Added) != 2 || ag.calls != 1 || !rep.Result.Reloaded {
		t.Fatalf("report %+v, agent calls %d", rep, ag.calls)
	}
	var out bytes.Buffer
	printFlarumSyncReport(&out, rep)
	for _, want := range []string{"a.example.com/forum/api/", "b.example.com/api/", "2 exclusion(s) added", "crowdsec reloaded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// Re-run: already covered, nothing added.
	rep, err = runFlarumSync(context.Background(), func(context.Context, string) ([]string, error) {
		return []string{"i1", "i2"}, nil
	}, fsDeps(ag, excl))
	if err != nil || len(rep.Added) != 0 || len(excl.rows) != 2 {
		t.Fatalf("re-run: report %+v rows %d err %v", rep, len(excl.rows), err)
	}
}

// No Flarum on the box: nothing is written and crowdsec is not touched.
func TestRunFlarumSync_NoInstallsTouchesNothing(t *testing.T) {
	ag, excl := &fsAgent{}, &fsExcl{}
	rep, err := runFlarumSync(context.Background(), func(context.Context, string) ([]string, error) {
		return nil, nil
	}, fsDeps(ag, excl))
	if err != nil || rep.Installs != 0 || ag.calls != 0 || len(excl.rows) != 0 {
		t.Fatalf("report %+v calls %d rows %d err %v", rep, ag.calls, len(excl.rows), err)
	}
}

func TestRunFlarumSync_ListErrorAppliesNothing(t *testing.T) {
	ag := &fsAgent{}
	_, err := runFlarumSync(context.Background(), func(context.Context, string) ([]string, error) {
		return nil, errors.New("db down")
	}, fsDeps(ag, &fsExcl{}))
	if err == nil || ag.calls != 0 {
		t.Fatalf("want an error and no apply, got err=%v calls=%d", err, ag.calls)
	}
}
