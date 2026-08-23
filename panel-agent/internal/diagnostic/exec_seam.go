package diagnostic

import "os/exec"

// execCommandContext and execCommand are the single exec boundary for the
// diagnostic collectors. Production dispatches straight to os/exec with zero
// indirection; the test binary replaces these vars in TestMain (see
// exec_seam_test.go) so a bare `go test ./...` never shells out to real host
// tools — systemctl, journalctl, iptables, cscli, cat, tail. GH #994 / #1160.
//
// The diagnostic bundle is read-only (it never mutates the host), so this is a
// hermeticity guarantee — deterministic, host-independent tests — rather than a
// host-mutation guard. Do NOT call exec.Command / exec.CommandContext directly
// in non-test files in this package; use these vars so the seam holds.
var (
	execCommandContext = exec.CommandContext
	execCommand        = exec.Command
)
