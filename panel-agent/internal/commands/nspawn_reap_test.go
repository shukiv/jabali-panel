package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// recordExec swaps the exec seam for a recorder that captures every
// (name, args...) invocation and returns a no-op success command. Restored on
// cleanup.
func recordExec(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}
	t.Cleanup(func() { execCommandContext = prev })
	return &calls
}

func TestReapUserNspawnPHPUnit_TearsDownUnitAndConfig(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("JABALI_NSPAWN_CONFIG_ROOT", cfgDir)
	// A leftover .nspawn config for the instance must be removed.
	cfg := filepath.Join(cfgDir, "den-php.nspawn")
	if err := os.WriteFile(cfg, []byte("[Exec]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := recordExec(t)
	reapUserNspawnPHPUnit(context.Background(), "den")

	wantUnit := "systemd-nspawn@den-php.service"
	var sawDisable, sawReset bool
	for _, c := range *calls {
		if c[0] != "systemctl" {
			t.Fatalf("only systemctl calls expected, got %v", c)
		}
		switch {
		case len(c) == 4 && c[1] == "disable" && c[2] == "--now" && c[3] == wantUnit:
			sawDisable = true
		case len(c) == 3 && c[1] == "reset-failed" && c[2] == wantUnit:
			sawReset = true
		}
	}
	if !sawDisable {
		t.Errorf("expected `systemctl disable --now %s`; calls=%v", wantUnit, *calls)
	}
	if !sawReset {
		t.Errorf("expected `systemctl reset-failed %s` (disable alone leaves the failed entry); calls=%v", wantUnit, *calls)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Errorf("expected %s removed, stat err=%v", cfg, err)
	}
}

func TestReapUserNspawnPHPUnit_RejectsBadUsername(t *testing.T) {
	calls := recordExec(t)
	// A malformed username must never reach systemctl (no injection surface
	// into the unit name).
	reapUserNspawnPHPUnit(context.Background(), "../../etc/evil")
	if len(*calls) != 0 {
		t.Fatalf("bad username must issue no commands, got %v", *calls)
	}
}

// TestReapUserNspawnPHPUnit_BestEffortNoConfig proves the missing-config case is
// a silent no-op (the common case: unit enabled, no generated .nspawn file).
func TestReapUserNspawnPHPUnit_BestEffortNoConfig(t *testing.T) {
	t.Setenv("JABALI_NSPAWN_CONFIG_ROOT", t.TempDir())
	calls := recordExec(t)
	reapUserNspawnPHPUnit(context.Background(), "bob")
	if len(*calls) != 2 {
		t.Fatalf("expected exactly disable + reset-failed (2 calls), got %v", *calls)
	}
}
