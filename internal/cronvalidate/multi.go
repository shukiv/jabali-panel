package cronvalidate

import (
	"fmt"
	"strings"
)

// Multi-command cron support (GH #1435). A cron job may hold several commands,
// one per line, so a tenant can run an ordered sequence — e.g. generate an
// export then import it — in a single scheduled unit instead of racing two
// separately-timed jobs.
//
// SECURITY: this does NOT loosen the per-command contract. Each non-empty,
// non-comment line is validated INDEPENDENTLY by ValidateAny — the same closed
// wp/php (or self-domain http-trigger) allow-list, the same metacharacter and
// control-character rejection. The agent renders one ExecStart= per validated
// command into the Type=oneshot unit; systemd runs them in order and stops at
// the first failure. Splitting on the newline BEFORE validation means a newline
// never reaches ValidateCommand (which still rejects control chars), so the
// systemd-unit-injection vector the single-command validator guards against is
// unchanged.

const (
	// maxCronCommands caps the number of commands in one job.
	maxCronCommands = 20
	// maxMultiCommandBytes matches the cron_jobs.command column (varchar 1024).
	maxMultiCommandBytes = 1024
)

// ValidateAnyMulti validates a (possibly multi-line) cron command into an
// ordered list of validated commands. Blank lines and comment/shebang lines
// (starting with '#') are skipped, so a pasted script's `#!/bin/bash` header is
// tolerated — but every executable line must still pass the closed allow-list.
// A single-line command yields a one-element slice (back-compatible).
func ValidateAnyMulti(raw string, ownedDocroots, ownedDomains []string) ([]*Command, error) {
	if len(raw) > maxMultiCommandBytes {
		return nil, &ValidationError{
			Code:   ErrCodeTooLong,
			Detail: fmt.Sprintf("command exceeds %d bytes (%d bytes)", maxMultiCommandBytes, len(raw)),
		}
	}

	var cmds []*Command
	for i, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // blank line or comment/shebang
		}
		if len(cmds) >= maxCronCommands {
			return nil, &ValidationError{
				Code:   ErrCodeTooLong,
				Detail: fmt.Sprintf("too many commands (max %d)", maxCronCommands),
			}
		}
		c, err := ValidateAny(trimmed, ownedDocroots, ownedDomains)
		if err != nil {
			// Prefix the failing line number so the operator can fix the right line.
			if ve, ok := err.(*ValidationError); ok {
				return nil, &ValidationError{Code: ve.Code, Detail: fmt.Sprintf("line %d: %s", i+1, ve.Detail)}
			}
			return nil, err
		}
		cmds = append(cmds, c)
	}

	if len(cmds) == 0 {
		return nil, &ValidationError{Code: ErrCodeEmpty, Detail: "command cannot be empty"}
	}
	return cmds, nil
}

// NormalizeMultiCommand re-serialises the executable lines of a validated
// multi-command string: blanks and comments dropped, one command per line. The
// panel stores this so the DB row is exactly what runs.
func NormalizeMultiCommand(raw string) string {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, trimmed)
	}
	return strings.Join(lines, "\n")
}
