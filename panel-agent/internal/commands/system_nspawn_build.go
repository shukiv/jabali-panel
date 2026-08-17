package commands

// system_nspawn_build.go — GH #574. Surface the operator nspawn image
// lifecycle CLIs (`jabali nspawn build` / `prune`) to the admin GUI. Build is a
// long-running debootstrap so it runs as a transient systemd unit + is polled
// via system.nspawn_build_status (Repair Center pattern). Prune is quick and
// runs synchronously. The root agent runs the already-privileged jabali CLI;
// nothing is re-implemented panel-side.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

const nspawnBuildUnit = "jabali-nspawn-build.service"

// Validate build inputs before shelling out (belt-and-suspenders — the CLI
// validates too, but reject early so a bad value never reaches systemd-run).
var (
	nspawnBuildNameRe     = regexp.MustCompile(`^[a-z0-9-]+$`)
	nspawnBuildSnapshotRe = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)
)

type nspawnBuildParams struct {
	Codename string `json:"codename"`
	Version  string `json:"version"`
	Snapshot string `json:"snapshot"`
	Suite    string `json:"suite"`
	Includes string `json:"includes"`
}

type nspawnBuildResponse struct {
	Unit      string `json:"unit"`
	StartedAt string `json:"started_at"`
}

// systemNspawnBuildHandler kicks `jabali nspawn build …` as a transient unit
// (debootstrap runs for minutes); the UI polls system.nspawn_build_status.
func systemNspawnBuildHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p nspawnBuildParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !nspawnBuildNameRe.MatchString(p.Codename) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "codename must match [a-z0-9-]+"}
	}
	if !nspawnBuildNameRe.MatchString(p.Version) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "version must match [a-z0-9-]+"}
	}
	if !nspawnBuildSnapshotRe.MatchString(p.Snapshot) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "snapshot must match YYYYMMDDTHHMMSSZ"}
	}
	if p.Suite != "" && !nspawnBuildNameRe.MatchString(p.Suite) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "suite must match [a-z0-9-]+"}
	}

	// Clear any prior run's lingering unit state so a name collision doesn't
	// reject the new run and journalctl --since scoping stays clean.
	_ = execCommandContext(ctx, "systemctl", "reset-failed", nspawnBuildUnit).Run()

	args := []string{"--unit=" + nspawnBuildUnit, "--no-block", repairBinary, "nspawn", "build",
		"--codename", p.Codename, "--version", p.Version, "--snapshot", p.Snapshot}
	if p.Suite != "" {
		args = append(args, "--suite", p.Suite)
	}
	if p.Includes != "" {
		args = append(args, "--includes", p.Includes)
	}
	startedAt := time.Now().UTC()
	if out, err := execCommandContext(ctx, "systemd-run", args...).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemd-run: %v: %s", err, string(out))}
	}
	return nspawnBuildResponse{Unit: nspawnBuildUnit, StartedAt: startedAt.Format(time.RFC3339Nano)}, nil
}

type nspawnPruneParams struct {
	// Apply=false is a dry-run (list removable images); Apply=true removes them.
	Apply bool `json:"apply"`
}

type nspawnPruneResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

// systemNspawnPruneHandler runs `jabali nspawn prune [--yes]` synchronously
// (quick — it stats image dirs against the pinned set). Dry-run by default.
func systemNspawnPruneHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p nspawnPruneParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
		}
	}
	args := []string{"nspawn", "prune"}
	if p.Apply {
		args = append(args, "--yes")
	}
	out, err := execCommandContext(ctx, repairBinary, args...).CombinedOutput()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("run nspawn prune: %v: %s", err, string(out))}
		}
	}
	return nspawnPruneResponse{Output: string(out), ExitCode: exitCode}, nil
}

func init() {
	Default.Register("system.nspawn_build", systemNspawnBuildHandler)
	Default.Register("system.nspawn_prune", systemNspawnPruneHandler)
}
