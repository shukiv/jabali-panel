package commands

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// stubSystemctlUfw scripts execCommandContext so ftp.disable sees deterministic
// systemctl/ufw output. The verb is all reads-after-writes, so only the query
// commands (is-active / is-enabled / show / ufw status) need real output; the
// mutating stop/mask/delete just exit 0.
func stubSystemctlUfw(t *testing.T, isActive, isEnabled, loadState, ufwStatus string) {
	t.Helper()
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		out := ""
		switch {
		case name == "systemctl" && len(args) >= 1 && args[0] == "is-active":
			out = isActive
		case name == "systemctl" && len(args) >= 1 && args[0] == "is-enabled":
			out = isEnabled
		case name == "systemctl" && len(args) >= 1 && args[0] == "show":
			out = loadState
		case name == "ufw" && len(args) >= 1 && args[0] == "status":
			out = ufwStatus
		}
		return exec.CommandContext(ctx, "/bin/sh", "-c", fmt.Sprintf("printf %%s '%s'; exit 0", out))
	}
	t.Cleanup(func() { execCommandContext = prev })
}

func TestFtpDisable_SucceedsWhenInactiveAndMasked(t *testing.T) {
	stubSystemctlUfw(t, "inactive", "masked", "loaded", "Status: inactive")
	res, err := ftpModuleDisableHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	r := res.(ftpDisableResponse)
	if r.Active || !r.Masked {
		t.Fatalf("want inactive+masked, got %+v", r)
	}
}

func TestFtpDisable_FailsClosedWhenStillActive(t *testing.T) {
	// The daemon refused to stop — the verb MUST report a typed failure so the
	// panel never shows FTP "off" while it is still reachable, and the
	// reconciler keeps retrying.
	stubSystemctlUfw(t, "active", "masked", "loaded", "")
	_, err := ftpModuleDisableHandler(context.Background(), nil)
	if err == nil {
		t.Fatal("expected failed_precondition while vsftpd is still active")
	}
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("want failed_precondition, got %v", err)
	}
}

func TestFtpDisable_FailsClosedWhenNotMasked(t *testing.T) {
	// Stopped but NOT masked: a dependency or a stray reconciler install could
	// restart it, so this is not a safe "off" state.
	stubSystemctlUfw(t, "inactive", "enabled", "loaded", "")
	_, err := ftpModuleDisableHandler(context.Background(), nil)
	if err == nil {
		t.Fatal("expected failed_precondition while vsftpd is stopped but unmasked")
	}
}

// A never-installed vsftpd (unit not-found) is already "off" — the verb must
// succeed, not fail on the absent unit's empty is-active/is-enabled output.
func TestFtpDisable_NeverInstalledSucceeds(t *testing.T) {
	stubSystemctlUfw(t, "", "", "not-found", "")
	if _, err := ftpModuleDisableHandler(context.Background(), nil); err != nil {
		t.Fatalf("a never-installed vsftpd must succeed (nothing to disable), got %v", err)
	}
}

// A lingering ufw rule alone (daemon inactive+masked = not reachable) is
// reported but does NOT fail the op — that would hot-loop the reconciler on a
// cosmetic rule when nothing is actually exposed.
func TestFtpDisable_LingeringUfwRuleDoesNotFail(t *testing.T) {
	stubSystemctlUfw(t, "inactive", "masked", "loaded", "21/tcp ALLOW Anywhere")
	res, err := ftpModuleDisableHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("masked+inactive daemon must succeed despite a stray ufw rule, got %v", err)
	}
	if !res.(ftpDisableResponse).PortsOpen {
		t.Fatal("ports_open must be reported so Phase C can surface the stray rule")
	}
}
