package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"text/tabwriter"
	"time"

	"strings"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func dockerAppRepoFromDB() repository.DockerAppRepository {
	return repository.NewDockerAppRepository(sharedDB)
}

func loadDockerCatalogForCLI() (*dockerapp.Catalog, error) {
	cat, errs := dockerapp.LoadDir("/usr/local/share/jabali/docker-apps")
	if cat.Len() == 0 {
		if dev, _ := dockerapp.LoadDir("install/docker-apps"); dev.Len() > 0 {
			cat = dev
		}
	}
	if cat.Len() == 0 {
		if len(errs) > 0 {
			return nil, fmt.Errorf("catalog empty (first error: %s)", errs[0].Error())
		}
		return nil, errors.New("catalog empty: /usr/local/share/jabali/docker-apps unreadable")
	}
	return cat, nil
}

func newDockerAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docker-app",
		Short: "Manage M48 docker-app catalog installs (admin-only)",
	}
	cmd.AddCommand(
		newDockerAppCatalogCmd(),
		newDockerAppInstallCmd(),
		newDockerAppListCmd(),
		newDockerAppStatusCmd(),
		newDockerAppLifecycleCmd("start", "Start a stopped install"),
		newDockerAppLifecycleCmd("stop", "Stop a running install"),
		newDockerAppLifecycleCmd("restart", "Restart an install"),
		newDockerAppLifecycleCmd("rebuild", "Force-recreate (docker compose up --force-recreate)"),
		newDockerAppDeleteCmd(),
		newDockerAppLogsCmd(),
		newDockerAppUpdateCmd(),
		newDockerAppBackupsCmd(),
		newDockerAppEnvCmd(),
		newDockerAppExecCmd(),
		newDockerAppBackupCreateCmd(),
		newDockerAppRestoreCmd(),
		newDockerAppMaintenanceCmd(),
		newDockerAppEditCmd(),
		newDockerAppPatchCmd(),
	)
	return cmd
}

func newDockerAppCatalogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "List entries in the installed catalog",
		RunE: func(cmd *cobra.Command, args []string) error {
			cat, err := loadDockerCatalogForCLI()
			if err != nil {
				return err
			}
			entries := cat.All()
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(entries)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SLUG\tNAME\tVERSION\tIMAGE")
			for _, e := range entries {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Slug, e.Name, e.Version, e.ImageChannel)
			}
			return tw.Flush()
		},
	}
	return cmd
}

