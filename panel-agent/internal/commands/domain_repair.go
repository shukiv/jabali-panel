package commands

// domain_repair.go — GH #576. Surface the operator domain-repair CLIs
// (`jabali domain fix-perms`, `jabali domain prune-orphans`) to the admin GUI
// Repair Center. Same pattern as system.repair_* (system_repair.go): the root
// agent runs the already-privileged `jabali` subcommand and returns its output;
// nothing is re-implemented panel-side. Both ops are quick and NOT
// panel-restart-risky, so they run synchronously (unlike repair --auto).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// domainRepairUserRe bounds the username passed to the CLI. exec.Command uses no shell
// so there's no shell-injection surface, but this stops a value like "--force"
// or "a b" from being mis-parsed as flags/extra args.
var domainRepairUserRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type domainRepairFixPermsParams struct {
	Username string `json:"username"`
}

type domainRepairFixPermsResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// domainRepairFixPermsHandler runs `jabali domain fix-perms <username>`, which
// retrofits each of the user's docroots to group www-data + setgid (symlink
// safe). Idempotent. Display-only output (no GUI selection) so plain text is
// fine.
func domainRepairFixPermsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p domainRepairFixPermsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !domainRepairUserRe.MatchString(p.Username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid username"}
	}
	cmd := execCommandContext(ctx, repairBinary, "domain", "fix-perms", p.Username)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// fix-perms exits non-zero when some docroot failed; surface the
			// code + output rather than treating it as an agent error.
			exitCode = ee.ExitCode()
		} else {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("run domain fix-perms: %v: %s", err, string(out))}
		}
	}
	return domainRepairFixPermsResponse{Output: string(out), ExitCode: exitCode}, nil
}

type domainRepairOrphansParams struct {
	// Apply=false (default) is a dry-run listing; Apply=true issues the
	// irreversible domain.delete for each orphan.
	Apply bool `json:"apply"`
}

// domainRepairOrphansHandler runs `jabali domain prune-orphans [--apply] --json`
// and passes the CLI's structured result straight through (orphans list +
// applied flag), so the GUI shows a confirm over the exact set the CLI
// computed. Dry-run by default.
func domainRepairOrphansHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p domainRepairOrphansParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
		}
	}
	args := []string{"domain", "prune-orphans"}
	if p.Apply {
		args = append(args, "--apply")
	}
	args = append(args, "--json")
	cmd := execCommandContext(ctx, repairBinary, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("run domain prune-orphans: %v: %s", err, string(out))}
		}
		// Non-zero exit with parseable JSON still carries the result; fall
		// through and try to decode. If decode fails, surface the raw output.
	}
	var result map[string]any
	if jErr := json.Unmarshal(out, &result); jErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("parse prune-orphans json: %v: %s", jErr, string(out))}
	}
	return result, nil
}

func init() {
	Default.Register("domain.repair_fix_perms", domainRepairFixPermsHandler)
	Default.Register("domain.repair_orphans", domainRepairOrphansHandler)
}
