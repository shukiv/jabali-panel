package appsecops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// scriptedAgent answers call i with errs[i]; calls past the end succeed. It
// records the params of every call, failed ones included, so a test can see
// what each apply would have written.
type scriptedAgent struct {
	mu    sync.Mutex
	errs  []error
	calls []appseccfg.OperatorApplyParams
}

func (a *scriptedAgent) Call(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd != appseccfg.OperatorApplyVerb {
		return nil, errors.New("unexpected verb " + cmd)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(params)
	var p appseccfg.OperatorApplyParams
	_ = json.Unmarshal(raw, &p)
	a.mu.Lock()
	defer a.mu.Unlock()
	i := len(a.calls)
	a.calls = append(a.calls, p)
	if i < len(a.errs) && a.errs[i] != nil {
		return nil, a.errs[i]
	}
	return json.RawMessage(`{"changed":true,"reloaded":true}`), nil
}

func (a *scriptedAgent) sent() []appseccfg.OperatorApplyParams {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]appseccfg.OperatorApplyParams(nil), a.calls...)
}

// failingWrites is a fakeExcl whose Create or DeleteByID fails on demand, for
// the undo paths.
type failingWrites struct {
	*fakeExcl
	createErr error
	deleteErr error
}

func (f *failingWrites) Create(ctx context.Context, e *models.CRSRuleExclusion) error {
	if f.createErr != nil {
		return f.createErr
	}
	return f.fakeExcl.Create(ctx, e)
}

func (f *failingWrites) DeleteByID(ctx context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.fakeExcl.DeleteByID(ctx, id)
}

func exclEnv(errs ...error) (*env, *scriptedAgent) {
	e := newEnv()
	ag := &scriptedAgent{errs: errs}
	e.deps.Agent = ag
	return e, ag
}

// reloadFailed is how the agent reports a file it wrote but could not load.
var reloadFailed = &agent.AgentError{Code: agent.CodeInternal, Message: "crowdsec reload and restart failed"}

var blogWP = appseccfg.Exclusion{Host: "blog.example.com", URIPrefix: "/wp-json/x/", RuleID: "942100", Note: "webhook"}

func TestAddExclusion_StoresNormalisedAndApplies(t *testing.T) {
	e, ag := exclEnv()
	v, res, err := AddExclusion(context.Background(), e.deps, appseccfg.Exclusion{
		Host: "  Blog.Example.COM ", URIPrefix: " /wp-json/x/ ", RuleID: " 942100 ", Note: " webhook ",
	})
	if err != nil {
		t.Fatalf("AddExclusion: %v", err)
	}
	if !res.Changed || !res.Reloaded {
		t.Errorf("result = %+v, want the agent's changed+reloaded", res)
	}
	if len(e.excl.rows) != 1 {
		t.Fatalf("stored %d rows, want 1", len(e.excl.rows))
	}
	got := e.excl.rows[0]
	if got.Host != "blog.example.com" || got.URIPrefix != "/wp-json/x/" || got.RuleID != "942100" || got.Note != "webhook" {
		t.Errorf("stored %+v, want trimmed fields and a lowercase host", got)
	}
	if got.ID == "" || v.ID != got.ID {
		t.Errorf("returned id %q, stored id %q", v.ID, got.ID)
	}
	sent := ag.sent()
	if len(sent) != 1 || !equal(hosts(sent[0]), []string{"blog.example.com/wp-json/x/#942100"}) {
		t.Errorf("applied %+v, want the new row", sent)
	}
	if len(sent[0].HostModes) != 1 {
		t.Errorf("apply dropped the host modes: %+v", sent[0].HostModes)
	}
}

func TestAddExclusion_RefusesInvalidBeforeStoring(t *testing.T) {
	cases := map[string]appseccfg.Exclusion{
		"no path":           {Host: "blog.example.com", RuleID: "942100"},
		"no host":           {URIPrefix: "/x/", RuleID: "942100"},
		"anomaly blocker":   {Host: "blog.example.com", URIPrefix: "/x/", RuleID: "949110"},
		"quote in path":     {Host: "blog.example.com", URIPrefix: `/x"/`, RuleID: "942100"},
		"newline in note":   {Host: "blog.example.com", URIPrefix: "/x/", RuleID: "942100", Note: "a\nSecRule"},
		"port in host":      {Host: "blog.example.com:8443", URIPrefix: "/x/", RuleID: "942100"},
		"reserved range id": {Host: "blog.example.com", URIPrefix: "/x/", RuleID: "9597001"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			e, ag := exclEnv()
			_, _, err := AddExclusion(context.Background(), e.deps, in)
			var inv *InvalidError
			if !errors.As(err, &inv) {
				t.Fatalf("err = %v, want *InvalidError", err)
			}
			if len(e.excl.rows) != 0 || len(ag.sent()) != 0 {
				t.Errorf("stored %d rows and sent %d applies for an invalid exclusion", len(e.excl.rows), len(ag.sent()))
			}
		})
	}
}

