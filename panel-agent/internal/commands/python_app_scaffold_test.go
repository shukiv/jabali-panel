package commands

import (
	"context"
	"encoding/json"
	"testing"
)

// The scaffold handler is the security boundary: it must reject a malformed
// caller BEFORE any exec. These cases return at validation (no venv/pip run).
func TestPythonScaffold_RejectsBadParams(t *testing.T) {
	bad := []pythonScaffoldParams{
		{AppID: "ok", Username: "u", PythonVersion: "3.12", Server: "gunicorn", Requirements: []string{"x"}, ScaffoldKind: "template", AppRoot: "relative/path"},      // non-abs app_root
		{AppID: "ok", Username: "u", PythonVersion: "3.12", Server: "gunicorn", Requirements: []string{"x"}, ScaffoldKind: "template", AppRoot: "/home/u/../etc"},     // traversal
		{AppID: "ok", Username: "u", PythonVersion: "3.12", Server: "nope", Requirements: []string{"x"}, ScaffoldKind: "template", AppRoot: "/home/u/app"},            // bad server
		{AppID: "ok", Username: "u", PythonVersion: "9.9", Server: "gunicorn", Requirements: []string{"x"}, ScaffoldKind: "template", AppRoot: "/home/u/app"},         // bad python
		{AppID: "ok", Username: "BAD USER", PythonVersion: "3.12", Server: "gunicorn", Requirements: []string{"x"}, ScaffoldKind: "template", AppRoot: "/home/u/app"}, // bad username
		{AppID: "ok", Username: "u", PythonVersion: "3.12", Server: "gunicorn", Requirements: []string{"x"}, ScaffoldKind: "weird", AppRoot: "/home/u/app"},           // bad kind
		{AppID: "ok", Username: "u", PythonVersion: "3.12", Server: "gunicorn", Requirements: nil, ScaffoldKind: "template", AppRoot: "/home/u/app"},                  // no requirements
	}
	for i, p := range bad {
		raw, _ := json.Marshal(p)
		if _, err := pythonAppScaffoldHandler(context.Background(), raw); err == nil {
			t.Errorf("case %d (%+v) should have been rejected", i, p)
		}
	}
}
