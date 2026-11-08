package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"testing"
)

// The panel owns the module flags. An install it starts tells install.sh so,
// and install.sh then leaves the flag alone; run by an operator, `install.sh
// --install-module KEY` turns the module on.
func TestSystemModuleInstall_TellsInstallShThePanelStartedIt(t *testing.T) {
	fakeInstall(t, "", 0)
	fakeServerInstaller(t, "")
	var got []string
	prev := execCommandContext
	t.Cleanup(func() { execCommandContext = prev })
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemd-run" {
			got = args
			return exec.CommandContext(ctx, "true")
		}
		return prev(ctx, name, args...)
	}

	raw, _ := json.Marshal(moduleStatusRequest{Key: "mail"})
	if _, err := systemModuleInstallHandler(context.Background(), raw); err != nil {
		t.Fatalf("install: %v", err)
	}
	sep := slices.Index(got, "--")
	if sep < 0 {
		t.Fatalf("systemd-run args have no --: %q", got)
	}
	if !slices.Contains(got[:sep], "--setenv=JABALI_MODULE_INSTALL_BY_PANEL=1") {
		t.Errorf("systemd-run options %q don't set JABALI_MODULE_INSTALL_BY_PANEL=1 for install.sh", got[:sep])
	}
	if want := []string{"bash", moduleInstallScript, "--install-module", "mail"}; !slices.Equal(got[sep+1:], want) {
		t.Errorf("command = %q, want %q", got[sep+1:], want)
	}
}
