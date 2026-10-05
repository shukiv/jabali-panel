package commands

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// GH #1820: a pool whose FPM reload or restart failed stored a last_error the
// admin could not act on — "failed to reload …: %!w(<nil>)" (the is-active
// result shadowed the reload error) or a bare "exit status 1". It must carry
// systemctl's own explanation.

const systemctlResourcesMsg = "Job for jabali-fpm@bob.service failed because of unavailable resources or another system error."

// stubSystemctl answers each systemctl verb with the given shell snippet.
func stubSystemctl(t *testing.T, byVerb map[string]string) {
	t.Helper()
	t.Setenv("JABALI_PHP_POOL_SKIP_RELOAD", "")
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemctl" && len(args) > 0 {
			if script, ok := byVerb[args[0]]; ok {
				return exec.CommandContext(ctx, "sh", "-c", script)
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
}

func failWith(msg string) string {
	return "echo '" + msg + "' >&2; echo 'See \"systemctl status\" for details.' >&2; exit 1"
}

func TestRestartOrReloadUserFPM_ReloadFailureCarriesSystemctlReason(t *testing.T) {
	stubSystemctl(t, map[string]string{
		"reload":    failWith(systemctlResourcesMsg),
		"is-active": "exit 0",
	})
	err := restartOrReloadUserFPM(context.Background(), "bob", "8.4", "8.4")
	if err == nil {
		t.Fatal("a failed reload of an active unit must be an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "%!w") {
		t.Fatalf("error wraps a nil error: %q", msg)
	}
	if !strings.Contains(msg, "unavailable resources") || !strings.Contains(msg, "exit status 1") {
		t.Fatalf("error must name the reload failure and systemctl's reason, got %q", msg)
	}
	if strings.Contains(msg, "See ") {
		t.Fatalf("only systemctl's first line belongs in the error, got %q", msg)
	}
}

func TestRestartOrReloadUserFPM_RestartFailureCarriesSystemctlReason(t *testing.T) {
	stubSystemctl(t, map[string]string{"restart": failWith(systemctlResourcesMsg)})
	err := restartOrReloadUserFPM(context.Background(), "bob", "8.3", "8.4")
	if err == nil || !strings.Contains(err.Error(), "unavailable resources") {
		t.Fatalf("restart error must carry systemctl's reason, got %v", err)
	}
}

func TestSystemctlDetail(t *testing.T) {
	if got := systemctlDetail(nil); got != "" {
		t.Fatalf("no output → %q, want empty", got)
	}
	if got := systemctlDetail([]byte("  \n first line \nsecond\n")); got != " (first line)" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("x", 400)
	if got := systemctlDetail([]byte(long)); len(got) > 310 || !strings.HasSuffix(got, "…)") {
		t.Fatalf("long output not truncated: %d bytes", len(got))
	}
}
