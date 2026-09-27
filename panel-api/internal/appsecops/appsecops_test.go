package appsecops

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ---- fakes ----

type fakeExcl struct {
	mu      sync.Mutex
	rows    []models.CRSRuleExclusion
	listErr error
	created chan string // receives each created row's host, when set
}

func (f *fakeExcl) List(context.Context) ([]models.CRSRuleExclusion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]models.CRSRuleExclusion(nil), f.rows...), nil
}

func (f *fakeExcl) Create(_ context.Context, e *models.CRSRuleExclusion) error {
	f.mu.Lock()
	f.rows = append(f.rows, *e)
	f.mu.Unlock()
	if f.created != nil {
		f.created <- e.Host
	}
	return nil
}

func (f *fakeExcl) DeleteByID(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, r := range f.rows {
		if r.ID == id {
			f.rows = append(f.rows[:i], f.rows[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

type fakeModes struct {
	rows []models.CRSHostMode
	err  error
}

func (f *fakeModes) List(context.Context) ([]models.CRSHostMode, error) { return f.rows, f.err }

type fakeInstalls struct {
	mu   sync.Mutex
	byID map[string]*models.ApplicationInstall
	err  error
}

func (f *fakeInstalls) FindByID(_ context.Context, id string) (*models.ApplicationInstall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if in, ok := f.byID[id]; ok {
		return in, nil
	}
	return nil, repository.ErrNotFound
}

type fakeDomains map[string]*models.Domain

func (f fakeDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if d, ok := f[id]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

// fakeAgent records, in completion order, the params of every apply call.
// hold, when set, makes the FIRST call wait until it is closed.
type fakeAgent struct {
	mu      sync.Mutex
	applied []appseccfg.OperatorApplyParams
	err     error
	entered chan struct{}
	hold    chan struct{}
	calls   int
}

func (a *fakeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd != appseccfg.OperatorApplyVerb {
		return nil, errors.New("unexpected verb " + cmd)
	}
	a.mu.Lock()
	a.calls++
	first := a.calls == 1
	a.mu.Unlock()
	if first && a.hold != nil {
		close(a.entered)
		<-a.hold
	}
	if a.err != nil {
		return nil, a.err
	}
	raw, _ := json.Marshal(params)
	var p appseccfg.OperatorApplyParams
	_ = json.Unmarshal(raw, &p)
	a.mu.Lock()
	a.applied = append(a.applied, p)
	a.mu.Unlock()
	return json.RawMessage(`{"changed":true,"reloaded":true}`), nil
}

func (a *fakeAgent) last(t *testing.T) appseccfg.OperatorApplyParams {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.applied) == 0 {
		t.Fatal("nothing was applied")
	}
	return a.applied[len(a.applied)-1]
}

type env struct {
	excl     *fakeExcl
	installs *fakeInstalls
	agent    *fakeAgent
	deps     Deps
}

func newEnv() *env {
	e := &env{
		excl:     &fakeExcl{},
		installs: &fakeInstalls{byID: map[string]*models.ApplicationInstall{}},
		agent:    &fakeAgent{},
	}
	e.deps = Deps{
		Agent:      e.agent,
		Exclusions: e.excl,
		HostModes:  &fakeModes{rows: []models.CRSHostMode{{Host: "code.example.com", Mode: appseccfg.HostModeDetect}}},
		Installs:   e.installs,
		Domains: fakeDomains{
			"d1": {ID: "d1", Name: "Forum.Example.com"},
			"d2": {ID: "d2", Name: "other.example.org"},
		},
	}
	return e
}

func (e *env) addFlarum(id, domainID, sub string, www bool) {
	e.installs.mu.Lock()
	defer e.installs.mu.Unlock()
	e.installs.byID[id] = &models.ApplicationInstall{ID: id, DomainID: domainID, AppType: AppTypeFlarum, Subdirectory: sub, UseWWW: www}
}

func (e *env) dropInstall(id string) {
	e.installs.mu.Lock()
	defer e.installs.mu.Unlock()
	delete(e.installs.byID, id)
}

func hosts(p appseccfg.OperatorApplyParams) []string {
	var out []string
	for _, x := range p.Exclusions {
		out = append(out, x.Host+x.URIPrefix+"#"+x.RuleID)
	}
	sort.Strings(out)
	return out
}

func equal(a, b []string) bool {
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

// ---- Apply ----

// Apply sends both tables, complete, in one call.
func TestApply_SendsBothTables(t *testing.T) {
	e := newEnv()
	e.excl.rows = []models.CRSRuleExclusion{{ID: "x", Host: "a.example.com", URIPrefix: "/p/", RuleID: "942100", Note: "op"}}
	res, err := Apply(context.Background(), e.deps)
	if err != nil || !res.Changed || !res.Reloaded {
		t.Fatalf("got %+v, %v", res, err)
	}
	p := e.agent.last(t)
	if len(p.Exclusions) != 1 || p.Exclusions[0].Host != "a.example.com" || len(p.HostModes) != 1 {
		t.Fatalf("unexpected params %+v", p)
	}
}

// If either table cannot be read, nothing is sent: a partial desired state would
// remove the other table's live entries.
func TestApply_ReadFailureSendsNothing(t *testing.T) {
	for name, mut := range map[string]func(*env){
		"exclusions": func(e *env) { e.excl.listErr = errors.New("db down") },
		"host modes": func(e *env) { e.deps.HostModes = &fakeModes{err: errors.New("db down")} },
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv()
			mut(e)
			if _, err := Apply(context.Background(), e.deps); err == nil {
				t.Fatal("want an error")
			}
			if e.agent.calls != 0 {
				t.Fatalf("agent was called %d times after a failed read", e.agent.calls)
			}
		})
	}
}

// ---- FlarumExclusion ----

func TestFlarumExclusion_HostAndPath(t *testing.T) {
	for _, tc := range []struct {
		name, domain, sub string
		www               bool
		host, prefix      string
	}{
		{"docroot", "Forum.Example.com", "", false, "forum.example.com", "/api/"},
		{"subdirectory", "example.com", "forum", false, "example.com", "/forum/api/"},
		{"www", "example.com", "community", true, "www.example.com", "/community/api/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x, err := FlarumExclusion("01ABC", tc.domain, tc.www, tc.sub)
			if err != nil {
				t.Fatal(err)
			}
			if x.Host != tc.host || x.URIPrefix != tc.prefix || x.RuleID != "920450" {
				t.Fatalf("got %+v", x)
			}
			if managedFlarumInstallID(models.CRSRuleExclusion{RuleID: x.RuleID, Note: x.Note}) != "01ABC" {
				t.Fatalf("note does not round-trip the install id: %q", x.Note)
			}
		})
	}
}

// ---- SyncFlarum ----

func TestSyncFlarum_AddsScopedExclusionAndApplies(t *testing.T) {
	e := newEnv()
	e.addFlarum("i1", "d1", "forum", false)
	if _, err := SyncFlarum(context.Background(), e.deps, "i1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"forum.example.com/forum/api/#920450"}
	if got := hosts(e.agent.last(t)); !equal(got, want) {
		t.Fatalf("applied %v, want %v", got, want)
	}

	// Again: no duplicate row.
	if _, err := SyncFlarum(context.Background(), e.deps, "i1"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.excl.rows); n != 1 {
		t.Fatalf("want 1 row after a repeat sync, got %d", n)
	}
}

// An operator's own identical exclusion (any note) already covers it: no second
// row, and the operator's row survives the install's delete.
func TestSyncFlarum_RespectsOperatorRow(t *testing.T) {
	e := newEnv()
	e.excl.rows = []models.CRSRuleExclusion{{ID: "op", Host: "forum.example.com", URIPrefix: "/api/", RuleID: "920450", Note: "added by hand"}}
	e.addFlarum("i1", "d1", "", false)
	if _, err := SyncFlarum(context.Background(), e.deps, "i1"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.excl.rows); n != 1 {
		t.Fatalf("want only the operator's row, got %d rows", n)
	}
	e.dropInstall("i1")
	if _, err := SyncFlarum(context.Background(), e.deps, ""); err != nil {
		t.Fatal(err)
	}
	if len(e.excl.rows) != 1 || e.excl.rows[0].ID != "op" {
		t.Fatalf("operator's row was removed: %+v", e.excl.rows)
	}
}

// Once its install is gone — app delete, or a domain/account delete cascading
// the install row away — the managed row is removed and the removal applied.
// Other rows stay.
func TestSyncFlarum_RemovesRowOfGoneInstall(t *testing.T) {
	e := newEnv()
	e.excl.rows = []models.CRSRuleExclusion{{ID: "op", Host: "shop.example.com", URIPrefix: "/wp-json/x/", RuleID: "931120", Note: "op"}}
	e.addFlarum("i1", "d1", "forum", false)
	e.addFlarum("i2", "d2", "", true)
	for _, id := range []string{"i1", "i2"} {
		if _, err := SyncFlarum(context.Background(), e.deps, id); err != nil {
			t.Fatal(err)
		}
	}
	e.dropInstall("i1")
	if _, err := SyncFlarum(context.Background(), e.deps, ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"shop.example.com/wp-json/x/#931120", "www.other.example.org/api/#920450"}
	if got := hosts(e.agent.last(t)); !equal(got, want) {
		t.Fatalf("applied %v, want %v", got, want)
	}
}

// A managed row whose install now lives at another host or path is stale too.
func TestSyncFlarum_RemovesRowThatNoLongerMatches(t *testing.T) {
	e := newEnv()
	e.addFlarum("i1", "d1", "forum", false)
	if _, err := SyncFlarum(context.Background(), e.deps, "i1"); err != nil {
		t.Fatal(err)
	}
	e.addFlarum("i1", "d1", "board", false) // same install, other path
	if _, err := SyncFlarum(context.Background(), e.deps, ""); err != nil {
		t.Fatal(err)
	}
	if got := hosts(e.agent.last(t)); len(got) != 0 {
		t.Fatalf("stale row still applied: %v", got)
	}
}

func TestSyncFlarum_IgnoresOtherApps(t *testing.T) {
	e := newEnv()
	e.installs.byID["wp"] = &models.ApplicationInstall{ID: "wp", DomainID: "d1", AppType: "wordpress"}
	if _, err := SyncFlarum(context.Background(), e.deps, "wp"); err != nil {
		t.Fatal(err)
	}
	if len(e.excl.rows) != 0 {
		t.Fatalf("a non-Flarum install got an exclusion: %+v", e.excl.rows)
	}
}

// If an install lookup fails, it is unknown whether the install is gone: keep
// the row and apply nothing.
func TestSyncFlarum_LookupFailureRemovesNothing(t *testing.T) {
	e := newEnv()
	e.addFlarum("i1", "d1", "", false)
	if _, err := SyncFlarum(context.Background(), e.deps, "i1"); err != nil {
		t.Fatal(err)
	}
	calls := e.agent.calls
	e.installs.err = errors.New("db down")
	if _, err := SyncFlarum(context.Background(), e.deps, ""); err == nil {
		t.Fatal("want an error")
	}
	if len(e.excl.rows) != 1 || e.agent.calls != calls {
		t.Fatalf("changed state on a failed lookup: rows=%d calls=%d", len(e.excl.rows), e.agent.calls)
	}
}

// ---- PruneFlarum ----

func TestPruneFlarum_OnlyRemovesAndOnlyAppliesOnChange(t *testing.T) {
	e := newEnv()
	e.addFlarum("i1", "d1", "", false)
	e.addFlarum("i2", "d2", "", false)
	if _, err := SyncFlarum(context.Background(), e.deps, "i1"); err != nil {
		t.Fatal(err)
	}
	calls := e.agent.calls

	// i2 has no row; prune must not add one, and with nothing stale it must
	// not call the agent.
	changed, _, err := PruneFlarum(context.Background(), e.deps)
	if err != nil || changed || e.agent.calls != calls || len(e.excl.rows) != 1 {
		t.Fatalf("no-op prune: changed=%v err=%v calls=%d rows=%d", changed, err, e.agent.calls-calls, len(e.excl.rows))
	}

	e.dropInstall("i1")
	changed, _, err = PruneFlarum(context.Background(), e.deps)
	if err != nil || !changed || len(e.excl.rows) != 0 {
		t.Fatalf("stale prune: changed=%v err=%v rows=%d", changed, err, len(e.excl.rows))
	}
	if got := hosts(e.agent.last(t)); len(got) != 0 {
		t.Fatalf("removal not applied: %v", got)
	}
}

// ---- ordering ----

// Each apply carries the complete desired state, so a newer state must never be
// overwritten by an older one. Here the first apply is still in flight when a
// second install registers. Without serialisation the second apply lands first
// and the first, older one lands last — without the second forum's exclusion.
func TestSyncFlarum_OverlappingSyncsLandInOrder(t *testing.T) {
	e := newEnv()
	e.addFlarum("i1", "d1", "", false)
	e.addFlarum("i2", "d2", "", false)
	e.agent.entered = make(chan struct{})
	e.agent.hold = make(chan struct{})
	e.excl.created = make(chan string, 4)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = SyncFlarum(context.Background(), e.deps, "i1") }()
	<-e.excl.created
	<-e.agent.entered // first apply is in flight
	go func() { defer wg.Done(); _, _ = SyncFlarum(context.Background(), e.deps, "i2") }()
	select {
	case <-e.excl.created: // unserialised: the second sync ran while the first was in flight
	case <-time.After(300 * time.Millisecond): // serialised: it is waiting
	}
	close(e.agent.hold)
	wg.Wait()

	want := []string{"forum.example.com/api/#920450", "other.example.org/api/#920450"}
	if got := hosts(e.agent.last(t)); !equal(got, want) {
		t.Fatalf("the last apply to land is stale: %v, want %v", got, want)
	}
}

// Apply on its own (the path an admin UI takes) is serialised the same way: an
// apply that read an older state must not land after one that read a newer one.
func TestApply_OverlappingAppliesLandInOrder(t *testing.T) {
	e := newEnv()
	e.excl.rows = []models.CRSRuleExclusion{{ID: "a", Host: "a.example.com", URIPrefix: "/a/", RuleID: "942100"}}
	e.agent.entered = make(chan struct{})
	e.agent.hold = make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = Apply(context.Background(), e.deps) }()
	<-e.agent.entered // the first apply, holding the old state, is in flight
	e.excl.mu.Lock()
	e.excl.rows = append(e.excl.rows, models.CRSRuleExclusion{ID: "b", Host: "b.example.com", URIPrefix: "/b/", RuleID: "942100"})
	e.excl.mu.Unlock()
	go func() { defer wg.Done(); _, _ = Apply(context.Background(), e.deps) }()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) { // unserialised: the second apply lands meanwhile
		e.agent.mu.Lock()
		n := len(e.agent.applied)
		e.agent.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(e.agent.hold)
	wg.Wait()

	want := []string{"a.example.com/a/#942100", "b.example.com/b/#942100"}
	if got := hosts(e.agent.last(t)); !equal(got, want) {
		t.Fatalf("the last apply to land is stale: %v, want %v", got, want)
	}
}
