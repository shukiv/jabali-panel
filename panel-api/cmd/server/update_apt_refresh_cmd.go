package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newUpdateAptRefreshCmd refreshes the Updates Center's OS-patch state right now
// (GH #1224). The background poll runs only every 6h, so after unattended-upgrades
// applies at the scheduled time the page shows a stale "Last Checked"/"Last
// Applied" for up to 6 hours — which reads as "auto-updates never ran". An
// ExecStartPost drop-in on apt-daily-upgrade.service calls this immediately after
// an apply so the panel reflects it within seconds.
//
// It does exactly what the update-poll reconciler does (agent system.apt_check ->
// UpsertApt + UpsertAptStatus), on demand. Best-effort by design: the drop-in
// prefixes it with '-' so a transient failure never fails the apt run.
func newUpdateAptRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apt-refresh",
		Short: "Refresh the Updates Center OS-patch state now (used after an auto-apply) (GH #1224)",
		Args:  cobra.NoArgs,
		// requireDBAndAgent, not requireDB: it calls the agent for system.apt_check.
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()

			raw, err := sharedAgent.Call(ctx, "system.apt_check", nil)
			if err != nil {
				return fmt.Errorf("system.apt_check: %w", err)
			}
			var av struct {
				Total          int        `json:"total"`
				SecurityTotal  int        `json:"security_total"`
				RebootRequired bool       `json:"reboot_required"`
				LastAppliedAt  *time.Time `json:"last_applied_at"`
			}
			if err := json.Unmarshal(raw, &av); err != nil {
				return fmt.Errorf("parse system.apt_check: %w", err)
			}

			state := repository.NewUpdateStateRepository(sharedDB)
			now := time.Now()
			if err := state.UpsertApt(ctx, av.Total, av.SecurityTotal, now); err != nil {
				return fmt.Errorf("persist apt totals: %w", err)
			}
			if err := state.UpsertAptStatus(ctx, av.LastAppliedAt, av.RebootRequired); err != nil {
				return fmt.Errorf("persist apt status: %w", err)
			}

			applied := "never"
			if av.LastAppliedAt != nil {
				applied = av.LastAppliedAt.Local().Format(time.RFC3339)
			}
			fmt.Printf("Updates Center refreshed: %d upgradable (%d security), last applied %s, reboot_required=%v\n",
				av.Total, av.SecurityTotal, applied, av.RebootRequired)
			return nil
		},
	}
}
