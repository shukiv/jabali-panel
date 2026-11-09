package diagnostic

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/panelrepo"
)

// GH #2467: the bundle's git HEAD and status come from the root-owned
// checkout of #2399, so git runs as root there, not as the service user git
// refuses ("detected dubious ownership").
func TestCollect_RootOwnedCheckoutRunsGitAsRoot(t *testing.T) {
	prevOwner := panelrepo.OwnerUID
	panelrepo.OwnerUID = func(string) (uint32, error) { return 0, nil }
	var calls [][]string
	prevExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	}
	t.Cleanup(func() {
		panelrepo.OwnerUID = prevOwner
		execCommandContext = prevExec
	})

	collect(context.Background())

	want := map[string]bool{"rev-parse HEAD": false, "status --porcelain": false}
	for _, c := range calls {
		line := strings.Join(c, " ")
		if c[0] == "sudo" && strings.Contains(line, " git ") {
			t.Errorf("git ran as the service user on a root-owned checkout: %q", c)
		}
		if !strings.HasPrefix(line, "env GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git -C "+panelrepo.Dir+" ") {
			continue
		}
		for args := range want {
			if strings.HasSuffix(line, " "+args) {
				want[args] = true
			}
		}
	}
	for args, seen := range want {
		if !seen {
			t.Errorf("bundle did not run root git %q on the checkout", args)
		}
	}
}
