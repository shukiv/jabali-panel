package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #331 — DR / standby node CLI. `dr pair` designates this box a one-way async
// standby that pulls the primary's state from a read-only backup destination;
// `dr status` reports the role; `dr unpair` reverts to primary. Promotion lives
// in `dr promote` (Step 4). Pairing issues NO primary-mutating credential — the
// standby only ever reads/restores from the shared destination (least privilege).

func serverSettingsRepoFromDB() repository.ServerSettingsRepository {
	return repository.NewServerSettingsRepository(sharedDB)
}

func newDRCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dr",
		Short: "Disaster-recovery standby: pair, status, promote (GH #331)",
		Long: "One-way async DR standby with manual promotion. A standby pulls the " +
			"primary's state from a read-only backup destination and serves no live " +
			"traffic until `dr promote`. No automatic failover, no split-brain.",
	}
	cmd.AddCommand(newDRStatusCmd(), newDRPairCmd(), newDRUnpairCmd())
	return cmd
}

func newDRStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "status",
		Short:   "Show this box's DR role + pairing",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			s, err := serverSettingsRepoFromDB().Get(ctx)
			if err != nil || s == nil {
				return fmt.Errorf("read server settings: %w", err)
			}
			destLabel := ""
			if s.DRDestinationID != nil {
				if d, derr := backupDestinationRepoFromDB().Get(ctx, *s.DRDestinationID); derr == nil && d != nil {
					destLabel = fmt.Sprintf("%s (%s)", d.Name, d.Kind)
				} else {
					destLabel = *s.DRDestinationID
				}
			}
			paired := ""
			if s.DRPairedAt != nil {
				paired = s.DRPairedAt.UTC().Format(time.RFC3339)
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"server_role":       s.IsStandby(),
					"role":              orDefault(s.ServerRole, models.ServerRolePrimary),
					"dr_peer_label":     s.DRPeerLabel,
					"dr_destination_id": strOrEmpty(s.DRDestinationID),
					"dr_paired_at":      paired,
				})
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "Role:\t%s\n", orDefault(s.ServerRole, models.ServerRolePrimary))
			fmt.Fprintf(w, "Peer:\t%s\n", orDefault(s.DRPeerLabel, "-"))
			fmt.Fprintf(w, "DR destination:\t%s\n", orDefault(destLabel, "-"))
			fmt.Fprintf(w, "Paired at:\t%s\n", orDefault(paired, "-"))
			return w.Flush()
		},
	}
}

func newDRPairCmd() *cobra.Command {
	var destID, peerLabel string
	cmd := &cobra.Command{
		Use:   "pair",
		Short: "Designate this box a DR standby of a primary",
		Long: "Flip this box to a standby replica. --destination is an existing backup " +
			"destination (the DR channel: the primary ships system backups to it, this " +
			"standby pulls from it). The standby serves no live traffic until `dr promote`.",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if destID == "" {
				return fmt.Errorf("--destination is required (the read-only DR backup destination)")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			// The DR destination must exist — a standby that can't reach the feed
			// is useless, and we never want to record a dangling pointer.
			dest, derr := backupDestinationRepoFromDB().Get(ctx, destID)
			if derr != nil || dest == nil {
				return fmt.Errorf("backup destination %q not found; create it first with `jabali backup destination create`", destID)
			}
			repo := serverSettingsRepoFromDB()
			s, err := repo.Get(ctx)
			if err != nil || s == nil {
				return fmt.Errorf("read server settings: %w", err)
			}
			now := time.Now().UTC()
			s.ServerRole = models.ServerRoleStandby
			s.DRDestinationID = &destID
			s.DRPeerLabel = peerLabel
			s.DRPairedAt = &now
			if err := repo.Upsert(ctx, s); err != nil {
				return fmt.Errorf("save DR pairing: %w", err)
			}
			fmt.Printf("This box is now a DR STANDBY (peer=%q, destination=%s).\n", orDefault(peerLabel, "unlabelled"), dest.Name)
			fmt.Println("It will refuse tenant-provisioning writes and serve no live traffic until `jabali dr promote`.")
			return nil
		},
	}
	cmd.Flags().StringVar(&destID, "destination", "", "backup destination ID used as the read-only DR channel")
	cmd.Flags().StringVar(&peerLabel, "peer-label", "", "human label for the primary this box replicates (e.g. its hostname)")
	return cmd
}

func newDRUnpairCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "unpair",
		Short:   "Revert this box to a primary (clears standby role)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := serverSettingsRepoFromDB()
			s, err := repo.Get(ctx)
			if err != nil || s == nil {
				return fmt.Errorf("read server settings: %w", err)
			}
			s.ServerRole = models.ServerRolePrimary
			s.DRDestinationID = nil
			s.DRPeerLabel = ""
			s.DRPairedAt = nil
			if err := repo.Upsert(ctx, s); err != nil {
				return fmt.Errorf("clear DR pairing: %w", err)
			}
			fmt.Println("This box is now a PRIMARY. DR pairing cleared.")
			return nil
		},
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
