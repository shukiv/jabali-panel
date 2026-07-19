// Python app create + env management from the CLI (Gitea #548). Mirrors the
// create + putEnv contracts in api/python_apps.go: same validation (python
// version / app type / entrypoint / base URI), the same free-loopback-port
// allocation, owner = the domain's owner, and the same proxy_pass nginx rule
// attached to the domain so the reconciler renders the vhost. Created apps are
// identical rows to GUI installs, so list/start/stop/restart/logs/delete work
// on them unchanged.
package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/pyframeworks"
)

// loadPyFrameworkCatalogCLI loads the JAB-164 framework catalog with the same
// deployed-path + dev-fallback used by the server.
func loadPyFrameworkCatalogCLI() *pyframeworks.Catalog {
	cat, _ := pyframeworks.LoadDir("/usr/local/share/jabali/py-frameworks")
	if cat.Len() == 0 {
		if c2, _ := pyframeworks.LoadDir("install/py-frameworks"); c2.Len() > 0 {
			return c2
		}
	}
	return cat
}

// Validation regexes mirror api/python_apps.go.
var (
	cliBaseURIRe    = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	cliPyVersionRe  = regexp.MustCompile(`^3\.(?:[0-9]|1[0-9])$`)
	cliEntrypointRe = regexp.MustCompile(`^[A-Za-z0-9_.]+:[A-Za-z0-9_]+$`)
)

func newPythonAppCreateCmd() *cobra.Command {
	var (
		name      string
		domainID  string
		domain    string
		pyVersion string
		appType   string
		entry     string
		appRoot   string
		baseURI   string
		envPairs  []string
		framework string
	)
	cmd := &cobra.Command{
		Use:     "create",
		Short:   "Create a Python app (same validation + port/proxy allocation as the UI)",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			// JAB-164: a framework install derives app_type/entrypoint (and a
			// default python version) from the catalog entry; the reconciler then
			// scaffolds the starter + deps + generated secrets before first apply.
			if framework != "" {
				fe, ok := loadPyFrameworkCatalogCLI().Get(framework)
				if !ok {
					return fmt.Errorf("unknown --framework %q (run `python-app frameworks` to list)", framework)
				}
				spec, serr := fe.ResolveCreate()
				if serr != nil {
					return fmt.Errorf("resolve framework %q: %w", framework, serr)
				}
				appType = spec.AppType
				entry = spec.Entrypoint
				if pyVersion == "" && fe.PythonMin != "" {
					pyVersion = fe.PythonMin
				}
			}
			if appType == "" {
				appType = "wsgi"
			}
			if baseURI == "" {
				baseURI = "/"
			}
			if !cliPyVersionRe.MatchString(pyVersion) {
				return fmt.Errorf("invalid --python-version %q (e.g. 3.11, 3.12)", pyVersion)
			}
			if appType != "wsgi" && appType != "asgi" {
				return fmt.Errorf("invalid --app-type %q (wsgi|asgi)", appType)
			}
			if !cliEntrypointRe.MatchString(entry) {
				return fmt.Errorf("invalid --entrypoint %q (expected module:callable)", entry)
			}
			if !cliBaseURIRe.MatchString(baseURI) {
				return fmt.Errorf("invalid --base-uri %q", baseURI)
			}
			if appRoot == "" {
				return fmt.Errorf("--app-root is required")
			}
			if domainID == "" && domain == "" {
				return fmt.Errorf("one of --domain-id or --domain is required")
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			domRepo := domainRepoFromDB()
			var dom *models.Domain
			var derr error
			if domainID != "" {
				dom, derr = domRepo.FindByID(ctx, domainID)
			} else {
				dom, derr = domRepo.FindByName(ctx, domain)
			}
			if derr != nil || dom == nil {
				return fmt.Errorf("domain not found (check --domain/--domain-id)")
			}

			repo := pythonAppRepoFromDB()
			port, err := repo.FindFreeLoopbackPort(ctx)
			if err != nil {
				return fmt.Errorf("allocate loopback port: %w", err)
			}
			app := &models.PythonApp{
				ID:            ulid.Make().String(),
				UserID:        dom.UserID,
				DomainID:      dom.ID,
				Name:          name,
				Runtime:       "python",
				PythonVersion: pyVersion,
				AppRoot:       appRoot,
				AppType:       appType,
				Entrypoint:    entry,
				BaseURI:       baseURI,
				LoopbackPort:  &port,
				Framework:     framework,
				Status:        models.PythonAppStatusPending,
			}
			if err := repo.Create(ctx, app); err != nil {
				return fmt.Errorf("create app (domain+path may already be in use): %w", err)
			}
			if len(envPairs) > 0 {
				rows, perr := parsePythonEnvPairs(envPairs)
				if perr != nil {
					_ = repo.Delete(ctx, app.ID)
					return perr
				}
				_ = repo.ReplaceEnv(ctx, app.ID, rows)
			}
			attachPythonProxyRuleCLI(ctx, dom, app)

			cliAuditOK(ctx, "python_app.create", "python_app", app.ID, &dom.UserID)
			if jsonOutput {
				return printJSON(app)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created Python app %s (%s) on %s%s → 127.0.0.1:%d (status=pending; the reconciler will provision it).\n",
				app.Name, app.ID, dom.Name, baseURI, port)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "app name (required)")
	cmd.Flags().StringVar(&domainID, "domain-id", "", "domain id to mount on (or use --domain)")
	cmd.Flags().StringVar(&domain, "domain", "", "domain name to mount on (resolved to its id)")
	cmd.Flags().StringVar(&pyVersion, "python-version", "", "Python version, e.g. 3.12 (required)")
	cmd.Flags().StringVar(&appType, "app-type", "wsgi", "wsgi|asgi")
	cmd.Flags().StringVar(&entry, "entrypoint", "", "module:callable, e.g. myapp.wsgi:application (required unless --framework)")
	cmd.Flags().StringVar(&framework, "framework", "", "marketplace slug (e.g. django, flask, fastapi); derives app-type/entrypoint and scaffolds the starter")
	cmd.Flags().StringVar(&appRoot, "app-root", "", "app root under the owner's home (required)")
	cmd.Flags().StringVar(&baseURI, "base-uri", "/", "mount path on the domain")
	cmd.Flags().StringArrayVar(&envPairs, "env", nil, "KEY=VALUE (repeatable)")
	return cmd
}

func parsePythonEnvPairs(pairs []string) ([]models.PythonAppEnv, error) {
	rows := make([]models.PythonAppEnv, 0, len(pairs))
	for _, kv := range pairs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--env %q must be KEY=VALUE", kv)
		}
		rows = append(rows, models.PythonAppEnv{ID: ulid.Make().String(), Key: k, Value: v})
	}
	return rows, nil
}

