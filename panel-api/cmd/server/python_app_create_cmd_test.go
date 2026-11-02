package main

import (
	"strings"
	"testing"
)

// GH #2156: the CLI refuses a framework install on a Python older than the
// entry's python_min, as the API does, before it touches the database.
func TestPythonAppCreateCmd_RefusesBelowPythonMin(t *testing.T) {
	// The dev fallback reads install/py-frameworks from the repo root.
	t.Chdir("../../..")
	if loadPyFrameworkCatalogCLI().Len() == 0 {
		t.Skip("framework catalog not readable from the repo root")
	}
	cmd := newPythonAppCreateCmd()
	for flag, v := range map[string]string{
		"name": "news", "domain": "news.example.com", "python-version": "3.11",
		"app-root": "/home/t/news", "framework": "sic",
	} {
		if err := cmd.Flags().Set(flag, v); err != nil {
			t.Fatalf("set --%s: %v", flag, err)
		}
	}
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "needs Python 3.12 or newer") {
		t.Fatalf("err = %v, want the python_min refusal", err)
	}
}

// ADR-0175: a --runtime node create follows the API's rules before it touches
// the database.
func TestPythonAppCreateCmd_NodeValidation(t *testing.T) {
	for _, tc := range []struct {
		flags map[string]string
		want  string
	}{
		{map[string]string{"runtime": "ruby"}, "invalid --runtime"},
		{map[string]string{"runtime": "node", "node-version": "20", "entrypoint": "server.js"}, "invalid --node-version"},
		{map[string]string{"runtime": "node", "framework": "django"}, "--framework is for Python apps only"},
		{map[string]string{"runtime": "node", "entrypoint": "../x/server.js"}, "invalid --entrypoint"},
		{map[string]string{"runtime": "node", "app-type": "wsgi", "entrypoint": "server.js"}, "invalid --app-type"},
		{map[string]string{"runtime": "node", "app-type": "npm", "entrypoint": "start; id"}, "invalid --entrypoint"},
		{map[string]string{"runtime": "node", "entrypoint": "server.js", "env": "PORT=3000"}, "PORT is set by the panel"},
	} {
		cmd := newPythonAppCreateCmd()
		tc.flags["name"] = "api"
		for flag, v := range tc.flags {
			if err := cmd.Flags().Set(flag, v); err != nil {
				t.Fatalf("set --%s: %v", flag, err)
			}
		}
		if err := cmd.RunE(cmd, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.flags, err, tc.want)
		}
	}
}

func TestCLINodeCreate_Defaults(t *testing.T) {
	appType, entry := "wsgi", ""
	if err := cliNodeCreate(&appType, &entry, "22", "", false); err == nil {
		t.Fatal("a node app needs an entrypoint script")
	}
	appType, entry = "npm", ""
	if err := cliNodeCreate(&appType, &entry, "22", "", true); err != nil || entry != "start" {
		t.Fatalf("npm: entry=%q err=%v, want start", entry, err)
	}
	appType, entry = "wsgi", "dist/server.mjs"
	if err := cliNodeCreate(&appType, &entry, "22", "", false); err != nil || appType != "node" {
		t.Fatalf("default app type = %q err=%v, want node", appType, err)
	}
}
