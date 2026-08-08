package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
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
	cmd.AddCommand(newDRStatusCmd(), newDRPairCmd(), newDRUnpairCmd(), newDRFeedCmd(), newDRPromoteCmd())
	return cmd
}

func scheduleRepoFromDB() repository.BackupScheduleRepository {
	return repository.NewBackupScheduleRepository(sharedDB)
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
			lastSync := ""
			if s.DRLastSyncAt != nil {
				lastSync = s.DRLastSyncAt.UTC().Format(time.RFC3339)
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"server_role":         s.IsStandby(),
					"role":                orDefault(s.ServerRole, models.ServerRolePrimary),
					"dr_peer_label":       s.DRPeerLabel,
					"dr_destination_id":   strOrEmpty(s.DRDestinationID),
					"dr_paired_at":        paired,
					"dr_last_sync_at":     lastSync,
					"dr_last_snapshot_id": s.DRLastSnapshotID,
					"dr_last_sync_status": s.DRLastSyncStatus,
					"dr_last_sync_error":  s.DRLastSyncError,
				})
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "Role:\t%s\n", orDefault(s.ServerRole, models.ServerRolePrimary))
			fmt.Fprintf(w, "Peer:\t%s\n", orDefault(s.DRPeerLabel, "-"))
			fmt.Fprintf(w, "DR destination:\t%s\n", orDefault(destLabel, "-"))
			fmt.Fprintf(w, "Paired at:\t%s\n", orDefault(paired, "-"))
			// Sync liveness is only meaningful on a standby (the loop is inert
			// on a primary), so only surface it there.
			if s.IsStandby() {
				fmt.Fprintf(w, "Last sync:\t%s\n", orDefault(lastSync, "never"))
				fmt.Fprintf(w, "Last sync status:\t%s\n", orDefault(s.DRLastSyncStatus, "-"))
				fmt.Fprintf(w, "Applied snapshot:\t%s\n", orDefault(s.DRLastSnapshotID, "-"))
				if s.DRLastSyncError != "" {
					fmt.Fprintf(w, "Last sync error:\t%s\n", s.DRLastSyncError)
				}
			}
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

// newDRFeedCmd runs on the PRIMARY. It ensures a system_backup schedule ships
// the primary's control-plane state to the DR destination on a cadence, which is
// exactly what the standby's drsync loop pulls from. Idempotent: a re-run against
// a destination already fed just re-enables the existing schedule.
func newDRFeedCmd() *cobra.Command {
	var destID, cron string
	cmd := &cobra.Command{
		Use:   "feed",
		Short: "Primary side: ship system backups to the DR destination on a cadence",
		Long: "Run on the PRIMARY. Ensures an enabled system_backup schedule targets the " +
			"DR backup destination so the standby always has a fresh manifest to pull. " +
			"Idempotent — re-running re-enables the existing schedule rather than adding a " +
			"duplicate. The standby reads this same destination; it is never given a " +
			"primary-mutating credential.",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if destID == "" {
				return fmt.Errorf("--destination is required (the DR backup destination to ship to)")
			}
			cron = strings.TrimSpace(cron)
			if cron == "" {
				cron = "0 * * * *" // hourly — a sane DR freshness default; tune with --cron
			}
			if _, err := internalbackup.NextFire(cron, time.Now().UTC()); err != nil {
				return fmt.Errorf("invalid --cron %q: %w", cron, err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			dest, derr := backupDestinationRepoFromDB().Get(ctx, destID)
			if derr != nil || dest == nil {
				return fmt.Errorf("backup destination %q not found; create it first with `jabali backup destination create`", destID)
			}

			schedRepo := scheduleRepoFromDB()
			// Idempotency: reuse an existing system_backup schedule already
			// linked to this destination rather than stacking duplicates.
			existing, ferr := findSystemScheduleForDest(ctx, schedRepo, destID)
			if ferr != nil {
				return ferr
			}
			if existing != nil {
				if !existing.Enabled {
					existing.Enabled = true
					if err := schedRepo.Update(ctx, existing); err != nil {
						return fmt.Errorf("re-enable existing DR feed schedule: %w", err)
					}
				}
				fmt.Printf("DR feed already configured (schedule %s → %s); ensured enabled.\n", existing.ID, dest.Name)
				return nil
			}

			next, _ := internalbackup.NextFire(cron, time.Now().UTC())
			s := &models.BackupSchedule{
				ID:        ids.NewULID(),
				Kind:      models.BackupScheduleKindSystem,
				CronExpr:  cron,
				Enabled:   true,
				NextRunAt: &next,
			}
			if err := schedRepo.Create(ctx, s); err != nil {
				return fmt.Errorf("create DR feed schedule: %w", err)
			}
			if err := schedRepo.ReplaceDestinations(ctx, s.ID, []string{destID}); err != nil {
				return fmt.Errorf("link DR feed schedule to destination: %w", err)
			}
			fmt.Printf("DR feed configured: system_backup schedule %s ships to %s on cron %q.\n", s.ID, dest.Name, cron)
			fmt.Println("Pair the standby against this same destination with `jabali dr pair --destination <id>`.")
			return nil
		},
	}
	cmd.Flags().StringVar(&destID, "destination", "", "DR backup destination ID to ship system backups to")
	cmd.Flags().StringVar(&cron, "cron", "", "cron cadence for the DR feed (default hourly, \"0 * * * *\")")
	return cmd
}

// findSystemScheduleForDest returns the first enabled-or-disabled system_backup
// schedule whose destination set includes destID, or nil if none exists.
func findSystemScheduleForDest(ctx context.Context, repo repository.BackupScheduleRepository, destID string) (*models.BackupSchedule, error) {
	all, err := repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list backup schedules: %w", err)
	}
	for i := range all {
		if all[i].Kind != models.BackupScheduleKindSystem {
			continue
		}
		dests, derr := repo.GetDestinations(ctx, all[i].ID)
		if derr != nil {
			return nil, fmt.Errorf("read schedule destinations: %w", derr)
		}
		for _, d := range dests {
			if d.ID == destID {
				return &all[i], nil
			}
		}
	}
	return nil, nil
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
