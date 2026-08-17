package commands

import "os/exec"

// execCommandContext and execCommand are the single exec boundary for every
// handler in package commands. Production code dispatches straight to os/exec
// with zero indirection; the test binary replaces these vars in TestMain (see
// exec_seam_test.go) so a bare `go test ./...` can never mutate the host —
// no polkit prompts, no systemctl, no rm, no timedatectl. GH #994.
//
// Do NOT call exec.Command / exec.CommandContext directly in non-test files;
// TestExecSeam_NoRawExec enforces that. Use these vars instead — they return
// the same *exec.Cmd, so .Dir/.Env/.Stdin/.CombinedOutput/.Run/.Start all work
// unchanged.
var (
	execCommandContext = exec.CommandContext
	execCommand        = exec.Command
)
