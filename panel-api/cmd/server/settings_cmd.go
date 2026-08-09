// `jabali settings get|set` (Gitea #539): a root-only CLI surface for
// inspecting and patching server settings without a browser session or raw
// SQL. Validation reuses api.ValidateServerSettings — the exact rules the REST
// PATCH applies. Side effects (postgres/docker/python installs, nginx reload)
// are dispatched SYNCHRONOUSLY here (unlike the REST handler's background
// goroutines) so the operator sees success/failure inline; the agent's own
// `nginx -t` gate is the second validation layer for nginx tunables. The
// side-effect dispatch is duplicated from the REST handler, not shared — the
// handler's gin/goroutine coupling makes extraction impractical, so this is a
// known drift point. Mutations are CLI-audited as actor_kind=cli (#537).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newSettingsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Inspect and patch server settings (headless equivalent of /admin/settings)",
	}
	cmd.AddCommand(newSettingsGetCmd(), newSettingsSetCmd(), newSettingsKeysCmd())
	return cmd
}

func newSettingsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get",
		Short:   "Print current server settings (JSON or key=value)",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			s, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx)
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}
			if jsonOutput {
				return printJSON(s) // honours json:"-" — secrets never printed
			}
			m, err := settingsToMap(s)
			if err != nil {
				return err
			}
			keys := make([]string, 0, len(m))
			for k := range m {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(cmd.OutOrStdout(), "%s=%v\n", k, m[k])
			}
			return nil
		},
	}
}

func newSettingsKeysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keys",
		Short: "List the settable keys for `settings set`",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			keys := make([]string, 0, len(settableKeys))
			for k := range settableKeys {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", k, settableKeys[k].help)
			}
			return nil
		},
	}
}

func newSettingsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key>=<value> [<key>=<value>...]",
		Short: "Patch one or more server settings (same validation + side effects as the UI)",
		Long: "Patch server settings. Unknown keys and invalid values are rejected. " +
			"Settings that drive host changes (postgres/docker/python installs, nginx " +
			"reload) apply synchronously and may take several minutes. Run `jabali " +
			"settings keys` for the full list of settable keys.",
		Args:    cobra.MinimumNArgs(1),
		PreRunE: requireDBAndAgent,
		RunE:    runSettingsSet,
	}
}

func runSettingsSet(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	repo := repository.NewServerSettingsRepository(sharedDB)

	loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	s, err := repo.Get(loadCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}

	// Snapshot the fields whose change drives a host side effect.
	prev := sideEffectSnapshot(s)

	// Apply each key=value. Unknown key / bad value → hard error (criterion #4).
	for _, arg := range args {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			return fmt.Errorf("argument %q is not key=value", arg)
		}
		k = strings.TrimSpace(k)
		def, known := settableKeys[k]
		if !known {
			return fmt.Errorf("unknown setting key %q (run `jabali settings keys`)", k)
		}
		if err := def.apply(s, v); err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}

	// Same validation the REST PATCH runs.
	if err := api.ValidateServerSettings(s); err != nil {
		return fmt.Errorf("invalid settings: %w", err)
	}

	// REST precondition: tenant Docker requires the marketplace engine.
	if s.DockerAppsForUsersEnabled && !prev.dockerForUsers && !s.DockerMarketplaceEnabled {
		return fmt.Errorf("docker_apps_for_users_enabled requires docker_marketplace_enabled (enable the Docker marketplace first)")
	}

	if err := repo.Upsert(ctx, s); err != nil {
		return fmt.Errorf("persist settings: %w", err)
	}

	// Side effects, synchronous, in the REST order. A failure here means the DB
	// is updated but the host apply failed — report it explicitly (no silent
	// drift) and exit non-zero.
	if applyErr := applySettingsSideEffects(ctx, cmd, repo, s, prev); applyErr != nil {
		cliAuditErr(ctx, "server_settings.update", "server_settings", "1", nil)
		return applyErr
	}

	cliAuditOK(ctx, "server_settings.update", "server_settings", "1", nil)
	if jsonOutput {
		return printJSON(s)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Settings updated.")
	return nil
}

// sideEffectState captures the pre-change values that decide which host side
// effects fire (mirrors the REST handler's prev* locals).
type sideEffectState struct {
	hostname       string
	postgres       bool
	dockerMarket   bool
	dockerForUsers bool
	python         bool
}