func TestAddExclusion_RefusesDuplicate(t *testing.T) {
	e, ag := exclEnv()
	e.excl.rows = []models.CRSRuleExclusion{{ID: "a", Host: "blog.example.com", URIPrefix: "/wp-json/x/", RuleID: "942100"}}
	_, _, err := AddExclusion(context.Background(), e.deps, appseccfg.Exclusion{
		Host: "BLOG.example.com", URIPrefix: "/wp-json/x/", RuleID: "942100", Note: "again",
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
	if len(e.excl.rows) != 1 || len(ag.sent()) != 0 {
		t.Errorf("rows = %d, applies = %d; a duplicate must change nothing", len(e.excl.rows), len(ag.sent()))
	}
}

func TestAddExclusion_RefusesPastRenderCap(t *testing.T) {
	e, ag := exclEnv()
	for i := 0; i < appseccfg.MaxOperatorExclusions; i++ {
		e.excl.rows = append(e.excl.rows, models.CRSRuleExclusion{
			ID: fmt.Sprint(i), Host: "h.example.com", URIPrefix: fmt.Sprintf("/p%d/", i), RuleID: "942100",
		})
	}
	_, _, err := AddExclusion(context.Background(), e.deps, blogWP)
	if !errors.Is(err, ErrTooMany) {
		t.Fatalf("err = %v, want ErrTooMany", err)
	}
	if len(e.excl.rows) != appseccfg.MaxOperatorExclusions || len(ag.sent()) != 0 {
		t.Errorf("a refused add stored a row or applied")
	}
}

// The agent writes the file, then fails the reload. The row must go, and the
// file must be written again without it, or the exclusion the operator saw
// fail goes live at the next reload.
func TestAddExclusion_ApplyFailureRemovesRowAndRewritesFile(t *testing.T) {
	e, ag := exclEnv(reloadFailed)
	_, _, err := AddExclusion(context.Background(), e.deps, blogWP)

	var ae *ApplyError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *ApplyError", err)
	}
	if !ae.RolledBack || ae.ReapplyErr != nil {
		t.Errorf("ApplyError = %+v, want rolled back and re-applied", ae)
	}
	var agErr *agent.AgentError
	if !errors.As(err, &agErr) || agErr.Code != agent.CodeInternal {
		t.Errorf("the agent error is not reachable through the ApplyError: %v", err)
	}
	if len(e.excl.rows) != 0 {
		t.Errorf("rows after a failed add = %+v, want none", e.excl.rows)
	}
	sent := ag.sent()
	if len(sent) != 2 {
		t.Fatalf("applies = %d, want the failed one and the rewrite", len(sent))
	}
	if len(hosts(sent[0])) != 1 || len(hosts(sent[1])) != 0 {
		t.Errorf("applies = %v then %v, want the row then none", hosts(sent[0]), hosts(sent[1]))
	}
}

func TestAddExclusion_ReportsFailedRewrite(t *testing.T) {
	e, _ := exclEnv(reloadFailed, reloadFailed)
	_, _, err := AddExclusion(context.Background(), e.deps, blogWP)
	var ae *ApplyError
	if !errors.As(err, &ae) || !ae.RolledBack || ae.ReapplyErr == nil {
		t.Fatalf("err = %#v, want rolled back with a ReapplyErr", err)
	}
	if len(e.excl.rows) != 0 {
		t.Errorf("rows = %+v, want none", e.excl.rows)
	}
}

func TestAddExclusion_ReportsFailedRollback(t *testing.T) {
	e, _ := exclEnv(reloadFailed)
	store := &failingWrites{fakeExcl: e.excl, deleteErr: errors.New("db gone")}
	e.deps.Exclusions = store
	_, _, err := AddExclusion(context.Background(), e.deps, blogWP)
	var ae *ApplyError
	if !errors.As(err, &ae) || ae.RolledBack || ae.RollbackErr == nil {
		t.Fatalf("err = %#v, want not rolled back, with a RollbackErr", err)
	}
}

// A client that goes away mid-request must not abandon the apply: the agent
// finishes the write anyway, and the panel would no longer know.
func TestAddExclusion_AppliesAfterClientIsGone(t *testing.T) {
	e, ag := exclEnv()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := AddExclusion(ctx, e.deps, blogWP); err != nil {
		t.Fatalf("AddExclusion on a cancelled request: %v", err)
	}
	if len(ag.sent()) != 1 || len(e.excl.rows) != 1 {
		t.Errorf("applies = %d, rows = %d; want 1 and 1", len(ag.sent()), len(e.excl.rows))
	}
}

func TestAddExclusion_NotConfigured(t *testing.T) {
	e, _ := exclEnv()
	e.deps.Agent = nil
	if _, _, err := AddExclusion(context.Background(), e.deps, blogWP); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func seeded(e *env) models.CRSRuleExclusion {
	row := models.CRSRuleExclusion{
		ID: "01HX", Host: "blog.example.com", URIPrefix: "/wp-json/x/", RuleID: "942100", Note: "webhook",
		CreatedAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
	}
	e.excl.rows = []models.CRSRuleExclusion{row}
	return row
}

func TestRemoveExclusion_DeletesAndApplies(t *testing.T) {
	e, ag := exclEnv()
	seeded(e)
	if _, err := RemoveExclusion(context.Background(), e.deps, "01HX"); err != nil {
		t.Fatalf("RemoveExclusion: %v", err)
	}
	if len(e.excl.rows) != 0 {
		t.Errorf("rows = %+v, want none", e.excl.rows)
	}
	if sent := ag.sent(); len(sent) != 1 || len(sent[0].Exclusions) != 0 {
		t.Errorf("applies = %+v, want one without the row", sent)
	}
}

func TestRemoveExclusion_UnknownID(t *testing.T) {
	e, ag := exclEnv()
	seeded(e)
	if _, err := RemoveExclusion(context.Background(), e.deps, "nope"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(e.excl.rows) != 1 || len(ag.sent()) != 0 {
		t.Errorf("an unknown id changed something")
	}
}

// A remove whose apply fails puts the row back as it was, so the list still
// shows the exclusion the WAF may be running, and the operator can retry.
func TestRemoveExclusion_ApplyFailureRestoresRow(t *testing.T) {
	e, ag := exclEnv(reloadFailed)
	want := seeded(e)
	_, err := RemoveExclusion(context.Background(), e.deps, "01HX")
	var ae *ApplyError
	if !errors.As(err, &ae) || !ae.RolledBack || ae.ReapplyErr != nil {
		t.Fatalf("err = %#v, want rolled back and re-applied", err)
	}
	if len(e.excl.rows) != 1 || e.excl.rows[0] != want {
		t.Errorf("rows = %+v, want the original row back, same id and created_at", e.excl.rows)
	}
	sent := ag.sent()
	if len(sent) != 2 || len(sent[0].Exclusions) != 0 || len(sent[1].Exclusions) != 1 {
		t.Errorf("applies = %+v, want without then with the row", sent)
	}
}

func TestRemoveExclusion_ReportsFailedRollback(t *testing.T) {
	e, _ := exclEnv(reloadFailed)
	seeded(e)
	e.deps.Exclusions = &failingWrites{fakeExcl: e.excl, createErr: errors.New("db gone")}
	_, err := RemoveExclusion(context.Background(), e.deps, "01HX")
	var ae *ApplyError
	if !errors.As(err, &ae) || ae.RolledBack || ae.RollbackErr == nil {
		t.Fatalf("err = %#v, want not rolled back, with a RollbackErr", err)
	}
}

func TestListExclusions_MarksManagedFlarumRows(t *testing.T) {
	e, _ := exclEnv()
	flarum, err := FlarumExclusion("inst1", "forum.example.com", false, "")
	if err != nil {
		t.Fatal(err)
	}
	e.excl.rows = []models.CRSRuleExclusion{
		{ID: "a", Host: flarum.Host, URIPrefix: flarum.URIPrefix, RuleID: flarum.RuleID, Note: flarum.Note},
		{ID: "b", Host: "blog.example.com", URIPrefix: "/x/", RuleID: "942100", Note: "by hand"},
	}
	got, err := ListExclusions(context.Background(), e.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ManagedInstallID != "inst1" || got[1].ManagedInstallID != "" {
		t.Errorf("managed ids = %q, %q; want inst1 and none", got[0].ManagedInstallID, got[1].ManagedInstallID)
	}
}
