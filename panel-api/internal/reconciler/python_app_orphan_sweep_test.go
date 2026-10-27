package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #2289: an app unit whose app row is gone kept running the deleted app on
// its loopback port, and the next app given that port could not start. The
// sweep removes such a unit once it has had no row for pythonAppOrphanGrace,
// and never a unit that has a row.

// sweepAgent answers app.python.list_units with units and records every call.
type sweepAgent struct {
	units   string // the list_units answer
	failCmd map[string]error
	calls   []fakeMTAStsCall
}

func (f *sweepAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	m, _ := params.(map[string]any)
	f.calls = append(f.calls, fakeMTAStsCall{Command: cmd, Params: m})
	if err, ok := f.failCmd[cmd]; ok {
		return nil, err
	}
	if cmd == "app.python.list_units" {
		return json.RawMessage(f.units), nil
	}
	return json.RawMessage(`{"removed":true}`), nil
}

func (f *sweepAgent) removed() []string {
	var ids []string
	for _, c := range f.calls {
		if c.Command == "app.python.remove" {
			ids = append(ids, c.Params["app_id"].(string))
		}
	}
	return ids
}

func sweepReconciler(ag *sweepAgent) *Reconciler {
	return &Reconciler{agent: ag, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// agePythonAppOrphans marks ids as found without a row longer than the grace.
func agePythonAppOrphans(r *Reconciler, ids ...string) {
	r.pythonAppOrphanMu.Lock()
	defer r.pythonAppOrphanMu.Unlock()
	if r.pythonAppOrphanSeen == nil {
		r.pythonAppOrphanSeen = map[string]*pythonAppOrphan{}
	}
	for _, id := range ids {
		r.pythonAppOrphanSeen[id] = &pythonAppOrphan{since: time.Now().Add(-pythonAppOrphanGrace - time.Minute)}
	}
}

func TestSweepPythonAppUnits_RemovesAUnitWithNoRowAfterTheGrace(t *testing.T) {
	ag := &sweepAgent{units: `{"app_ids":["01LIVE","01NODE","01GONE"]}`}
	r := sweepReconciler(ag)
	apps := []*models.PythonApp{
		{ID: "01LIVE", Runtime: "python", Status: models.PythonAppStatusFailed},
		{ID: "01NODE", Runtime: "node"},
	}

	r.sweepPythonAppUnits(context.Background(), apps)
	if got := ag.removed(); len(got) != 0 {
		t.Fatalf("removed %q on the first sight, want it kept for the grace", got)
	}

	agePythonAppOrphans(r, "01GONE")
	r.sweepPythonAppUnits(context.Background(), apps)
	if got := ag.removed(); !reflect.DeepEqual(got, []string{"01GONE"}) {
		t.Fatalf("removed %q, want only the unit with no row", got)
	}
	if _, ok := r.pythonAppOrphanSeen["01GONE"]; ok {
		t.Fatal("a removed unit is still recorded")
	}
}

// When the unit list can't be had, nothing is removed, not even a unit
// already past its grace.
func TestSweepPythonAppUnits_NoListRemovesNothing(t *testing.T) {
	for name, ag := range map[string]*sweepAgent{
		"call fails":  {failCmd: map[string]error{"app.python.list_units": errors.New("agent down")}},
		"unreadable":  {units: `not json`},
		"wrong shape": {units: `{"app_ids":"01GONE"}`},
	} {
		r := sweepReconciler(ag)
		agePythonAppOrphans(r, "01GONE")
		r.sweepPythonAppUnits(context.Background(), nil)
		if got := ag.removed(); len(got) != 0 {
			t.Fatalf("%s: removed %q", name, got)
		}
	}
}

// A failed removal is tried again on the next pass.
func TestSweepPythonAppUnits_RetriesAFailedRemoval(t *testing.T) {
	ag := &sweepAgent{units: `{"app_ids":["01GONE"]}`, failCmd: map[string]error{"app.python.remove": errors.New("still active")}}
	r := sweepReconciler(ag)
	agePythonAppOrphans(r, "01GONE")

	r.sweepPythonAppUnits(context.Background(), nil)
	delete(ag.failCmd, "app.python.remove")
	r.sweepPythonAppUnits(context.Background(), nil)

	if got := ag.removed(); !reflect.DeepEqual(got, []string{"01GONE", "01GONE"}) {
		t.Fatalf("removes = %q, want a retry after the failure", got)
	}
}

// A unit that has a row again starts its grace again.
func TestSweepPythonAppUnits_ARowRestartsTheGrace(t *testing.T) {
	ag := &sweepAgent{units: `{"app_ids":["01APP"]}`}
	r := sweepReconciler(ag)
	agePythonAppOrphans(r, "01APP")

	r.sweepPythonAppUnits(context.Background(), []*models.PythonApp{{ID: "01APP"}})
	r.sweepPythonAppUnits(context.Background(), nil)

	if got := ag.removed(); len(got) != 0 {
		t.Fatalf("removed %q, want the grace restarted by the row", got)
	}
}

type listAllFailsRepo struct{ repository.PythonAppRepository }

func (listAllFailsRepo) ListAll(context.Context) ([]*models.PythonApp, error) {
	return nil, errors.New("db down")
}

// Without the row list the pass doesn't even ask for the units.
func TestReconcilePythonApps_NoRowsNoSweep(t *testing.T) {
	ag := &sweepAgent{units: `{"app_ids":["01GONE"]}`}
	r := sweepReconciler(ag)
	r.pythonApps = listAllFailsRepo{}
	r.users = notActiveUsers{}
	agePythonAppOrphans(r, "01GONE")

	r.reconcilePythonApps(context.Background())

	if len(ag.calls) != 0 {
		t.Fatalf("calls = %+v, want none", ag.calls)
	}
}

type sweepOnlyRepo struct{ repository.PythonAppRepository }

func (sweepOnlyRepo) ListAll(context.Context) ([]*models.PythonApp, error) { return nil, nil }

// The pass sweeps after it applies the apps.
func TestReconcilePythonApps_Sweeps(t *testing.T) {
	ag := &sweepAgent{units: `{"app_ids":["01GONE"]}`}
	r := sweepReconciler(ag)
	r.pythonApps = sweepOnlyRepo{}
	r.users = notActiveUsers{}
	agePythonAppOrphans(r, "01GONE")

	r.reconcilePythonApps(context.Background())

	if got := ag.removed(); !reflect.DeepEqual(got, []string{"01GONE"}) {
		t.Fatalf("removed %q", got)
	}
}

type goneAppRepo struct {
	statusCaptureRepo
	err error
}

func (g *goneAppRepo) FindByID(context.Context, string) (*models.PythonApp, error) {
	return nil, g.err
}

// An app deleted after the pass read the rows is not applied: the apply would
// write its unit back with no row to own it.
func TestReconcileOnePythonApp_NoApplyOnceTheRowIsGone(t *testing.T) {
	uname := "tenant"
	port := 20017
	for _, err := range []error{repository.ErrNotFound, errors.New("db down")} {
		ag := &sweepAgent{}
		r := sweepReconciler(ag)
		r.pythonApps = &goneAppRepo{err: err}
		r.users = notActiveUsers{u: &models.User{ID: "01USER", Username: &uname}}
		app := &models.PythonApp{
			ID: "01GONE", UserID: "01USER", AppRoot: "/home/tenant/app",
			PythonVersion: "3.12", AppType: "wsgi", Entrypoint: "wsgi:app", LoopbackPort: &port,
		}

		r.reconcileOnePythonApp(context.Background(), app)

		for _, c := range ag.calls {
			if c.Command == "app.python.apply" {
				t.Fatalf("%v: the deleted app was applied", err)
			}
		}
	}
}

// The agent's answer to app.python.list_units decodes with every field kept.
// The agent encodes the same fixture (TestPythonAppListUnits_PanelContract in
// panel-agent/internal/commands).
func TestPythonAppListUnitsContract(t *testing.T) {
	raw, err := os.ReadFile("../agent/testdata/python_app_list_units_response.json")
	if err != nil {
		t.Fatal(err)
	}
	var units pythonAppUnitList
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&units); err != nil {
		t.Fatal(err)
	}
	if len(units.AppIDs) == 0 {
		t.Fatal("the fixture names no app ids")
	}
	again, _ := json.Marshal(units)
	var got, want any
	_ = json.Unmarshal(again, &got)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the panel drops or renames fields:\nwant %s\ngot  %s", raw, again)
	}
}
