package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// app.python.install_runtime — installs the Python app runtime prerequisites
// (python3-venv + dev + build toolchain) by sourcing install.sh and running
// install_python_apps_runtime, same shape as docker.install. Wrapped in
// systemd-run --pipe so apt/dpkg escape the agent's PrivateTmp mount namespace.
// Idempotent (apt-get install is a no-op when present). Dispatched by the
// Server Settings -> Apps toggle on enable (ADR-0131 / GH #203).
func pythonInstallRuntimeHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	if _, err := os.Stat(installShPath); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("install.sh missing at %s", installShPath),
		}
	}
	cmd := execCommandContext(ctx, "systemd-run",
		"--pipe", "--wait", "--quiet", "--collect",
		"--unit=jabali-python-runtime-install",
		"--service-type=oneshot",
		"--", "bash", "-c",
		"source "+installShPath+" && install_python_apps_runtime")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("install_python_apps_runtime failed: %v: %s", err, strings.TrimSpace(string(out))),
		}
	}
	return pythonRuntimeStatusHandler(ctx, nil)
}

// app.python.runtime_status — reports whether the runtime prerequisites are
// present, so the Apps-tab card can drop its installing spinner.
func pythonRuntimeStatusHandler(_ context.Context, _ json.RawMessage) (any, error) {
	venvOK := false
	if out, err := execCommand("python3", "-c", "import venv").CombinedOutput(); err == nil {
		_ = out
		venvOK = true
	}
	gccOK := false
	if _, err := exec.LookPath("gcc"); err == nil {
		gccOK = true
	}
	return map[string]any{"installed": venvOK && gccOK, "venv": venvOK, "toolchain": gccOK}, nil
}

func init() {
	Default.Register("app.python.install_runtime", pythonInstallRuntimeHandler)
	Default.Register("app.python.runtime_status", pythonRuntimeStatusHandler)
}
