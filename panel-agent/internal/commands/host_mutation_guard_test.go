package commands

import (
	"os"
	"testing"
)

// requireHostMutationAllowed skips a test that reaches real host-mutating exec
// — systemd-run / systemctl stop|disable|mask / rm -rf / useradd / userdel /
// groupadd / chpasswd / timedatectl — because it drives production command
// paths through the real OS instead of a fake runner (GH #994).
//
// Running such a test on a developer laptop fires polkit root-password prompts;
// on any provisioned panel host it would stop a live PHP-FPM service, rm -rf a
// tenant docroot subtree, or change the system timezone. The suite passing on a
// laptop today is environmental luck (no matching users/units), not a
// safeguard.
//
// Default: SKIP, so `go test ./...` on a laptop or CI is inert. To actually run
// these against the host, set JABALI_ALLOW_HOST_MUTATION=1 on a THROWAWAY
// provisioned box you are willing to have mutated.
func requireHostMutationAllowed(t *testing.T) {
	t.Helper()
	if os.Getenv("JABALI_ALLOW_HOST_MUTATION") != "1" {
		t.Skip("GH #994: touches the host (systemd/rm/useradd/timedatectl); " +
			"set JABALI_ALLOW_HOST_MUTATION=1 on a throwaway provisioned host to run")
	}
}