func sideEffectSnapshot(s *models.ServerSettings) sideEffectState {
	return sideEffectState{
		hostname:       s.Hostname,
		postgres:       s.PostgresEnabled,
		dockerMarket:   s.DockerMarketplaceEnabled,
		dockerForUsers: s.DockerAppsForUsersEnabled,
		python:         s.PythonAppsEnabled,
	}
}

// applySettingsSideEffects dispatches the agent calls the REST PATCH would,
// synchronously, with the REST per-operation timeouts. Returns the first
// failure (the DB is already persisted at this point).
func applySettingsSideEffects(ctx context.Context, cmd *cobra.Command, repo repository.ServerSettingsRepository, s *models.ServerSettings, prev sideEffectState) error {
	out := cmd.OutOrStdout()

	// Hostname → apply to the OS. Mirrors the REST PATCH, which dispatches
	// system.set_hostname when Hostname changes (there in a background
	// goroutine; here synchronous like the rest of this command so the operator
	// sees the result inline). The panel-cert reconciler reissues the panel
	// certificate for the new name on its next pass — same as the UI flow.
	if s.Hostname != prev.hostname {
		fmt.Fprintf(out, "Applying hostname change (system.set_hostname %s)…\n", s.Hostname)
		if err := callAgentTimeout(ctx, 30*time.Second, "system.set_hostname", map[string]any{"hostname": s.Hostname}); err != nil {
			return fmt.Errorf("DB updated, but system.set_hostname failed: %w", err)
		}
	}

	if s.PostgresEnabled != prev.postgres {
		method := "db.postgres.disable"
		if s.PostgresEnabled {
			method = "db.postgres.install"
		}
		fmt.Fprintf(out, "Applying Postgres change (%s) — this may take several minutes…\n", method)
		if err := callAgentTimeout(ctx, 5*time.Minute, method, map[string]any{}); err != nil {
			return fmt.Errorf("DB updated, but %s failed: %w", method, err)
		}
	}

	if s.DockerMarketplaceEnabled != prev.dockerMarket {
		method := "docker.disable"
		if s.DockerMarketplaceEnabled {
			method = "docker.install"
		}
		fmt.Fprintf(out, "Applying Docker marketplace change (%s) — this may take several minutes…\n", method)
		if err := callAgentTimeout(ctx, 10*time.Minute, method, map[string]any{}); err != nil {
			return fmt.Errorf("DB updated, but %s failed: %w", method, err)
		}
	}

	if s.DockerAppsForUsersEnabled != prev.dockerForUsers {
		fmt.Fprintf(out, "Applying tenant Docker change (docker.tenant_set enabled=%v) — this may take several minutes…\n", s.DockerAppsForUsersEnabled)
		if err := callAgentTimeout(ctx, 12*time.Minute, "docker.tenant_set", map[string]any{"enabled": s.DockerAppsForUsersEnabled}); err != nil {
			// On a failed ENABLE, revert the flag so the UI/DB reflects that the
			// host setup did not happen (mirrors the REST revert).
			if s.DockerAppsForUsersEnabled {
				rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
				if cur, gerr := repo.Get(rctx); gerr == nil && cur != nil {
					cur.DockerAppsForUsersEnabled = false
					_ = repo.Upsert(rctx, cur)
				}
				rcancel()
			}
			return fmt.Errorf("DB updated, but docker.tenant_set failed (flag reverted): %w", err)
		}
	}

	// Python: install runtime on enable only (disable leaves packages).
	if s.PythonAppsEnabled != prev.python && s.PythonAppsEnabled {
		fmt.Fprintln(out, "Installing Python app runtime — this may take several minutes…")
		if err := callAgentTimeout(ctx, 10*time.Minute, "app.python.install_runtime", map[string]any{}); err != nil {
			return fmt.Errorf("DB updated, but app.python.install_runtime failed: %w", err)
		}
	}

	if nginxDirty {
		fmt.Fprintln(out, "Applying nginx tunables (validated with nginx -t)…")
		if err := callAgentTimeout(ctx, 60*time.Second, "nginx.tunables.apply", map[string]any{
			"client_max_body_size":  s.NginxClientMaxBodySize,
			"keepalive_timeout":     s.NginxKeepaliveTimeout,
			"server_tokens":         s.NginxServerTokens,
			"gzip":                  s.NginxGzip,
			"client_body_timeout":   s.NginxClientBodyTimeout,
			"client_header_timeout": s.NginxClientHeaderTimeout,
			"send_timeout":          s.NginxSendTimeout,
			"proxy_connect_timeout": s.NginxProxyConnectTimeout,
			"proxy_read_timeout":    s.NginxProxyReadTimeout,
			"proxy_send_timeout":    s.NginxProxySendTimeout,
			"worker_processes":      s.NginxWorkerProcesses,
			"worker_connections":    s.NginxWorkerConnections,
			"custom_http":           s.NginxCustomHTTP,
		}); err != nil {
			return fmt.Errorf("DB updated, but nginx.tunables.apply failed: %w", err)
		}
	}
	return nil
}

