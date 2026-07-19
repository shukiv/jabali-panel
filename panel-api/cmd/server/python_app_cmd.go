package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// jabali python-app — admin CLI for the Python Application Manager
// (ADR-0131 / GH #203). Reads the DB directly + dispatches the agent, like the
// docker-app CLI. App creation stays in the panel UI/API (it allocates a port
// + attaches the nginx proxy rule); the CLI covers list/control/logs/delete.
func newPythonAppCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "python-app",
		Short: "Manage Python apps (ADR-0131; admin-only)",
	}
	cmd.AddCommand(
		newPythonAppListCmd(),
		newPythonAppControlCmd("start", "Start an app"),
		newPythonAppControlCmd("stop", "Stop an app"),
		newPythonAppControlCmd("restart", "Restart an app"),
		newPythonAppLogsCmd(),
		newPythonAppDeleteCmd(),
		newPythonAppCreateCmd(),
		newPythonAppEnvCmd(),
		newPythonAppFrameworksCmd(),
	)
	return cmd
}

// newPythonAppFrameworksCmd lists the JAB-164 framework marketplace entries
// usable with `python-app create --framework <slug>`.
func newPythonAppFrameworksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "frameworks",
		Short: "List the Python frameworks installable via --framework",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cat := loadPyFrameworkCatalogCLI()
			if jsonOutput {
				return printJSON(cat.All())
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%-12s %-8s %-9s %s\n", "SLUG", "TYPE", "SERVER", "DESCRIPTION")
			for _, e := range cat.All() {
				fmt.Fprintf(out, "%-12s %-8s %-9s %s\n", e.Slug, e.AppType, e.Server, e.Description)
			}
			return nil
		},
	}
}

func pythonAppRepoFromDB() repository.PythonAppRepository {
	return repository.NewPythonAppRepository(sharedDB)
}

func newPythonAppListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List Python apps",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			apps, err := pythonAppRepoFromDB().ListAll(context.Background())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tRUNTIME\tMOUNT\tPORT\tSTATUS")
			for _, a := range apps {
				port := "-"
				if a.LoopbackPort != nil {
					port = fmt.Sprintf("%d", *a.LoopbackPort)
				}
				fmt.Fprintf(w, "%s\t%s\tpy%s/%s\t%s\t%s\t%s\n",
					a.ID, a.Name, a.PythonVersion, a.AppType, a.BaseURI, port, a.Status)
			}
			return w.Flush()
		},
	}
}

func newPythonAppControlCmd(verb, short string) *cobra.Command {
	return &cobra.Command{
		Use:     verb + " <app-id>",
		Short:   short,
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			raw, err := sharedAgent.Call(ctx, "app.python.control",
				map[string]any{"app_id": args[0], "action": verb})
			if err != nil {
				return err
			}
			fmt.Println(string(raw))
			return nil
		},
	}
}

func newPythonAppLogsCmd() *cobra.Command {
	var lines int
	c := &cobra.Command{
		Use:     "logs <app-id>",
		Short:   "Show an app's recent logs",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			raw, err := sharedAgent.Call(ctx, "app.python.logs",
				map[string]any{"app_id": args[0], "lines": lines})
			if err != nil {
				return err
			}
			var r struct {
				Logs string `json:"logs"`
			}
			_ = json.Unmarshal(raw, &r)
			fmt.Print(r.Logs)
			return nil
		},
	}
	c.Flags().IntVar(&lines, "lines", 200, "number of log lines")
	return c
}

func newPythonAppDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "delete <app-id>",
		Short:   "Stop + remove an app (app files are kept)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			repo := pythonAppRepoFromDB()
			app, err := repo.FindByID(ctx, args[0])
			if err != nil {
				return fmt.Errorf("app not found: %w", err)
			}
			callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if _, err := sharedAgent.Call(callCtx, "app.python.remove", map[string]any{"app_id": app.ID}); err != nil {
				fmt.Fprintf(os.Stderr, "warning: agent remove failed: %v\n", err)
			}
			detachPythonRulesCLI(ctx, app)
			if err := repo.Delete(ctx, app.ID); err != nil {
				return err
			}
			cliAuditOK(ctx, "python_app.delete", "python_app", app.ID, &app.UserID)
			fmt.Printf("deleted %s (%s)\n", app.Name, app.ID)
			return nil
		},
	}
}
