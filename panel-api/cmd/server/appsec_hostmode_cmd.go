package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// appsec_hostmode_cmd.go — GH #1641.
//
// Per-host AppSec mode. Where `appsec exclusion` drops ONE rule for ONE path,
// this is the coarse tool for a host that trips a rotating set of rules — a
// Flarum forum where users legitimately paste SQL/shell into post bodies. "detect"
// puts the host into detection-only: rules still run + score + log (explain keeps
// working), only the anomaly-score BLOCK is suppressed; native virtual-patches
// and the behavioural IP bouncer are unaffected. See appseccfg.RenderHostModes.

func crsHostModeRepo() repository.CRSHostModeRepository {
	return repository.NewCRSHostModeRepository(sharedDB)
}

func newAppSecHostModeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host-mode",
		Short: "Manage per-host AppSec mode (detection-only)",
		Long: "Put a host into detection-only, for a host whose ordinary traffic trips a\n" +
			"rotating set of CRS rules (e.g. a Flarum forum where users paste SQL/code).\n\n" +
			"'detect' suppresses only the CRS anomaly-score BLOCK for that host: every rule\n" +
			"still runs, scores and logs (jabali appsec explain keeps working), the native\n" +
			"virtual-patch rules still block scanners, and the behavioural IP bouncer is\n" +
			"untouched. It is a deliberate, host-scoped posture — not 'AppSec off'.",
	}
	cmd.AddCommand(newAppSecHostModeSetCmd(), newAppSecHostModeListCmd(), newAppSecHostModeClearCmd())
	return cmd
}

func newAppSecHostModeSetCmd() *cobra.Command {
	var host, mode, note string
	cmd := &cobra.Command{
		Use:     "set",
		Short:   "Set a host's AppSec mode (host is required; mode defaults to detect)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			m := appseccfg.HostMode{Host: host, Mode: mode, Note: note}
			if err := appseccfg.ValidateHostMode(m); err != nil {
				return err
			}
			row := &models.CRSHostMode{ID: ids.NewULID(), Host: host, Mode: mode, Note: note}
			if err := crsHostModeRepo().Upsert(ctx, row); err != nil {
				return fmt.Errorf("save host mode: %w", err)
			}
			cliAuditOK(ctx, "appsec.host_mode_set", "crs_host_mode", host, nil)
			if jsonOutput {
				return printJSON(map[string]any{"host": host, "mode": mode})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"set %s → %s (detection-only: rules still score + log; native virtual-patches\n"+
					"  and the IP bouncer still block)\n"+
					"  apply with: jabali appsec render-config --reconcile --reload\n",
				host, mode)
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "hostname to set the mode for (required)")
	cmd.Flags().StringVar(&mode, "mode", appseccfg.HostModeDetect, "AppSec mode (only 'detect' is supported)")
	cmd.Flags().StringVar(&note, "note", "", "why this host is in this mode")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func newAppSecHostModeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List per-host AppSec modes",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			rows, err := crsHostModeRepo().List(ctx)
			if err != nil {
				return fmt.Errorf("list host modes: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"host_modes": rows, "total": len(rows)})
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No per-host AppSec modes.")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "HOST\tMODE\tNOTE")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\n", r.Host, r.Mode, r.Note)
			}
			return w.Flush()
		},
	}
}

func newAppSecHostModeClearCmd() *cobra.Command {
	var host string
	cmd := &cobra.Command{
		Use:     "clear",
		Short:   "Clear a host's mode, restoring full blocking",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			if err := crsHostModeRepo().DeleteByHost(ctx, host); err != nil {
				return fmt.Errorf("clear host mode: %w", err)
			}
			cliAuditOK(ctx, "appsec.host_mode_clear", "crs_host_mode", host, nil)
			if jsonOutput {
				return printJSON(map[string]any{"host": host, "cleared": true})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"cleared %s (full blocking restored)\n"+
					"  apply with: jabali appsec render-config --reconcile --reload\n", host)
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "hostname to clear (required)")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}