func callAgentTimeout(ctx context.Context, d time.Duration, method string, params map[string]any) error {
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	_, err := sharedAgent.Call(cctx, method, params)
	return err
}

// nginxDirty is set by any nginx-key setter so the nginx side effect fires.
// Reset is unnecessary: each CLI process applies a single `set`.
var nginxDirty bool

// settingDef is one settable key: a human help string and a parser/setter.
type settingDef struct {
	help  string
	apply func(s *models.ServerSettings, raw string) error
}

func parseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on", "enable", "enabled":
		return true, nil
	case "false", "0", "no", "off", "disable", "disabled":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q (use true/false)", raw)
}

func boolKey(help string, set func(*models.ServerSettings, bool)) settingDef {
	return settingDef{help: help, apply: func(s *models.ServerSettings, raw string) error {
		b, err := parseBool(raw)
		if err != nil {
			return err
		}
		set(s, b)
		return nil
	}}
}

func strKey(help string, set func(*models.ServerSettings, string)) settingDef {
	return settingDef{help: help, apply: func(s *models.ServerSettings, raw string) error {
		set(s, strings.TrimSpace(raw))
		return nil
	}}
}

func nginxStrKey(help string, set func(*models.ServerSettings, string)) settingDef {
	return settingDef{help: help, apply: func(s *models.ServerSettings, raw string) error {
		set(s, strings.TrimSpace(raw))
		nginxDirty = true
		return nil
	}}
}

func nginxBoolKey(help string, set func(*models.ServerSettings, bool)) settingDef {
	return settingDef{help: help, apply: func(s *models.ServerSettings, raw string) error {
		b, err := parseBool(raw)
		if err != nil {
			return err
		}
		set(s, b)
		nginxDirty = true
		return nil
	}}
}

