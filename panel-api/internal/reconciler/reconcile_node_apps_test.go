package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sort"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ADR-0175: Node.js apps share the python_apps registry (runtime "node"). The
// reconciler sends them to app.node.apply, and applies each runtime's apps
// only while its setting is on.

type applyCall struct {
	method string
	params map[string]any
}

type recordingAppAgent struct{ calls []applyCall }

func (a *recordingAppAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	a.calls = append(a.calls, applyCall{method, params.(map[string]any)})
	return json.Marshal(map[string]any{"active": true, "unit": "jabali-app-x.service"})
}

type listingAppRepo struct {
	statusCaptureRepo
	apps []*models.PythonApp
}

func (r *listingAppRepo) ListAll(context.Context) ([]*models.PythonApp, error) { return r.apps, nil }
func (r *listingAppRepo) ListEnv(context.Context, string) ([]*models.PythonAppEnv, error) {
	return nil, nil
}

type appsSettings struct {
	repository.ServerSettingsRepository
	s *models.ServerSettings
}

func (f appsSettings) Get(context.Context) (*models.ServerSettings, error) { return f.s, nil }

func nodeTestApp() *models.PythonApp {
	port := 20001
	cmd := "ignored"
	return &models.PythonApp{
		ID: "01NODE", UserID: "01USER", Runtime: "node", AppRoot: "/home/tenant/api",
		PythonVersion: "22", AppType: "npm", Entrypoint: "start", BaseURI: "/",
		LoopbackPort: &port, StartCommand: &cmd, Status: models.PythonAppStatusPending,
	}
}

func appsReconciler(ag *recordingAppAgent, apps []*models.PythonApp, s *models.ServerSettings) *Reconciler {
	uname := "tenant"
	return &Reconciler{
		agent:          ag,
		pythonApps:     &listingAppRepo{apps: apps},
		users:          notActiveUsers{u: &models.User{ID: "01USER", Username: &uname}},
		serverSettings: appsSettings{s: s},
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestReconcilePythonApps_NodeAppGoesToNodeApply(t *testing.T) {
	ag := &recordingAppAgent{}
	appsReconciler(ag, []*models.PythonApp{nodeTestApp()}, &models.ServerSettings{NodeAppsEnabled: true}).
		reconcilePythonApps(context.Background())
	if len(ag.calls) != 1 || ag.calls[0].method != "app.node.apply" {
		t.Fatalf("calls = %+v, want one app.node.apply", ag.calls)
	}
	p := ag.calls[0].params
	if p["node_version"] != "22" || p["app_type"] != "npm" || p["entrypoint"] != "start" || p["port"] != 20001 ||
		p["username"] != "tenant" || p["app_root"] != "/home/tenant/api" {
		t.Fatalf("params = %v", p)
	}
	for _, k := range []string{"python_version", "base_uri", "start_command"} {
		if _, ok := p[k]; ok {
			t.Errorf("app.node.apply got the Python-only %q", k)
		}
	}
}

func TestReconcilePythonApps_EachRuntimeFollowsItsSetting(t *testing.T) {
	py := tasksLimitApp(models.PythonAppStatusPending, nil)
	for _, tc := range []struct {
		name         string
		python, node bool
		want         []string
	}{
		{"both on", true, true, []string{"app.node.apply", "app.python.apply"}},
		{"python only", true, false, []string{"app.python.apply"}},
		{"node only", false, true, []string{"app.node.apply"}},
		{"both off", false, false, nil},
	} {
		ag := &recordingAppAgent{}
		appsReconciler(ag, []*models.PythonApp{py, nodeTestApp()}, &models.ServerSettings{PythonAppsEnabled: tc.python, NodeAppsEnabled: tc.node}).
			reconcilePythonApps(context.Background())
		var got []string
		for _, c := range ag.calls {
			got = append(got, c.method)
		}
		sort.Strings(got)
		if len(got) != len(tc.want) || (len(got) > 0 && got[0] != tc.want[0]) || (len(got) > 1 && got[1] != tc.want[1]) {
			t.Errorf("%s: calls = %v, want %v", tc.name, got, tc.want)
		}
	}
}
