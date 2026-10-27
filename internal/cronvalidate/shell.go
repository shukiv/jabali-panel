package cronvalidate

import (
	"path/filepath"
	"strings"
)

// Shell scripts in cron (GH #2290). An account whose hosting package includes
// SSH may schedule its own .sh scripts. A tenant job runs inside the account's
// SSH sandbox (jabali-ssh-shell --exec, GH #1458), the same one its SSH shell
// runs in, so a script gives the account nothing its shell doesn't. Without
// SSH the closed list stays: a script would be a way around it. A root job
// never gets this (the maintainer kept root on the closed list).

// ErrCodeShellNeedsSSH: a shell script, for an account whose package has no SSH.
const ErrCodeShellNeedsSSH = "shell_needs_ssh"

// Policy is what the account's hosting package allows beyond the closed list.
// The zero value is the closed list, so a caller that doesn't know the package
// refuses shell scripts.
type Policy struct {
	// ShellScripts lets a job run a .sh script inside the account with bash or
	// sh. The panel sets it when the account's package includes SSH.
	ShellScripts bool
}

// shellBinaries are the shells a job may name: bash and sh, bare or at their
// system paths. Not a shell under the account's home: that could be any
// program.
var shellBinaries = map[string]bool{
	"bash": true, "/bin/bash": true, "/usr/bin/bash": true,
	"sh": true, "/bin/sh": true, "/usr/bin/sh": true,
}

// isShellScriptPath reports whether token is a script named on its own: an
// absolute path ending in .sh.
func isShellScriptPath(token string) bool {
	return filepath.IsAbs(token) && strings.HasSuffix(token, ".sh")
}

// validateShellCommand requires `<shell> <script.sh> [args]` or
// `<script.sh> [args]`, the script an absolute .sh file inside the account.
// No option may come before the script: -c runs inline code, -s reads stdin,
// --rcfile and --init-file run another file. The script alone runs with bash.
func validateShellCommand(argv []string, scriptRoots []string) (*Command, error) {
	if isShellScriptPath(argv[0]) {
		argv = append([]string{"/bin/bash"}, argv...)
	}
	if len(argv) < 2 || strings.HasPrefix(argv[1], "-") {
		return nil, &ValidationError{
			Code:   ErrCodeBadPathArg,
			Detail: "a shell job runs a script: bash /home/<you>/script.sh [arguments], with no options before the script",
		}
	}
	if err := validateScriptFile(argv[1], ".sh", "shell", scriptRoots); err != nil {
		return nil, err
	}
	return &Command{Argv: argv}, nil
}

// isShellCommand reports whether argv[0] makes the command a shell job.
func isShellCommand(binary string) bool {
	return shellBinaries[binary] || isShellScriptPath(binary)
}

// UsesShell reports whether a stored command has a line that runs a shell
// script. The reconciler stops such a job when the account's package no
// longer includes SSH.
func UsesShell(raw string) bool {
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(strings.TrimSpace(strings.TrimRight(line, "\r")))
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		if isShellCommand(strings.Trim(fields[0], `'"`)) {
			return true
		}
	}
	return false
}