// settableKeys is the registry. Intentionally a SUBSET of the full model —
// the keys with clear CLI semantics + the acceptance-criteria toggles/tunables.
// It is NOT full-model coverage (branding/colors/VAPID/etc. stay UI-only).
var settableKeys = map[string]settingDef{
	// hostname: the one field that previously had NO CLI writer, so the
	// free-hostname (JAB-213) conversion couldn't be driven headlessly — the
	// panel only serves on a name once its stored hostname is that name, and
	// that write lived only behind the settings PATCH / free-hostname claim
	// endpoints (admin session). Setting it here runs the same OS apply
	// (system.set_hostname) the REST PATCH fires; the panel-cert reconciler then
	// reissues the panel cert for the new name, exactly as the UI path does.
	// FQDN-format validation is enforced by api.ValidateServerSettings below.
	"hostname": settingDef{
		help: "panel hostname / FQDN (applies to the OS via system.set_hostname; the panel-cert reconciler reissues the cert)",
		apply: func(s *models.ServerSettings, raw string) error {
			v := strings.TrimSpace(raw)
			if v == "" {
				return fmt.Errorf("hostname cannot be empty")
			}
			s.Hostname = v
			return nil
		},
	},
	"postgres_enabled":              boolKey("enable/disable Postgres (installs/stops the engine)", func(s *models.ServerSettings, b bool) { s.PostgresEnabled = b }),
	"webmail_enabled":               boolKey("enable/disable webmail (DB flag)", func(s *models.ServerSettings, b bool) { s.WebmailEnabled = b }),
	"docker_marketplace_enabled":    boolKey("enable/disable the Docker marketplace (installs/stops Docker)", func(s *models.ServerSettings, b bool) { s.DockerMarketplaceEnabled = b }),
	"docker_apps_for_users_enabled": boolKey("enable/disable tenant Docker apps (runs host tenant setup)", func(s *models.ServerSettings, b bool) { s.DockerAppsForUsersEnabled = b }),
	"python_apps_enabled":           boolKey("enable/disable Python apps (installs the runtime on enable)", func(s *models.ServerSettings, b bool) { s.PythonAppsEnabled = b }),
	"tenant_domain_options_enabled": boolKey("expose per-domain options to tenants (DB flag)", func(s *models.ServerSettings, b bool) { s.TenantDomainOptionsEnabled = b }),
	"release_channel": settingDef{
		help: "release channel: stable|development (GUI Updates page shares this contract)",
		apply: func(s *models.ServerSettings, raw string) error {
			v := strings.TrimSpace(raw)
			if v != "stable" && v != "development" {
				return fmt.Errorf("must be stable|development")
			}
			s.ReleaseChannel = v
			return nil
		},
	},
	"dns_user_record_policy_preset": settingDef{
		help: "set the DNS user-record policy from a named preset (e.g. hosting-safe, locked-down)",
		apply: func(s *models.ServerSettings, raw string) error {
			pol, ok := models.DNSPolicyPreset(strings.TrimSpace(raw))
			if !ok {
				return fmt.Errorf("unknown DNS policy preset %q", raw)
			}
			s.DNSUserRecordPolicy = pol
			return nil
		},
	},
	// nginx tunables (criterion #3) — validated by api.ValidateServerSettings.
	"nginx_client_max_body_size":  nginxStrKey("nginx client_max_body_size (e.g. 50m)", func(s *models.ServerSettings, v string) { s.NginxClientMaxBodySize = v }),
	"nginx_keepalive_timeout":     nginxStrKey("nginx keepalive_timeout (e.g. 65s)", func(s *models.ServerSettings, v string) { s.NginxKeepaliveTimeout = v }),
	"nginx_server_tokens":         nginxBoolKey("nginx server_tokens on/off", func(s *models.ServerSettings, b bool) { s.NginxServerTokens = b }),
	"nginx_gzip":                  nginxBoolKey("nginx gzip on/off", func(s *models.ServerSettings, b bool) { s.NginxGzip = b }),
	"nginx_client_body_timeout":   nginxStrKey("nginx client_body_timeout (e.g. 60s)", func(s *models.ServerSettings, v string) { s.NginxClientBodyTimeout = v }),
	"nginx_client_header_timeout": nginxStrKey("nginx client_header_timeout (e.g. 60s)", func(s *models.ServerSettings, v string) { s.NginxClientHeaderTimeout = v }),
	"nginx_send_timeout":          nginxStrKey("nginx send_timeout (e.g. 60s)", func(s *models.ServerSettings, v string) { s.NginxSendTimeout = v }),
	"nginx_proxy_connect_timeout": nginxStrKey("nginx proxy_connect_timeout (e.g. 60s)", func(s *models.ServerSettings, v string) { s.NginxProxyConnectTimeout = v }),
	"nginx_proxy_read_timeout":    nginxStrKey("nginx proxy_read_timeout (e.g. 60s)", func(s *models.ServerSettings, v string) { s.NginxProxyReadTimeout = v }),
	"nginx_proxy_send_timeout":    nginxStrKey("nginx proxy_send_timeout (e.g. 60s)", func(s *models.ServerSettings, v string) { s.NginxProxySendTimeout = v }),
	"nginx_worker_processes":      nginxStrKey("nginx worker_processes ('auto' or 1-99)", func(s *models.ServerSettings, v string) { s.NginxWorkerProcesses = v }),
	"nginx_custom_http":           nginxStrKey("nginx custom http{} snippet (<=4000 chars)", func(s *models.ServerSettings, v string) { s.NginxCustomHTTP = v }),
	"nginx_worker_connections": settingDef{
		help: "nginx worker_connections (<=1048576)",
		apply: func(s *models.ServerSettings, raw string) error {
			n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 32)
			if err != nil {
				return fmt.Errorf("invalid uint %q", raw)
			}
			s.NginxWorkerConnections = uint32(n)
			nginxDirty = true
			return nil
		},
	},
}

// settingsToMap renders settings as a flat key→value map via JSON, so the
// table form inherits the model's json tags (json:"-" secrets stay hidden).
func settingsToMap(s *models.ServerSettings) (map[string]any, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode settings: %w", err)
	}
	return m, nil
}
