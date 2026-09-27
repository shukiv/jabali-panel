package api

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/appsecops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1650 wiring: the Flarum install kicker and RunAppDelete drive the scoped
// CRS 920450 exclusion. The fakes share one event log so ordering is visible.

type wafLog struct {
	mu     sync.Mutex
	events []string
}

func (l *wafLog) add(e string) { l.mu.Lock(); l.events = append(l.events, e); l.mu.Unlock() }

type wafExclStore struct {
	rows []models.CRSRuleExclusion
}

func (s *wafExclStore) List(context.Context) ([]models.CRSRuleExclusion, error) {
	return append([]models.CRSRuleExclusion(nil), s.rows...), nil
}
func (s *wafExclStore) Create(_ context.Context, e *models.CRSRuleExclusion) error {
	s.rows = append(s.rows, *e)
	return nil
}
func (s *wafExclStore) DeleteByID(_ context.Context, id string) error {
	for i, r := range s.rows {
		if r.ID == id {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

type wafModes struct{ repository.CRSHostModeRepository }

func (wafModes) List(context.Context) ([]models.CRSHostMode, error) { return nil, nil }

type wafInstalls struct {
	repository.ApplicationInstallRepository
	log  *wafLog
	byID map[string]*models.ApplicationInstall
}

func (f *wafInstalls) FindByID(_ context.Context, id string) (*models.ApplicationInstall, error) {
	if in, ok := f.byID[id]; ok {
		return in, nil
	}
	return nil, repository.ErrNotFound
}
func (f *wafInstalls) UpdateStatus(_ context.Context, _, status string, _, _ *string) error {
	f.log.add("status:" + status)
	return nil
}
func (f *wafInstalls) Delete(_ context.Context, id string) error {
	delete(f.byID, id)
	return nil
}

type wafDomains struct{ repository.DomainRepository }

func (wafDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if id == "dom1" {
		return &models.Domain{ID: "dom1", Name: "example.com"}, nil
	}
	return nil, repository.ErrNotFound
}

type wafAgent struct {
	log     *wafLog
	applied []appseccfg.OperatorApplyParams
}

func (a *wafAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.log.add("agent:" + cmd)
	if cmd == appseccfg.OperatorApplyVerb {
		raw, _ := json.Marshal(params)
		var p appseccfg.OperatorApplyParams
		_ = json.Unmarshal(raw, &p)
		a.applied = append(a.applied, p)
		return json.RawMessage(`{"changed":true,"reloaded":true}`), nil
	}
	return json.RawMessage(`{"version":"1.8.9"}`), nil
}

func appliedKeys(p appseccfg.OperatorApplyParams) []string {
	var out []string
	for _, x := range p.Exclusions {
		out = append(out, x.Host+x.URIPrefix+"#"+x.RuleID)
	}
	sort.Strings(out)
	return out
}

func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A finished Flarum install registers its forum's exclusion and applies it
// before the row flips to ready, so a CLI --wait caller cannot exit mid-apply.
func TestFlarumKicker_RegistersExclusionBeforeReady(t *testing.T) {
	log := &wafLog{}
	excl := &wafExclStore{}
	installs := &wafInstalls{log: log, byID: map[string]*models.ApplicationInstall{
		"inst1": {ID: "inst1", DomainID: "dom1", AppType: "flarum", Subdirectory: "forum", UseWWW: true},
	}}
	ag := &wafAgent{log: log}
	createFlarumInstallAndKickAgent(context.Background(), flarumKickArgs{
		InstallID: "inst1", OSUser: "bob", DocRoot: "/home/bob/public_html", Subdirectory: "forum",
		SiteURL: "https://www.example.com/forum", UseWWW: true,
	}, ApplicationHandlerConfig{
		ApplicationInstalls: installs,
		Domains:             wafDomains{},
		Agent:               ag,
		CRSExclusions:       excl,
		CRSHostModes:        wafModes{},
	})

	if len(ag.applied) != 1 {
		t.Fatalf("want one apply, got %d (events %v)", len(ag.applied), log.events)
	}
	if got, want := appliedKeys(ag.applied[0]), []string{"www.example.com/forum/api/#920450"}; !sameKeys(got, want) {
		t.Fatalf("applied %v, want %v", got, want)
	}
	apply, ready := -1, -1
	for i, e := range log.events {
		switch e {
		case "agent:" + appseccfg.OperatorApplyVerb:
			apply = i
		case "status:ready":
			ready = i
		}
	}
	if apply < 0 || ready < 0 || apply > ready {
		t.Fatalf("the apply must land before the ready flip: %v", log.events)
	}
}

// Deleting a Flarum install removes its managed exclusion and applies that.
// An operator's own rows stay.
func TestRunAppDelete_FlarumRemovesItsExclusion(t *testing.T) {
	managed, err := appsecops.FlarumExclusion("inst1", "example.com", false, "")
	if err != nil {
		t.Fatal(err)
	}
	excl := &wafExclStore{rows: []models.CRSRuleExclusion{
		{ID: "m1", Host: managed.Host, URIPrefix: managed.URIPrefix, RuleID: managed.RuleID, Note: managed.Note},
		{ID: "op", Host: "shop.example.com", URIPrefix: "/wp-json/x/", RuleID: "931120", Note: "by hand"},
	}}
	log := &wafLog{}
	installs := &wafInstalls{log: log, byID: map[string]*models.ApplicationInstall{
		"inst1": {ID: "inst1", DomainID: "dom1", AppType: "flarum"},
	}}
	ag := &wafAgent{log: log}
	args := AppDeleteArgs{InstallID: "inst1", UserID: "u1", AppType: "flarum", OSUser: "bob",
		Docroot: "/home/bob/public_html", DomainName: "example.com"}
	if err := RunAppDelete(args, AppDeleteDeps{
		Installs: installs, Agent: ag, Domains: wafDomains{}, CRSExclusions: excl, CRSHostModes: wafModes{},
	}); err != nil {
		t.Fatal(err)
	}
	if len(excl.rows) != 1 || excl.rows[0].ID != "op" {
		t.Fatalf("want only the operator row left, got %+v", excl.rows)
	}
	if len(ag.applied) != 1 {
		t.Fatalf("want the removal applied once, got %d", len(ag.applied))
	}
	if got, want := appliedKeys(ag.applied[0]), []string{"shop.example.com/wp-json/x/#931120"}; !sameKeys(got, want) {
		t.Fatalf("applied %v, want %v", got, want)
	}
}

// Any other app's delete leaves the WAF alone: no sync, no crowdsec reload.
func TestRunAppDelete_OtherAppsLeaveWAFAlone(t *testing.T) {
	log := &wafLog{}
	installs := &wafInstalls{log: log, byID: map[string]*models.ApplicationInstall{
		"inst1": {ID: "inst1", DomainID: "dom1", AppType: "dokuwiki"},
	}}
	ag := &wafAgent{log: log}
	if err := RunAppDelete(AppDeleteArgs{InstallID: "inst1", UserID: "u1", AppType: "dokuwiki", OSUser: "bob",
		Docroot: "/home/bob/public_html", DomainName: "example.com"}, AppDeleteDeps{
		Installs: installs, Agent: ag, Domains: wafDomains{}, CRSExclusions: &wafExclStore{}, CRSHostModes: wafModes{},
	}); err != nil {
		t.Fatal(err)
	}
	if len(ag.applied) != 0 {
		t.Fatalf("a non-Flarum delete touched the WAF: %v", log.events)
	}
}
