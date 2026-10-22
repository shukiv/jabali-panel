package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// appBuildStepTimeout caps one build command (a pip install, a manage.py
// step, npm ci).
const appBuildStepTimeout = 8 * time.Minute

// runAsUserInSlice runs an app's build command as the user, in the user's
// slice, so the package's CPU, memory and task limits apply to the build as
// they do to the app. The command gets its own transient scope,
// jabali-app-build-<appID>-<n>.scope in jabali-user-<user>.slice, in which
// sudo drops to the user; with dir set, the user's own process changes into
// it (env -C). Nothing the command starts outlives it: the scope is stopped
// when the command ends, is cancelled or times out.
func runAsUserInSlice(ctx context.Context, appID, username, dir string, args ...string) (string, error) {
	scope := fmt.Sprintf("jabali-app-build-%s-%d.scope", appID, time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(ctx, appBuildStepTimeout)
	defer cancel()
	full := []string{
		"--scope", "--quiet", "--collect",
		"--slice=jabali-user-" + username + ".slice",
		"--unit=" + scope,
		"--property=TimeoutStopSec=5",
		"--", "sudo", "-u", username, "-H",
	}
	if dir != "" {
		full = append(full, envBin, "-C", dir, "--")
	}
	full = append(full, args...)
	cmd := execCommandContext(ctx, "systemd-run", full...)
	// A process the command left behind, or the build's own once a cancel or
	// timeout killed sudo, keeps its output open: wait for it at most 10s,
	// then stopping the scope below ends it.
	cmd.WaitDelay = 10 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	stopUnit(scope)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out.String(), fmt.Errorf("timed out")
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return out.String(), err
}

// stopAppBuildScopes ends every build command of an app that is still
// running (app.python.remove).
func stopAppBuildScopes(appID string) {
	stopUnit("jabali-app-build-" + appID + "-*.scope")
}

// stopUnit stops a unit, or the loaded units a glob matches. A build scope is
// created with TimeoutStopSec=5, so this returns within seconds.
func stopUnit(unit string) {
	_ = execCommandContext(context.Background(), "systemctl", "stop", unit).Run()
}
