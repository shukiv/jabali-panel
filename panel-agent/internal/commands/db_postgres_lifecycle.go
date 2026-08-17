package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// db.postgres.{install,uninstall,enable,disable,status}
//
// Lifecycle commands for the opt-in PostgreSQL feature (M37 Phase 4).
// install.sh no longer runs install_postgres on fresh installs — the
// operator toggles server_settings.postgres_enabled in the panel's
// Databases tab, panel-api dispatches db.postgres.install, this
// handler sources install.sh and runs install_postgres on demand.
//
// install   → apt install + drop-in conf + jabali user enrolled in
//             postgres group + service ENABLED + STARTED.
// uninstall → apt purge -y postgresql* (DESTRUCTIVE — drops
//             /var/lib/postgresql).
// enable    → systemctl enable --now postgresql (no-op if absent).
// disable   → systemctl disable --now postgresql (data preserved).
// status    → reports installed + active + version.

const installShPath = "/opt/jabali-panel/install.sh"

type postgresStatusResponse struct {
	Installed bool   `json:"installed"`
	Active    bool   `json:"active"`
	Version   string `json:"version,omitempty"`
}

func dbPostgresStatusHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	resp := postgresStatusResponse{}
	if _, err := exec.LookPath("psql"); err == nil {
		resp.Installed = true
	} else if matches, _ := filepath.Glob("/usr/lib/postgresql/*/bin/psql"); len(matches) > 0 {
		resp.Installed = true
	}
	if out, err := execCommandContext(ctx, "systemctl", "is-active", "postgresql").Output(); err == nil {
		resp.Active = strings.TrimSpace(string(out)) == "active"
	}
	if resp.Installed {
		if out, err := execCommandContext(ctx, "psql", "--version").Output(); err == nil {
			resp.Version = strings.TrimSpace(string(out))
		}
	}
	return resp, nil
}

// dbPostgresInstallHandler sources install.sh and runs install_postgres.
// Idempotent: install_postgres no-ops on re-run when the binary is
// present. Final step explicitly enables + starts the service so the
// reconciler doesn't have to.
func dbPostgresInstallHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	if _, err := os.Stat(installShPath); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("install.sh missing at %s", installShPath),
		}
	}
	// Wrap in `systemd-run --scope` so the install runs OUTSIDE the
	// jabali-agent.service mount namespace. The agent's namespace
	// inherits PrivateTmp + ProtectKernel* hardenings that prevent
	// `install -d /run/postgresql` from succeeding (the directory
	// either fails to materialise or fails to chmod due to the
	// remount restrictions). systemd-run --scope detaches the
	// child into a fresh transient unit attached to the calling
	// session, so apt + dpkg + postinst all run as if invoked by
	// a plain root shell.
	// systemd-run --pipe runs a transient service (NOT --scope —
	// scope inherits the caller's mount namespace, the very thing
	// we're trying to escape). --pipe forwards stdout/stderr back
	// to us and waits for completion. --wait makes the call
	// synchronous; --collect garbage-collects the unit on exit.
	cmd := execCommandContext(ctx, "systemd-run",
		"--pipe", "--wait", "--quiet", "--collect",
		"--unit=jabali-pg-install",
		"--service-type=oneshot",
		"--", "bash", "-c",
		"source "+installShPath+" && install_postgres && "+
			"systemctl enable postgresql >/dev/null 2>&1 && "+
			"systemctl start postgresql")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("install_postgres failed: %v: %s",
				err, strings.TrimSpace(string(out))),
		}
	}
	return dbPostgresStatusHandler(ctx, nil)
}

// dbPostgresEnableHandler starts + enables postgresql.service.
func dbPostgresEnableHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	if _, err := exec.LookPath("psql"); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: "postgres not installed; call db.postgres.install first",
		}
	}
	if out, err := execCommandContext(ctx, "systemctl", "enable", "--now", "postgresql").CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl enable --now postgresql: %v: %s",
				err, strings.TrimSpace(string(out))),
		}
	}
	return dbPostgresStatusHandler(ctx, nil)
}

// dbPostgresDisableHandler stops + disables (data PRESERVED).
func dbPostgresDisableHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	if out, err := execCommandContext(ctx, "systemctl", "disable", "--now", "postgresql").CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl disable --now postgresql: %v: %s",
				err, strings.TrimSpace(string(out))),
		}
	}
	return dbPostgresStatusHandler(ctx, nil)
}

// dbPostgresUninstallHandler is DESTRUCTIVE: apt purge + rm -rf data.
// Caller is expected to have shown a confirmation dialog UI-side.
func dbPostgresUninstallHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	steps := [][]string{
		{"systemctl", "disable", "--now", "postgresql"},
		// "postgresql*" purges every installed postgres major in one
		// shot (15, 16, 17 — whichever the host shipped). Wrapped
		// in systemd-run --scope so apt + dpkg + postrm escape the
		// jabali-agent mount namespace (same reason install does).
		// Intentionally NOT followed by `apt-get autoremove`:
		// autoremove can pick up unrelated stale kernel images and
		// fail mid-removal, which then blocks future apt ops on
		// the host.
		{"systemd-run", "--pipe", "--wait", "--quiet", "--collect",
			"--unit=jabali-pg-purge", "--service-type=oneshot",
			"--", "bash", "-c",
			"DEBIAN_FRONTEND=noninteractive apt-get purge -y " +
				"'postgresql*' postgresql-common postgresql-client-common"},
		{"rm", "-rf", "/var/lib/postgresql", "/etc/postgresql",
			"/etc/jabali-panel/postgres.password"},
	}
	for _, args := range steps {
		c := execCommandContext(ctx, args[0], args[1:]...)
		c.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
		if out, err := c.CombinedOutput(); err != nil {
			// Fail-soft on disable when service already absent; hard
			// on apt errors. Distinguish by command name.
			if args[0] == "systemctl" {
				continue
			}
			return nil, &agentwire.AgentError{
				Code: agentwire.CodeInternal,
				Message: fmt.Sprintf("%s %v failed: %v: %s",
					args[0], args[1:], err, strings.TrimSpace(string(out))),
			}
		}
	}
	return dbPostgresStatusHandler(ctx, nil)
}

func init() {
	Default.Register("db.postgres.status", dbPostgresStatusHandler)
	Default.Register("db.postgres.install", dbPostgresInstallHandler)
	Default.Register("db.postgres.enable", dbPostgresEnableHandler)
	Default.Register("db.postgres.disable", dbPostgresDisableHandler)
	Default.Register("db.postgres.uninstall", dbPostgresUninstallHandler)
}
