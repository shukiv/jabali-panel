package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/panelrepo"
)

func withCheckoutOwner(t *testing.T, uid uint32) {
	t.Helper()
	prev := panelrepo.OwnerUID
	panelrepo.OwnerUID = func(string) (uint32, error) { return uid, nil }
	t.Cleanup(func() { panelrepo.OwnerUID = prev })
}

// GH #2467: since #2399 the checkout is root-owned, and git refuses it to the
// service user ("detected dubious ownership"). The check must run git as
// root there, with system and global config off, never through sudo.
func TestSystemUpdateCheck_RootOwnedCheckoutRunsGitAsRoot(t *testing.T) {
	withCheckoutOwner(t, 0)
	calls := recordExec(t)
	if _, err := systemUpdateCheckHandler(context.Background(), json.RawMessage(`{"channel":"stable"}`)); err != nil {
		t.Fatalf("update check: %v", err)
	}
	if len(*calls) == 0 {
		t.Fatal("the check ran no git command")
	}
	for _, c := range *calls {
		if len(c) < 6 || c[0] != "env" || c[1] != "GIT_CONFIG_NOSYSTEM=1" || c[2] != "GIT_CONFIG_GLOBAL=/dev/null" ||
			c[3] != "git" || c[4] != "-C" || c[5] != systemRepoDir {
			t.Errorf("git not run as root on a root-owned checkout: %q", c)
		}
	}
	first := (*calls)[0]
	if got := first[len(first)-2:]; got[0] != "rev-parse" || got[1] != "HEAD" {
		t.Errorf("first call = %q, want it to read HEAD", first)
	}
}

// A checkout from before #2399 is still the service user's: git runs as that
// user, as before.
func TestSystemUpdateCheck_ServiceUserCheckoutRunsGitAsServiceUser(t *testing.T) {
	withCheckoutOwner(t, 1000)
	calls := recordExec(t)
	if _, err := systemUpdateCheckHandler(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatalf("update check: %v", err)
	}
	if len(*calls) == 0 {
		t.Fatal("the check ran no git command")
	}
	for _, c := range *calls {
		if len(c) < 7 || c[0] != "sudo" || c[1] != "-u" || c[2] != systemServiceUser || c[3] != "-H" || c[4] != "git" {
			t.Errorf("git not run as the service user on its own checkout: %q", c)
		}
	}
}
