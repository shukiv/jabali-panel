// docker_app.logs + docker_app.exec — diagnostics verbs for the M48
// marketplace.
//
// Why one file: they share the same slug-validation + dir-lookup
// pattern; splitting would just duplicate the prelude.
//
// logs  — tail the compose project's logs. One-shot snapshot, no
//         streaming. UI calls every 5s for a near-live view.
// exec  — admin-only single-command exec in the first service
//         container. NOT a TTY/shell. The blueprint defers
//         interactive shells (xterm.js + websocket) to a later
//         phase because the agent's NDJSON UDS transport is
//         one-shot request/response and adapting it to a bidi
//         stream is a separate design task. For now the operator
//         can pipe commands one at a time.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type dockerAppLogsParams struct {
	Slug    string `json:"slug"`
	Lines   int    `json:"lines,omitempty"`   // default 200, max 10000
	Service string `json:"service,omitempty"` // optional; empty = all services
}

type dockerAppLogsResponse struct {
	Slug string `json:"slug"`
	Logs string `json:"logs"`
}

func dockerAppLogsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppLogsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	if _, err := os.Stat(filepath.Join(dir, "compose.yml")); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("%s/compose.yml not found", dir)}
	}
	if p.Lines <= 0 {
		p.Lines = 200
	}
	if p.Lines > 10000 {
		p.Lines = 10000
	}

	args := []string{"logs", "--tail", strconv.Itoa(p.Lines), "--timestamps", "--no-color"}
	if p.Service != "" {
		// Defensive: same charset rule as slug. Service names come
		// from the compose project, themselves catalog-defined.
		if !dockerAppSlugRE.MatchString(p.Service) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid service %q", p.Service)}
		}
		args = append(args, p.Service)
	}
	out, err := runDockerCompose(ctx, dir, args...)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("docker compose logs failed: %v", err),
			Details: json.RawMessage(fmt.Sprintf(`{"stderr": %q}`, out)),
		}
	}
	return dockerAppLogsResponse{Slug: p.Slug, Logs: out}, nil
}

type dockerAppExecParams struct {
	Slug    string `json:"slug"`
	Service string `json:"service,omitempty"` // empty = first service in compose
	Command string `json:"command"`           // single-shell-string command
}

type dockerAppExecResponse struct {
	Slug     string `json:"slug"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func dockerAppExecHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppExecParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	if p.Command == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "command is required"}
	}
	dir := filepath.Join(dockerAppDataRoot, p.Slug)
	if _, err := os.Stat(filepath.Join(dir, "compose.yml")); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeNotFound, Message: fmt.Sprintf("%s/compose.yml not found", dir)}
	}

	// service= empty -> docker compose exec uses the FIRST service.
	// We pass --no-TTY so the agent UDS doesn't try to allocate one.
	args := []string{"exec", "-T"}
	if p.Service != "" {
		if !dockerAppSlugRE.MatchString(p.Service) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid service %q", p.Service)}
		}
		args = append(args, p.Service)
	} else {
		// First service inferred via `docker compose ps --services`.
		out, err := runDockerCompose(ctx, dir, "ps", "--services")
		if err != nil || out == "" {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "could not enumerate services"}
		}
		first := splitLines(out)[0]
		args = append(args, first)
	}
	args = append(args, "sh", "-c", p.Command)

	cmd := execCommandContext(ctx, "docker", append([]string{"compose"}, args...)...)
	cmd.Dir = dir
	stdout, stderr, exitCode := runWithStdoutStderr(cmd)
	return dockerAppExecResponse{
		Slug:     p.Slug,
		ExitCode: exitCode,
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

// runWithStdoutStderr runs the command and returns its stdout, stderr,
// and exit code. exit code -1 when the process couldn't be started.
func runWithStdoutStderr(cmd *exec.Cmd) (string, string, int) {
	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		return "", err.Error(), -1
	}
	stdoutBytes, _ := readAll(stdoutPipe)
	stderrBytes, _ := readAll(stderrPipe)
	err := cmd.Wait()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			exit = -1
			stderrBytes = []byte(err.Error())
		}
	}
	return string(stdoutBytes), string(stderrBytes), exit
}

// readAll is a tiny helper to drain a reader to []byte. We keep it
// local so the file doesn't pull io into its imports (already pulled
// elsewhere in the package, but each file's import block stays
// scoped to its own usage).
func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	const chunk = 4096
	buf := make([]byte, 0, chunk)
	tmp := make([]byte, chunk)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			return buf, err
		}
	}
}

func init() {
	Default.Register("docker_app.logs", dockerAppLogsHandler)
	Default.Register("docker_app.exec", dockerAppExecHandler)
}