// attachPythonProxyRuleCLI mirrors api.attachProxyRule: proxy_pass base_uri ->
// loopback port, replacing any same-path rule, then persists the domain so the
// reconciler renders the vhost.
func attachPythonProxyRuleCLI(ctx context.Context, dom *models.Domain, app *models.PythonApp) {
	port := 0
	if app.LoopbackPort != nil {
		port = *app.LoopbackPort
	}
	ws := true
	rule := models.NginxRule{Type: "proxy_pass", Path: app.BaseURI, Target: "http://127.0.0.1:" + strconv.Itoa(port), Websocket: &ws}
	rules := append(models.NginxRules{}, dom.NginxRules...)
	replaced := false
	for i := range rules {
		if rules[i].Type == "proxy_pass" && rules[i].Path == app.BaseURI {
			rules[i] = rule
			replaced = true
			break
		}
	}
	if !replaced {
		rules = append(rules, rule)
	}
	dom.NginxRules = rules
	_ = domainRepoFromDB().Update(ctx, dom)
}

func newPythonAppEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "View or update a Python app's environment variables",
	}
	cmd.AddCommand(newPythonAppEnvGetCmd(), newPythonAppEnvSetCmd())
	return cmd
}

func newPythonAppEnvGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "get <app-id>",
		Short:   "List the app's environment variables (values are not masked — python_app_env has no secret flag)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			rows, err := pythonAppRepoFromDB().ListEnv(ctx, args[0])
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(rows)
			}
			for _, r := range rows {
				fmt.Fprintf(cmd.OutOrStdout(), "%s=%s\n", r.Key, r.Value)
			}
			return nil
		},
	}
}

func newPythonAppEnvSetCmd() *cobra.Command {
	var replace bool
	cmd := &cobra.Command{
		Use:     "set <app-id> <KEY=VALUE> [<KEY=VALUE>...]",
		Short:   "Set env vars (merge by default; --replace swaps the whole set)",
		Args:    cobra.MinimumNArgs(2),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			repo := pythonAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return fmt.Errorf("app not found: %w", err)
			}
			merged := map[string]string{}
			if !replace {
				existing, lerr := repo.ListEnv(ctx, app.ID)
				if lerr != nil {
					return lerr
				}
				for _, r := range existing {
					merged[r.Key] = r.Value
				}
			}
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("argument %q is not KEY=VALUE", kv)
				}
				merged[k] = v
			}
			rows := make([]models.PythonAppEnv, 0, len(merged))
			for k, v := range merged {
				rows = append(rows, models.PythonAppEnv{ID: ulid.Make().String(), Key: k, Value: v})
			}
			if err := repo.ReplaceEnv(ctx, app.ID, rows); err != nil {
				return fmt.Errorf("replace env: %w", err)
			}
			cliAuditOK(ctx, "python_app.env_update", "python_app", app.ID, &app.UserID)
			fmt.Fprintf(cmd.OutOrStdout(), "Updated env for %s (%d vars). Restart the app to apply.\n", app.Name, len(rows))
			return nil
		},
	}
	cmd.Flags().BoolVar(&replace, "replace", false, "replace the entire env set instead of merging")
	return cmd
}
