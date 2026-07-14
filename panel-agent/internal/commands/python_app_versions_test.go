package commands

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestPythonVersions_ReportsInstalledAndNewestDefault(t *testing.T) {
	orig := pythonLookPath
	t.Cleanup(func() { pythonLookPath = orig })
	// Only 3.11 and 3.13 "installed"; 3.9 present too, to prove numeric order.
	installed := map[string]bool{"python3.9": true, "python3.11": true, "python3.13": true}
	pythonLookPath = func(file string) (string, error) {
		if installed[file] {
			return "/usr/bin/" + file, nil
		}
		return "", exec.ErrNotFound
	}

	raw, err := pythonVersionsHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	got := raw.(PythonVersionsResponse)
	want := []string{"3.9", "3.11", "3.13"} // numeric ascending, not lexical
	if len(got.Versions) != len(want) {
		t.Fatalf("versions = %v, want %v", got.Versions, want)
	}
	for i := range want {
		if got.Versions[i] != want[i] {
			t.Errorf("versions[%d] = %q, want %q (%v)", i, got.Versions[i], want[i], got.Versions)
		}
	}
	if got.Default != "3.13" {
		t.Errorf("default = %q, want 3.13 (newest)", got.Default)
	}
}

func TestPythonVersions_NoneInstalled(t *testing.T) {
	orig := pythonLookPath
	t.Cleanup(func() { pythonLookPath = orig })
	pythonLookPath = func(string) (string, error) { return "", errors.New("nope") }

	raw, err := pythonVersionsHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	got := raw.(PythonVersionsResponse)
	if len(got.Versions) != 0 {
		t.Errorf("expected no versions, got %v", got.Versions)
	}
	if got.Default != "" {
		t.Errorf("default must be empty when none installed, got %q", got.Default)
	}
}
