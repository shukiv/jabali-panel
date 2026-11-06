package commands

import (
	"context"
	"os/exec"
	"strings"
)

// sqlOnStdin returns `client args...` with sql on its stdin rather than on
// argv (-e). Use it for every statement that carries a password, a password
// hash or a private key, so the secret never shows in the process list. The
// client runs the statements the same way it runs -e: in order, stopping at
// the first error.
func sqlOnStdin(ctx context.Context, sql, client string, args ...string) *exec.Cmd {
	cmd := execCommandContext(ctx, client, args...)
	cmd.Stdin = strings.NewReader(sql)
	return cmd
}