// newDockerAppInstallCmd mints a docker_app row + ports + dispatches
// docker_app.install to the agent. Mirrors panel-api/internal/api/
// docker_apps.go install() but with two intentional simplifications:
//
//  1. No automatic domains-row creation. The CLI doesn't know which
//     user owns the row; the operator can wire a domain via
//     `jabali domain ...` after the install settles.
//  2. Catalog default ports only. Fine-grained per-port overrides
//     (host_port, bind, reverse_proxy) belong in the install drawer
//     — adding flags here would balloon UX without much win.
//
// Resource limits, update-mode, and env overrides ARE wired so a
// scripted ops install can pin the install to specific values.
func newDockerAppInstallCmd() *cobra.Command {
	var (
		name        string
		updateMode  string
		cpuLimit    string
		memoryLimit string
		pidsLimit   int
		envPairs    []string
		tenantUser  string
		tenantDom   string
	)
	cmd := &cobra.Command{
		Use:     "install <slug>",
		Short:   "Install a catalog entry (creates the docker_apps row + dispatches the agent)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			slug := args[0]
			if tenantUser != "" {
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
				defer cancel()
				return runTenantDockerInstall(ctx, slug, name, tenantUser, tenantDom, updateMode, cpuLimit, memoryLimit, pidsLimit, envPairs)
			}
			if !nameRE.MatchString(name) {
				return fmt.Errorf("invalid --name %q (must match ^[a-z0-9-]{1,32}$)", name)
			}
			if updateMode == "" {
				updateMode = models.DockerAppUpdateModeManual
			}
			if updateMode != models.DockerAppUpdateModeManual && updateMode != models.DockerAppUpdateModeAuto {
				return fmt.Errorf("invalid --update-mode %q (manual|auto)", updateMode)
			}

			cat, err := loadDockerCatalogForCLI()
			if err != nil {
				return err
			}
			entry, ok := cat.Get(slug)
			if !ok {
				return fmt.Errorf("slug %q not in catalog", slug)
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			repo := dockerAppRepoFromDB()

			if existing, _ := repo.FindBySlugName(ctx, slug, name); existing != nil {
				return fmt.Errorf("already installed: id=%s status=%s", existing.ID, existing.Status)
			}

			// Apply catalog defaults when caller omitted limits.
			cpu := cpuLimit
			if cpu == "" && entry.Resources.CPU != "" {
				cpu = entry.Resources.CPU
			}
			mem := memoryLimit
			if mem == "" && entry.Resources.Memory != "" {
				mem = entry.Resources.Memory
			}
			pids := pidsLimit
			if pids == 0 && entry.Resources.PIDs > 0 {
				pids = entry.Resources.PIDs
			}

			app := &models.DockerApp{
				ID:             ulid.Make().String(),
				Slug:           slug,
				Name:           name,
				CatalogVersion: entry.Version,
				InstanceSlug:   entry.Slug,
				Status:         models.DockerAppStatusPending,
				UpdateMode:     updateMode,
				CPULimit:       nilIfEmpty(cpu),
				MemoryLimit:    nilIfEmpty(mem),
			}
			if pids > 0 {
				p := pids
				app.PIDsLimit = &p
			}
			if err := repo.Create(ctx, app); err != nil {
				return fmt.Errorf("persist docker_apps row: %w", err)
			}

			// Resolve catalog-default ports + persist them.
			rows := make([]*models.DockerAppPublishedPort, 0, len(entry.Ports))
			runtime := make(map[string]dockerapp.RuntimePort, len(entry.Ports))
			for _, p := range entry.Ports {
				if !p.DefaultEnabled {
					continue
				}
				bind := p.DefaultBind
				if bind == "" {
					bind = "loopback"
				}
				host, err := repo.FindFreeHostPort(ctx, bind, p.Protocol)
				if err != nil {
					_ = repo.Delete(ctx, app.ID)
					return fmt.Errorf("allocate host port for %q: %w", p.Name, err)
				}
				row := &models.DockerAppPublishedPort{
					ID:            ulid.Make().String(),
					AppID:         app.ID,
					PortName:      p.Name,
					ContainerPort: p.ContainerPort,
					BindInterface: bind,
					HostPort:      host,
					Protocol:      p.Protocol,
					ReverseProxy:  p.DefaultReverseProxy,
					Enabled:       true,
				}
				if err := repo.CreatePort(ctx, row); err != nil {
					_ = repo.Delete(ctx, app.ID)
					return fmt.Errorf("persist port row %q: %w", p.Name, err)
				}
				rows = append(rows, row)
				rip := "127.0.0.1"
				if bind == "public" {
					rip = "0.0.0.0"
				}
				runtime[p.Name] = dockerapp.RuntimePort{
					HostPort:      host,
					ContainerPort: p.ContainerPort,
					BindInterface: rip,
					Protocol:      p.Protocol,
				}
			}

			envOverride := make(map[string]string, len(envPairs))
			for _, kv := range envPairs {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("--env %q must be KEY=VALUE", kv)
				}
				envOverride[k] = v
			}
			envMap, err := dockerapp.MaterialiseEnv(entry, envOverride)
			if err != nil {
				_ = repo.Delete(ctx, app.ID)
				return fmt.Errorf("materialise env: %w", err)
			}

			dataRoot := "/var/lib/jabali/docker-apps/" + entry.Slug
			composeYML, err := dockerapp.Render(entry, dockerapp.RenderParams{
				Slug:         entry.Slug,
				Name:         name,
				ImageChannel: entry.ImageChannel,
				DataRoot:     dataRoot,
				CPULimit:     cpu,
				MemoryLimit:  mem,
				PIDsLimit:    pids,
				Ports:        runtime,
				Env:          envMap,
			})
			if err != nil {
				_ = repo.Delete(ctx, app.ID)
				return fmt.Errorf("render compose: %w", err)
			}

			envFile := buildEnvFileForCLI(envMap)
			volumeNames := make([]string, 0, len(entry.Volumes))
			for _, v := range entry.Volumes {
				volumeNames = append(volumeNames, v.Name)
			}
			_ = repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusInstalling, nil)
			if _, err := sharedAgent.Call(ctx, "docker_app.install", map[string]any{
				"slug":                        entry.Slug,
				"compose_yml":                 composeYML,
				"env_file":                    envFile,
				"volumes":                     volumeNames,
				"volume_owner":                entry.VolumeOwner,
				"wait_healthy":                true,
				"healthcheck_timeout_seconds": 120,
			}); err != nil {
				msg := firstLine(err.Error())
				_ = repo.UpdateStatus(context.Background(), app.ID, models.DockerAppStatusFailed, &msg)
				return fmt.Errorf("agent install: %s", msg)
			}
			_ = repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusRunning, nil)
			cliAuditOK(ctx, "docker_app.install", "docker_app", app.ID, nil)
			fmt.Printf("ok: installed %s (%s) status=running\n", name, app.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "install name (lowercase, ^[a-z0-9-]{1,32}$)")
	cmd.Flags().StringVar(&updateMode, "update-mode", "manual", "manual|auto")
	cmd.Flags().StringVar(&cpuLimit, "cpu", "", "cgroup CPU limit (e.g. 1.0). Catalog default when omitted.")
	cmd.Flags().StringVar(&memoryLimit, "memory", "", "memory limit (e.g. 512m). Catalog default when omitted.")
	cmd.Flags().IntVar(&pidsLimit, "pids", 0, "pids cgroup limit. Catalog default when omitted.")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil, "KEY=VALUE override (repeatable)")
	cmd.Flags().StringVar(&tenantUser, "user", "", "install for this tenant (user id or username); enables the tenant-scoped install path")
	cmd.Flags().StringVar(&tenantDom, "domain", "", "domain the tenant app attaches to (required with --user; must be owned by that user or free)")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func buildEnvFileForCLI(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	var b strings.Builder
	for k, v := range env {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return b.String()
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

var nameRE = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func newDockerAppListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List installed docker apps",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := dockerAppRepoFromDB()
			apps, err := repo.ListAll(ctx)
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(apps)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSLUG\tNAME\tSTATUS\tUPDATE_MODE\tLAST_ERROR")
			for _, a := range apps {
				le := ""
				if a.LastError != nil {
					le = *a.LastError
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					a.ID, a.Slug, a.Name, a.Status, a.UpdateMode, firstLine(le))
			}
			return tw.Flush()
		},
	}
	return cmd
}

func newDockerAppStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status <id>",
		Short:   "Show full status of an installed app (DB row + agent status)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			repo := dockerAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			ports, _ := repo.ListPortsForApp(ctx, app.ID)
			out := map[string]any{
				"app":   app,
				"ports": ports,
			}
			if sharedAgent != nil {
				// JAB-315: agent ops must target EffectiveSlug (instance_slug when
				// set), not the catalog Slug — otherwise a second install of the
				// same catalog app (EffectiveSlug slug-2, slug-3…) is addressed by
				// its base slug and the op hits the WRONG instance.
				raw, agerr := sharedAgent.Call(ctx, "docker_app.status", map[string]any{"slug": app.EffectiveSlug()})
				if agerr != nil {
					out["agent_error"] = agerr.Error()
				} else {
					var agentStatus json.RawMessage = raw
					out["agent"] = agentStatus
				}
			}
			return json.NewEncoder(os.Stdout).Encode(out)
		},
	}
}

func newDockerAppLifecycleCmd(verb, short string) *cobra.Command {
	return &cobra.Command{
		Use:     verb + " <id>",
		Short:   short,
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()
			repo := dockerAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			// JAB-315: target EffectiveSlug so start/stop/restart hit this exact
			// instance, not the base-slug container of a different install.
			raw, err := sharedAgent.Call(ctx, "docker_app."+verb, map[string]any{"slug": app.EffectiveSlug()})
			if err != nil {
				return err
			}
			fmt.Printf("ok: %s -> %s\n", verb, app.EffectiveSlug())
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
}

func newDockerAppDeleteCmd() *cobra.Command {
	var keepVolumes bool
	cmd := &cobra.Command{
		Use:     "delete <id>",
		Short:   "Uninstall a docker app (stops the stack, removes its row)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			repo := dockerAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			// JAB-315: delete the container/data for THIS instance (EffectiveSlug),
			// not whatever the base catalog slug resolves to.
			if _, err := sharedAgent.Call(ctx, "docker_app.delete", map[string]any{
				"slug":          app.EffectiveSlug(),
				"purge_volumes": !keepVolumes,
			}); err != nil {
				return err
			}
			// Cleanup domains + ports + the docker_apps row.
			cleanupDockerAppDomains(ctx, repository.NewDomainRepository(sharedDB), sharedAgent, app.ID)
			ports, _ := repo.ListPortsForApp(ctx, app.ID)
			for _, p := range ports {
				_ = repo.DeletePort(ctx, p.ID)
			}
			if err := repo.Delete(ctx, app.ID); err != nil {
				return err
			}
			cliAuditOK(ctx, "docker_app.delete", "docker_app", app.ID, nil)
			fmt.Println("ok: deleted", app.Slug, "("+app.ID+")")
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepVolumes, "keep-volumes", false, "keep /var/lib/jabali/docker-apps/<slug> data on disk")
	return cmd
}

// cleanupDockerAppDomains removes an app's auto-created MANAGED domains (+ their
// proxy vhosts) and DETACHES — never deletes — a tenant's own domain attached to
// a reverse-proxy app. JAB-364: the CLI delete previously deleted EVERY domain
// row with docker_app_id = appID, destroying user-owned domains the HTTP handler
// (docker_apps_user.go) preserves. Shared so the CLI matches that behaviour.
func cleanupDockerAppDomains(ctx context.Context, domRepo repository.DomainRepository, ag agent.AgentInterface, appID string) {
	domList, _, _ := domRepo.List(ctx, repository.ListOptions{})
	for _, d := range domList {
		if d.DockerAppID == nil || *d.DockerAppID != appID {
			continue
		}
		if d.ManagedBy == models.DomainManagedByDockerApp {
			// Hostname we auto-created for this app at install — remove the row
			// and tear down its proxy vhost.
			domName := d.Name
			_ = domRepo.Delete(ctx, d.ID)
			if ag != nil && domName != "" {
				rmCtx, rmCancel := context.WithTimeout(ctx, 30*time.Second)
				_, _ = ag.Call(rmCtx, "docker_app.vhost_remove",
					map[string]string{"domain_name": domName})
				rmCancel()
			}
		} else {
			// The tenant's own pre-existing domain — keep the row, just detach
			// the app link + clear the injected proxy_pass rule.
			_ = domRepo.DetachDockerApp(ctx, d.ID, true)
		}
	}
}

func newDockerAppLogsCmd() *cobra.Command {
	var lines int
	var service string
	cmd := &cobra.Command{
		Use:     "logs <id>",
		Short:   "Tail container logs",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			repo := dockerAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			// JAB-315: tail THIS instance's container logs (EffectiveSlug).
			params := map[string]any{"slug": app.EffectiveSlug()}
			if lines > 0 {
				params["lines"] = lines
			}
			if service != "" {
				params["service"] = service
			}
			raw, err := sharedAgent.Call(ctx, "docker_app.logs", params)
			if err != nil {
				return err
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
	cmd.Flags().IntVar(&lines, "lines", 200, "lines to tail")
	cmd.Flags().StringVar(&service, "service", "", "compose service name (default: first service)")
	return cmd
}

func newDockerAppUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "update <id>",
		Short:   "Pull the latest image and re-create the stack",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			repo := dockerAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return err
			}
			// Re-render the compose from the current catalog template so a
			// catalog fix / version bump reaches this install (the agent
			// otherwise reuses the stale on-disk compose). Secrets preserved
			// via the install's existing .env. Best-effort: on any failure
			// fall back to the on-disk compose so update never hard-blocks.
			updateParams := map[string]any{
				"slug":                        app.EffectiveSlug(),
				"healthcheck_timeout_seconds": 300,
			}
			if composeYML, envFile, rerr := rerenderInstallForCLI(ctx, repo, app); rerr != nil {
				fmt.Fprintf(os.Stderr, "warning: re-render skipped (%v); using on-disk compose\n", rerr)
			} else {
				updateParams["compose_yml"] = composeYML
				updateParams["env_file"] = envFile
			}
			raw, err := sharedAgent.Call(ctx, "docker_app.update", updateParams)
			if err != nil {
				return err
			}
			// On a successful update the install was re-rendered from the
			// current catalog, so refresh the stored version label.
			var outc struct {
				Outcome string `json:"outcome"`
			}
			if json.Unmarshal(raw, &outc) == nil && outc.Outcome == "updated" {
				if cat, cerr := loadDockerCatalogForCLI(); cerr == nil {
					if entry, ok := cat.Get(app.Slug); ok && entry.Version != "" {
						_ = repo.UpdateCatalogVersion(ctx, app.ID, entry.Version)
					}
				}
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
}

func newDockerAppBackupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backups <id>",
		Short:   "List restic backups taken for this install",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := dockerAppRepoFromDB()
			rows, err := repo.ListBackupsForApp(ctx, args[0])
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tRESTIC_ID\tSIZE_BYTES\tREASON\tCREATED")
			for _, r := range rows {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n",
					r.ID, r.ResticID, r.SizeBytes, r.Reason, r.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		},
	}
	return cmd
}

