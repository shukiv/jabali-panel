// `jabali mailbox shares` cobra subcommands — list / add / remove
// mailbox sharing relationships (M6.5 shared folders).
//
// CLI inserts / deletes rows in the mailbox_shares table; the
// reconciler's m65_mailbox_share phase converges to Stalwart via
// the existing mailbox.share_set agent command on its next sweep.
// Operator workflow:
//
//	jabali mailbox shares list --owner alice@example.com
//	jabali mailbox shares add --owner alice@example.com \
//	    --shared-with bob@example.com --rights rw
//	jabali mailbox shares remove --id <ULID>
//
// Rights presets:
//
//	ro     → mayRead
//	rw     → mayRead + mayAddItems + mayRemoveItems
//	admin  → all rights
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func newMailboxSharesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shares",
		Short: "Manage shared mailbox folders (M6.5)",
	}
	cmd.AddCommand(
		newMailboxSharesListCmd(),
		newMailboxSharesAddCmd(),
		newMailboxSharesRemoveCmd(),
	)
	return cmd
}

func mailboxShareRepoFromDB() repository.MailboxShareRepository {
	return repository.NewMailboxShareRepository(sharedDB)
}

func newMailboxSharesListCmd() *cobra.Command {
	var ownerEmail string
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List shares for a given owner email",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ownerEmail == "" {
				return errors.New("--owner email required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			owner, err := mailboxRepoFromDB().FindByEmail(ctx, ownerEmail)
			if err != nil {
				return fmt.Errorf("find owner mailbox %q: %w", ownerEmail, err)
			}
			rows, _, err := mailboxShareRepoFromDB().FindByOwnerID(ctx, owner.ID, repository.ListOptions{Offset: 0, Limit: 200})
			if err != nil {
				return fmt.Errorf("list shares: %w", err)
			}
			if jsonOutput {
				return json.NewEncoder(os.Stdout).Encode(rows)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tSHARED_WITH_MAILBOX_ID\tRIGHTS\tCREATED")
			mboxRepo := mailboxRepoFromDB()
			for _, s := range rows {
				rt := summariseRights(s.Rights)
				sw := s.SharedWithMailboxID
				if mb, mErr := mboxRepo.FindByID(ctx, s.SharedWithMailboxID); mErr == nil {
					sw = mb.EmailCached
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
					s.ID, sw, rt, s.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&ownerEmail, "owner", "", "Owner mailbox email (required)")
	return cmd
}

func newMailboxSharesAddCmd() *cobra.Command {
	var ownerEmail, sharedWithEmail, preset string
	cmd := &cobra.Command{
		Use:     "add",
		Short:   "Grant a target mailbox shared access to the owner's mailbox",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ownerEmail == "" || sharedWithEmail == "" {
				return errors.New("--owner and --shared-with required")
			}
			rights, err := rightsFromPreset(preset)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			mboxRepo := mailboxRepoFromDB()
			owner, err := mboxRepo.FindByEmail(ctx, ownerEmail)
			if err != nil {
				return fmt.Errorf("find owner: %w", err)
			}
			target, err := mboxRepo.FindByEmail(ctx, sharedWithEmail)
			if err != nil {
				return fmt.Errorf("find shared-with: %w", err)
			}
			row := &models.MailboxShare{
				ID:                  ids.NewULID(),
				OwnerMailboxID:      owner.ID,
				SharedWithMailboxID: target.ID,
				Rights:              rights,
				ManagedBy:           "cli",
				CreatedAt:           time.Now().UTC(),
			}
			if err := mailboxShareRepoFromDB().Create(ctx, row); err != nil {
				return fmt.Errorf("create share: %w", err)
			}
			cliAuditOK(ctx, "mailbox.share_add", "mailbox_share", row.ID, nil)
			fmt.Fprintf(os.Stdout, "Share added id=%s rights=%s\n", row.ID, summariseRights(rights))
			fmt.Fprintln(os.Stdout, "Reconciler converges via mailbox.share_set on next sweep (~60s).")
			return nil
		},
	}
	cmd.Flags().StringVar(&ownerEmail, "owner", "", "Owner mailbox email (required)")
	cmd.Flags().StringVar(&sharedWithEmail, "shared-with", "", "Mailbox to grant share to (required)")
	cmd.Flags().StringVar(&preset, "rights", "rw", "Preset: ro | rw | admin (default rw)")
	return cmd
}

func newMailboxSharesRemoveCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:     "remove",
		Short:   "Revoke a share by ID",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return errors.New("--id required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			if err := mailboxShareRepoFromDB().Delete(ctx, id); err != nil {
				return fmt.Errorf("delete share: %w", err)
			}
			cliAuditOK(ctx, "mailbox.share_remove", "mailbox_share", id, nil)
			fmt.Fprintf(os.Stdout, "Share id=%s removed (reconciler converges within ~60s).\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "Share ID (ULID, from `jabali mailbox shares list`)")
	return cmd
}

func rightsFromPreset(s string) (models.Rights, error) {
	switch s {
	case "ro", "":
		return models.Rights{MayRead: true}, nil
	case "rw":
		return models.Rights{
			MayRead:        true,
			MayAddItems:    true,
			MayRemoveItems: true,
		}, nil
	case "admin":
		return models.Rights{
			MayRead:        true,
			MayAddItems:    true,
			MayRemoveItems: true,
			MayCreateChild: true,
			MayRename:      true,
			MayDelete:      true,
			MayAdmin:       true,
			MaySubmit:      true,
		}, nil
	}
	return models.Rights{}, fmt.Errorf("unknown rights preset %q (allowed: ro, rw, admin)", s)
}

func summariseRights(r models.Rights) string {
	if r.MayAdmin {
		return "admin"
	}
	if r.MayAddItems || r.MayRemoveItems {
		return "rw"
	}
	if r.MayRead {
		return "ro"
	}
	return "none"
}
