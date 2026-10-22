package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// The steps every app runtime shares (ADR-0131 Python, ADR-0175 Node.js):
// resolve the app root under the owner's home, and converge the per-app
// EnvironmentFile + systemd unit. One unit shape for every runtime keeps the
// Slice= line, which the egress table's owner-slice listener rule relies on,
// identical across them.

// resolveAppRoot scope-validates appRootIn under the owner's home and creates
// it as the owner when it does not exist yet.
func resolveAppRoot(ctx context.Context, userID, username, appRootIn string) (string, error) {
	homeDir := "/home/" + username
	scope, err := filesafe.NewScope(userID, username, []string{homeDir})
	if err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("scope: %v", err)}
	}
	// app_root may arrive home-relative ("domains/x/app") or absolute
	// ("/home/<user>/domains/x/app"); filesafe.Resolve requires absolute.
	if !filepath.IsAbs(appRootIn) {
		appRootIn = filepath.Join(homeDir, appRootIn)
	}
	appRoot, err := scope.Resolve(appRootIn)
	if err != nil {
		return "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("app_root validation failed: %v", err)}
	}
	if fi, err := os.Stat(appRoot); err != nil {
		// Fresh plain app: the dir doesn't exist yet and there is no framework
		// scaffold to create it. Make it AS THE TENANT so the venv + code land
		// tenant-owned, inside the scope-validated path under the owner's home.
		if out, mkerr := runAsUser(ctx, username, "mkdir", "-p", appRoot); mkerr != nil {
			return "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("create app_root: %v: %s", mkerr, out)}
		}
	} else if !fi.IsDir() {
		return "", &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "app_root exists but is not a directory"}
	}
	return appRoot, nil
}

// appUnitSpec is what renderAppUnit needs from any runtime's apply params.
type appUnitSpec struct {
	Runtime     string // shown in Description=, e.g. "Python", "Node.js"
	AppID       string
	Username    string
	AppRoot     string
	EnvPath     string
	Start       string
	MemoryLimit string
	CPULimit    string
	PIDsLimit   int
}

func renderAppUnit(u appUnitSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=Jabali %s app %s\nAfter=network.target\n\n", u.Runtime, u.AppID)
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "User=%s\nGroup=%s\n", u.Username, u.Username)
	// Place the app inside the owner's M18 user slice (ADR-0131, Gitea #490) so
	// the package's cgroup CPU/memory/PID limits apply to it via the slice,
	// independent of the optional per-app caps below. systemd nests it under
	// jabali.slice/jabali-user.slice automatically via the slice unit.
	fmt.Fprintf(&b, "Slice=jabali-user-%s.slice\n", u.Username)
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", u.AppRoot)
	fmt.Fprintf(&b, "EnvironmentFile=%s\n", u.EnvPath)
	fmt.Fprintf(&b, "ExecStart=%s\n", u.Start)
	b.WriteString("Restart=on-failure\nRestartSec=3\nNoNewPrivileges=true\nPrivateTmp=true\n")
	// No system D-Bus: tenant code has no use for systemd, logind or any other
	// system service on it (tenant PHP is denied it by the jabali-fpm-app
	// AppArmor profile). "-" keeps the unit starting where the bus is absent.
	b.WriteString("InaccessiblePaths=-/run/dbus/system_bus_socket\n")
	if u.MemoryLimit != "" {
		fmt.Fprintf(&b, "MemoryMax=%s\n", u.MemoryLimit)
	}
	if u.CPULimit != "" {
		fmt.Fprintf(&b, "CPUQuota=%s\n", u.CPULimit)
	}
	if u.PIDsLimit > 0 {
		fmt.Fprintf(&b, "TasksMax=%d\n", u.PIDsLimit)
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

// applyAppUnit writes the app's EnvironmentFile and unit, reloads systemd when
// the unit changed, enables the unit and restarts it when anything changed,
// then reports whether it is active.
func applyAppUnit(ctx context.Context, appID, envPath, envContent, unitContent string, needsRestart bool) (pythonAppApplyResult, error) {
	// EnvironmentFile (root-owned 0640; values never logged). ALWAYS write it
	// so the file the unit references exists — renderEnvFile is empty for a
	// minimal ASGI app with no env vars, and systemd hard-fails the unit
	// ("Failed to load environment files: No such file or directory") when the
	// EnvironmentFile is missing. A content change OR a previously-absent file
	// counts as a restart trigger (the latter recovers an app broken by that
	// missing file); an unchanged, present env is a no-op restart-wise.
	if prev, rerr := os.ReadFile(envPath); os.IsNotExist(rerr) || string(prev) != envContent {
		needsRestart = true
	}
	if err := os.WriteFile(envPath, []byte(envContent), 0o640); err != nil {
		return pythonAppApplyResult{}, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write env file: %v", err)}
	}

	// systemd unit — always write so it exists; a changed (or absent) unit
	// also needs a daemon-reload before restart.
	unit := pythonAppUnitName(appID)
	unitPath := filepath.Join(pythonAppUnitDir, unit)
	unitChanged := false
	if prev, rerr := os.ReadFile(unitPath); os.IsNotExist(rerr) || string(prev) != unitContent {
		unitChanged = true
		needsRestart = true
	}
	if err := os.WriteFile(unitPath, []byte(unitContent), 0o644); err != nil {
		return pythonAppApplyResult{}, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write unit: %v", err)}
	}

	// reload (only on unit change) + enable (idempotent) + restart (only when
	// something changed). When nothing changed we leave the running process
	// alone — systemd's Restart=on-failure owns crash recovery, and the Restart
	// button drives a separate app.python.control call.
	if unitChanged {
		_ = execCommandContext(ctx, "systemctl", "daemon-reload").Run()
	}
	_ = execCommandContext(ctx, "systemctl", "enable", "--quiet", unit).Run()
	if needsRestart {
		if out, err := execCommandContext(ctx, "systemctl", "restart", unit).CombinedOutput(); err != nil {
			return pythonAppApplyResult{}, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("restart %s: %v: %s%s", unit, err, strings.TrimSpace(string(out)), tasksLimitSuffix(ctx, unit))}
		}
	}

	active := execCommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
	res := pythonAppApplyResult{Active: active, Unit: unit, Restarted: needsRestart}
	if !active {
		// Capture WHY it isn't active so the panel shows the real startup error
		// instead of only "not active — check journalctl" (GH #357).
		// --output=cat drops syslog metadata; last 15 lines keep it bounded.
		if out, jerr := execCommandContext(ctx, "journalctl", "-u", unit, "-n", "20", "--no-pager", "--output=cat").CombinedOutput(); jerr == nil {
			res.Detail = lastLines(strings.TrimSpace(string(out)), 15)
		}
		res.Hint = tasksLimitHint(ctx, unit)
	}
	return res, nil
}
