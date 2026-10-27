package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// GH #2289: the sweep finds app units by their files. Only the agent's own
// unit files count: a build scope, a drop-in directory, a symlink or a name
// whose id apply would refuse is not an app unit.
func TestListPythonAppUnitIDs(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"jabali-app-01BBB.service",
		"jabali-app-01AAA.service",
		"jabali-app-build-01AAA-1.scope",
		"jabali-app-a.b.service",
		"jabali-panel.service",
		"nginx.service",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "jabali-app-01CCC.service"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nginx.service"), filepath.Join(dir, "jabali-app-01DDD.service")); err != nil {
		t.Fatal(err)
	}

	ids, err := listPythonAppUnitIDs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"01AAA", "01BBB"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ids = %q, want %q", ids, want)
	}
}

// An unreadable unit directory is an error, never an empty list: the sweep
// must not read "no units" from a failed read.
func TestListPythonAppUnitIDs_MissingDirIsAnError(t *testing.T) {
	if _, err := listPythonAppUnitIDs(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("want an error for a missing directory")
	}
}

// removeExec records the commands app.python.remove runs and answers
// `systemctl is-active` with state.
func removeExec(t *testing.T, state string) (calls func() []string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "calls.log")
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		appendLine(t, log, name+" "+strings.Join(args, " "))
		if name == "systemctl" && len(args) > 0 && args[0] == "is-active" {
			return exec.CommandContext(ctx, "echo", state)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return func() []string {
		b, _ := os.ReadFile(log)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

func hasCall(calls []string, want string) bool {
	for _, c := range calls {
		if c == want {
			return true
		}
	}
	return false
}

// GH #2289: a unit that is still running after the stop keeps its port, so
// remove fails and leaves the unit in place for the next try.
func TestPythonAppRemove_FailsWhileTheUnitStillRuns(t *testing.T) {
	for _, state := range []string{"active", "deactivating", ""} {
		calls := removeExec(t, state)
		_, err := pythonAppRemoveHandler(context.Background(), json.RawMessage(`{"app_id":"01APP"}`))
		if err == nil {
			t.Fatalf("state %q: want an error", state)
		}
		got := calls()
		if hasCall(got, "systemctl disable --quiet jabali-app-01APP.service") || hasCall(got, "systemctl daemon-reload") {
			t.Fatalf("state %q: the unit was taken down anyway: %q", state, got)
		}
	}
}

func TestPythonAppRemove_RemovesAStoppedUnit(t *testing.T) {
	for _, state := range []string{"inactive", "failed"} {
		calls := removeExec(t, state)
		if _, err := pythonAppRemoveHandler(context.Background(), json.RawMessage(`{"app_id":"01APP"}`)); err != nil {
			t.Fatalf("state %q: %v", state, err)
		}
		got := calls()
		for _, want := range []string{
			"systemctl stop jabali-app-01APP.service",
			"systemctl disable --quiet jabali-app-01APP.service",
			"systemctl daemon-reload",
		} {
			if !hasCall(got, want) {
				t.Fatalf("state %q: no %q in %q", state, want, got)
			}
		}
	}
}

// The panel's side of app.python.list_units: what the agent answers decodes
// into what the panel expects. The panel decodes the same fixture
// (TestPythonAppListUnitsContract in panel-api/internal/reconciler).
func TestPythonAppListUnits_PanelContract(t *testing.T) {
	raw, err := os.ReadFile("../../../panel-api/internal/agent/testdata/python_app_list_units_response.json")
	if err != nil {
		t.Fatal(err)
	}
	var res pythonAppListUnitsResponse
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(res)
	var got, want any
	_ = json.Unmarshal(again, &got)
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the agent drops or renames fields:\nwant %s\ngot  %s", raw, again)
	}
}
