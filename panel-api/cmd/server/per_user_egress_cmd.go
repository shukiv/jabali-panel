// `jabali per-user-egress flip-mature` — flips user_egress_policies
// rows that have been in `learning` state for at least the configured
// soak period (default 7 days) to `enforced`. Invoked by the
// jabali-per-user-egress-flip.timer systemd unit daily.
//
// Operator pin: when /etc/jabali/per-user-egress.mode contains the
// literal string "learning", flip is a no-op — operator-controlled
// hold for hosts where the LEARNING soak needs to run longer.
//
// See ADR-0084 §8 (LEARNING auto-flip) and the M34 runbook.

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const operatorPinFile = "/etc/jabali/per-user-egress.mode"

func newPerUserEgressCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "per-user-egress",
		Short: "Per-user PHP-FPM egress firewall (M34) operator commands",
	}
	cmd.AddCommand(
		newPerUserEgressFlipMatureCmd(),
		newPerUserEgressRequestsCmd(),
		newPerUserEgressDecideCmd(true),  // approve
		newPerUserEgressDecideCmd(false), // deny
		newPerUserEgressGetCmd(),
		newPerUserEgressSummaryCmd(),
		newPerUserEgressSetPolicyCmd(),
	)
	return cmd
}

func newPerUserEgressFlipMatureCmd() *cobra.Command {
	var (
		soakDays int
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:     "flip-mature",
		Short:   "Flip mature LEARNING policies to ENFORCED",
		Long:    `Find user_egress_policies rows in 'learning' state older than soak-days and flip to 'enforced'. Honors /etc/jabali/per-user-egress.mode = "learning" as an operator pin (no-op when set).`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if soakDays <= 0 {
				return fmt.Errorf("soak-days must be > 0")
			}
			if mode, err := os.ReadFile(operatorPinFile); err == nil {
				if strings.TrimSpace(string(mode)) == "learning" {
					fmt.Printf("per-user-egress flip-mature: operator pin %s = 'learning', skipping\n", operatorPinFile)
					return nil
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			repo := repository.NewUserEgressPolicyRepository(sharedDB)
			soak := time.Duration(soakDays) * 24 * time.Hour
			rows, err := repo.ListMatureLearning(ctx, soak)
			if err != nil {
				return fmt.Errorf("list mature: %w", err)
			}
			if len(rows) == 0 {
				fmt.Printf("per-user-egress flip-mature: no mature LEARNING rows (soak=%dd)\n", soakDays)
				return nil
			}

			fmt.Printf("per-user-egress flip-mature: %d mature rows (soak=%dd)\n", len(rows), soakDays)
			for _, r := range rows {
				fmt.Printf("  %s  learning_started=%s\n", r.UserID, r.LearningStartedAt)
				if dryRun {
					continue
				}
				next := r
				next.State = models.UserEgressStateEnforced
				if err := repo.Upsert(ctx, &next); err != nil {
					fmt.Fprintf(os.Stderr, "  upsert %s failed: %v\n", r.UserID, err)
					continue
				}
			}
			if dryRun {
				fmt.Println("per-user-egress flip-mature: dry-run, no rows flipped")
			} else {
				fmt.Printf("per-user-egress flip-mature: flipped %d rows to ENFORCED\n", len(rows))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&soakDays, "soak-days", 7, "minimum LEARNING age before auto-flip to ENFORCED")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without writing to DB")
	return cmd
}
