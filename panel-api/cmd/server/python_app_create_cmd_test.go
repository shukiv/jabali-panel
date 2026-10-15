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
