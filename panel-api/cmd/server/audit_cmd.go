// `jabali audit` cobra subcommands — query / verify / prune the M49
// unified audit log (ADR-0106). Thin flag-decode + render wrappers
// over repository.AuditEventRepository + audit.VerifyChain; same
// store the /admin/audit + /me/activity API reads.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/audit"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func auditRepoFromDB() repository.AuditEventRepository {
	return repository.NewAuditEventRepository(sharedDB)
}

func newAuditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Query, verify, and prune the unified audit log (M49 / ADR-0106)",
	}
	cmd.AddCommand(newAuditQueryCmd(), newAuditVerifyCmd(), newAuditPruneCmd())
	return cmd
}

func newAuditQueryCmd() *cobra.Command {
	var limit int
	var q string
	cmd := &cobra.Command{
		Use:     "query",
		Short:   "List recent audit events (admin/forensics view)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			if limit <= 0 || limit > 1000 {
				limit = 50
			}
			rows, total, err := auditRepoFromDB().ListAll(ctx, repository.ListOptions{
				Offset: 0, Limit: limit, Search: q, Sort: "ts", Order: "desc",
			})
			if err != nil {
				return err
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "TS\tACTOR_KIND\tACTION\tTARGET\tRESULT")
			for i := range rows {
				r := &rows[i]
				fmt.Fprintf(w, "%s\t%s\t%s\t%s/%s\t%s\n",
					r.TS.UTC().Format(time.RFC3339), r.ActorKind, r.Action,
					r.TargetType, r.TargetID, r.Result)
			}
			w.Flush()
			fmt.Printf("\n%d shown of %d total\n", len(rows), total)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "max rows (1-1000)")
	cmd.Flags().StringVar(&q, "q", "", "search (action/target/actor_kind/result)")
	return cmd
}

func newAuditVerifyCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "verify",
		Short:   "Recompute the hash chain and report tamper-evidence integrity",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			repo := auditRepoFromDB()
			rows, err := repo.AllForVerify(ctx)
			if err != nil {
				return err
			}
			anchor, _ := repo.ChainAnchor(ctx)
			brokenID, checked, ok := audit.VerifyChain(rows, anchor)
			if ok {
				fmt.Printf("chain OK — %d sealed rows verified (%d total rows)\n", checked, len(rows))
				return nil
			}
			// Non-nil error → non-zero exit, so a timer/monitor catches it.
			return fmt.Errorf("CHAIN BROKEN at row %s (after %d sealed rows verified OK)", brokenID, checked)
		},
	}
}

func newAuditPruneCmd() *cobra.Command {
	var days int
	cmd := &cobra.Command{
		Use:     "prune",
		Short:   "Delete audit rows older than --days (retention; ADR-0106)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			if days < 1 {
				days = 365
			}
			repo := auditRepoFromDB()
			cutoff := time.Now().UTC().AddDate(0, 0, -days)
			n, err := repo.PruneOlderThanWithAnchor(ctx, cutoff)
			if err != nil {
				return err
			}
			// Record the prune itself as an audit event — never a
			// silent selective delete (ADR-0106). Direct Create:
			// RowHash nil = pre-chain; the consumer seals it on its
			// next sweep if running. actor_kind=cli, no subject
			// (server-scoped → admin-only visibility).
			meta, _ := json.Marshal(map[string]any{
				"cutoff": cutoff.Format(time.RFC3339), "pruned": n, "days": days,
			})
			_ = repo.Create(ctx, &models.AuditEvent{
				ID:         ids.NewULID(),
				TS:         time.Now().UTC(),
				ActorKind:  models.AuditActorCLI,
				Action:     "audit.retention.prune",
				TargetType: "audit",
				Result:     models.AuditResultOK,
				Meta:       meta,
			})
			fmt.Printf("pruned %d audit rows older than %s (%dd)\n",
				n, cutoff.Format(time.RFC3339), days)
			return nil
		},
	}
	cmd.Flags().IntVar(&days, "days", 365, "retention window in days (delete older)")
	return cmd
}
