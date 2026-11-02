package main

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A Node.js app keeps its version in python_version (ADR-0175), so the list
// has to name the runtime from the app's runtime, not from that column.
func TestPythonAppRuntimeLabel(t *testing.T) {
	cases := []struct {
		app  models.PythonApp
		want string
	}{
		{models.PythonApp{Runtime: "python", PythonVersion: "3.12", AppType: "wsgi"}, "py3.12/wsgi"},
		{models.PythonApp{Runtime: "", PythonVersion: "3.11", AppType: "asgi"}, "py3.11/asgi"},
		{models.PythonApp{Runtime: "node", PythonVersion: "22", AppType: "node"}, "node22/node"},
		{models.PythonApp{Runtime: "node", PythonVersion: "22", AppType: "npm"}, "node22/npm"},
	}
	for _, tc := range cases {
		if got := pythonAppRuntimeLabel(&tc.app); got != tc.want {
			t.Errorf("runtime %q: got %q, want %q", tc.app.Runtime, got, tc.want)
		}
	}
}