// ---- jabali docker (engine toggle, mirrors `jabali db postgres enable`) -----

func newDockerEngineActionCmd(action, short string) *cobra.Command {
	return &cobra.Command{
		Use:     action,
		Short:   short,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			verb := "docker.install"
			if action == "disable" {
				verb = "docker.disable"
			}
			raw, err := sharedAgent.Call(ctx, verb, map[string]any{})
			if err != nil {
				return err
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
}

func newDockerEngineStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show docker engine status (active, marketplace toggle state)",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			raw, err := sharedAgent.Call(ctx, "docker.status", map[string]any{})
			if err != nil {
				return err
			}
			os.Stdout.Write(raw)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// rerenderInstallForCLI re-renders an install's compose from the current
// catalog template, preserving its domain/ports/limits/secrets. Mirrors
// api.(*dockerAppHandler).renderInstallCompose for the CLI update path.
// Returns the rendered compose + merged env file (never log either).
// readInstallEnvCLI reads an install's on-disk .env (KEY=VALUE) back over the
// agent so a re-render preserves generated secrets. Mirrors api.readInstallEnv.
func readInstallEnvCLI(ctx context.Context, app *models.DockerApp) (map[string]string, error) {
	raw, rerr := sharedAgent.Call(ctx, "docker_app.read_env", map[string]any{"slug": app.EffectiveSlug()})
	if rerr != nil {
		return nil, fmt.Errorf("read env: %w", rerr)
	}
	var resp struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(raw, &resp) != nil || resp.Env == nil {
		return map[string]string{}, nil
	}
	return resp.Env, nil
}

func rerenderInstallForCLI(ctx context.Context, repo repository.DockerAppRepository, app *models.DockerApp) (string, string, error) {
	existingEnv, err := readInstallEnvCLI(ctx, app)
	if err != nil {
		return "", "", err
	}
	return renderInstallComposeCLI(ctx, repo, app, existingEnv)
}

// renderInstallComposeCLI re-renders an install's compose from the current
// catalog using baseEnv as the secret/override source (preserved through
// MaterialiseEnv). The env edit/regenerate paths pass a modified baseEnv.
func renderInstallComposeCLI(ctx context.Context, repo repository.DockerAppRepository, app *models.DockerApp, baseEnv map[string]string) (string, string, error) {
	cat, err := loadDockerCatalogForCLI()
	if err != nil {
		return "", "", fmt.Errorf("load catalog: %w", err)
	}
	entry, ok := cat.Get(app.Slug)
	if !ok {
		return "", "", fmt.Errorf("catalog entry %q not found", app.Slug)
	}
	envMap, err := dockerapp.MaterialiseEnv(entry, baseEnv)
	if err != nil {
		return "", "", fmt.Errorf("materialise env: %w", err)
	}
	ports, _ := repo.ListPortsForApp(ctx, app.ID)
	runtime := make(map[string]dockerapp.RuntimePort, len(ports))
	for _, p := range ports {
		bind := "127.0.0.1"
		switch {
		case p.BindInterface == "public":
			bind = "0.0.0.0"
		case p.BindInterface != "" && p.BindInterface != "loopback":
			bind = p.BindInterface
		}
		runtime[p.PortName] = dockerapp.RuntimePort{
			HostPort:      p.HostPort,
			ContainerPort: p.ContainerPort,
			BindInterface: bind,
			Protocol:      p.Protocol,
		}
	}
	domain := ""
	if dl, _, derr := domainRepoFromDB().List(ctx, repository.ListOptions{}); derr == nil {
		for _, d := range dl {
			if d.DockerAppID != nil && *d.DockerAppID == app.ID {
				domain = d.Name
				break
			}
		}
	}
	cpu, mem, pids := "", "", 0
	if app.CPULimit != nil {
		cpu = *app.CPULimit
	}
	if app.MemoryLimit != nil {
		mem = *app.MemoryLimit
	}
	if app.PIDsLimit != nil {
		pids = *app.PIDsLimit
	}
	composeYML, err := dockerapp.Render(entry, dockerapp.RenderParams{
		Slug:         app.EffectiveSlug(),
		Name:         app.Name,
		Domain:       domain,
		ImageChannel: entry.ImageChannel,
		DataRoot:     "/var/lib/jabali/docker-apps/" + app.EffectiveSlug(),
		CPULimit:     cpu,
		MemoryLimit:  mem,
		PIDsLimit:    pids,
		Ports:        runtime,
		Env:          envMap,
	})
	if err != nil {
		return "", "", fmt.Errorf("render: %w", err)
	}
	return composeYML, buildEnvFileForCLI(envMap), nil
}
