package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newPanelPrimaryCmd mounts `jabali panel-primary …`. Only child is
// `ensure` — idempotently creates/updates the single is_panel_primary=1
// domain row for the panel hostname. Called by install.sh.
//
// See M6.4 / ADR-0048.
func newPanelPrimaryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "panel-primary",
		Short: "Manage the panel's primary mail domain row (ADR-0048)",
	}
	cmd.AddCommand(newPanelPrimaryEnsureCmd())
	return cmd
}

func newPanelPrimaryEnsureCmd() *cobra.Command {
	var hostname string
	cmd := &cobra.Command{
		Use:   "ensure",
		Short: "Ensure a panel-primary domain row exists for the given hostname",
		Long: `Idempotent primitive called by install.sh:

1. If no is_panel_primary=1 row exists, INSERT one with the given
   hostname owned by the first admin user, email_enabled=1.
2. If one exists with the same hostname, no-op.
3. If one exists with a different hostname, UPDATE the name in place
   (hostname-change case per ADR-0048 Decision 3).
4. If no admin user exists yet, log + exit 0 — caller is expected to
   retry after admin bootstrap completes.`,
		PreRunE: requireConfig,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := initDB(); err != nil {
				return err
			}
			if strings.TrimSpace(hostname) == "" {
				return fmt.Errorf("--hostname is required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			return ensurePanelPrimary(ctx, hostname)
		},
	}
	cmd.Flags().StringVar(&hostname, "hostname", "", "panel hostname (e.g. jabali-panel.local)")
	return cmd
}

// ensurePanelPrimary implements the idempotent decision table.
func ensurePanelPrimary(ctx context.Context, hostname string) error {
	domains := domainRepoFromDB()
	users := userRepo()

	// RFC 6761 reserved TLDs cannot accept real mail — Stalwart refuses
	// `Domain/set` with invalidPatch when given a .local / .test / etc.
	// name, so the reconciler's ensurePanelPrimaryDKIM loop crash-spins
	// forever. Create the row with email_enabled=0 on reserved TLDs and
	// log a hint; the operator flips email on manually (or re-runs
	// install.sh after setting a routable hostname).
	mailOK := hostnameIsMailRoutable(hostname)

	// Find the existing panel-primary row, if any.
	existing, err := domains.FindPanelPrimary(ctx)
	switch {
	case err == nil:
		// Row exists. Same hostname → check email_enabled matches
		// routability; if mismatch (e.g. old install created
		// email_enabled=1 on a .local hostname before the guard
		// landed), reconcile to the correct state.
		if existing.Name == hostname {
			if existing.EmailEnabled != mailOK {
				updates := map[string]interface{}{
					"email_enabled": mailOK,
					"updated_at":    time.Now(),
				}
				if !mailOK {
					// Flipping OFF: clear any DKIM state the reconciler
					// may have partially persisted, so a future flip
					// back ON re-provisions fresh DKIM.
					updates["dkim_selector"] = nil
					updates["dkim_public_key"] = nil
					updates["email_enabled_at"] = nil
				}
				if err := sharedDB.WithContext(ctx).
					Model(&models.Domain{}).
					Where("id = ?", existing.ID).
					Updates(updates).Error; err != nil {
					return fmt.Errorf("update panel-primary email_enabled: %w", err)
				}
				fmt.Printf("panel-primary domain row email_enabled reconciled: %v -> %v (id=%s, name=%s)\n", existing.EmailEnabled, mailOK, existing.ID, existing.Name)
				if !mailOK {
					fmt.Printf("NOTE: %q uses a reserved TLD (RFC 6761); email disabled. Set a routable hostname and re-run to activate mail.\n", hostname)
				}
				return nil
			}
			fmt.Printf("panel-primary domain row already present (id=%s, name=%s)\n", existing.ID, existing.Name)
			return nil
		}
		// Hostname drift — update the existing row's Name column. Use raw
		// SQL because Domain.Update()'s column allowlist doesn't include
		// every field we might need here (it DOES include name, but we're
		// bypassing the UpdatedAt-bump allowlist anyway by going through
		// the db directly — simpler than plumbing a new repo method for
		// a rare case). Also flip email_enabled to match the new
		// hostname's routability: routable→reserved clears DKIM and
		// disables mail (reconciler stops crash-spinning); reserved→
		// routable re-enables, reconciler provisions fresh DKIM next
		// tick.
		oldName := existing.Name
		updates := map[string]interface{}{
			"name":          hostname,
			"email_enabled": mailOK,
			"updated_at":    time.Now(),
		}
		if !mailOK {
			// Clear stale DKIM material so UI shows "not published" and
			// the reconciler's idempotent DKIM-exists guard doesn't skip
			// a future re-enable when hostname flips back to routable.
			updates["dkim_selector"] = nil
			updates["dkim_public_key"] = nil
			updates["email_enabled_at"] = nil
		}
		if err := sharedDB.WithContext(ctx).
			Model(&models.Domain{}).
			Where("id = ?", existing.ID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("update panel-primary hostname: %w", err)
		}
		fmt.Printf("panel-primary domain row hostname updated: %q -> %q (id=%s, email_enabled=%v)\n", oldName, hostname, existing.ID, mailOK)
		fmt.Printf("NOTE: old self-zone and DKIM key for %q are orphaned; see runbook for manual cleanup.\n", oldName)
		if !mailOK {
			fmt.Printf("NOTE: %q uses a reserved TLD (RFC 6761); email disabled. Set a routable hostname and re-run to activate mail.\n", hostname)
		}
		return nil

	case errors.Is(err, repository.ErrPanelPrimaryNotFound):
		// No row yet — create one.
		// Look up the first admin user to own the row. Paging one at a
		// time is fine — install.sh typically runs against an empty (or
		// single-admin) users table. Using a larger page is defensive
		// in case the first page has no admins.
		adminID, err := findFirstAdminID(ctx, users)
		if err != nil {
			return err
		}
		if adminID == "" {
			// No admin yet — install.sh will retry after admin bootstrap.
			fmt.Println("no admin user exists yet; deferring panel-primary domain creation until admin bootstrap completes")
			return nil
		}

		// INSERT. is_panel_primary=true. email_enabled depends on whether
		// the hostname's TLD is mail-routable: Stalwart rejects RFC 6761
		// reserved TLDs (.local, .test, etc.) at Domain/set, so we must
		// NOT ask the reconciler to provision DKIM/Stalwart/vhost for
		// those. The row still exists (delete-protected, visible in
		// Settings → Email with a hint) so the operator sees what's
		// happening.
		now := time.Now()
		d := &models.Domain{
			ID:             ids.NewULID(),
			UserID:         adminID,
			Name:           hostname,
			DocRoot:        "", // panel-primary has no public_html (mail-only).
			IsEnabled:      true,
			IsPanelPrimary: true,
			EmailEnabled:   mailOK,
			SSLEnabled:     true,
			IndexPriority:  "html_first",
			CreatedAt:      now,
			UpdatedAt:      now,
		}
		// GH #1816: the panel's own hostname is an administrator's name.
		if err := domainops.StampOwnership(ctx, domainops.OwnershipDeps{
			Policy: repository.NewDomainOwnershipRepository(sharedDB),
		}, d, true, now.UTC()); err != nil {
			return fmt.Errorf("panel-primary ownership state: %w", err)
		}
		if err := domains.Create(ctx, d); err != nil {
			return fmt.Errorf("create panel-primary domain row: %w", err)
		}
		// Double-check the is_panel_primary=1 invariant — Create doesn't
		// call MarkPanelPrimary, and GORM's Select allowlist on Update
		// doesn't include it either. Belt-and-suspenders: ensure at
		// most one =1 by explicitly calling MarkPanelPrimary (idempotent
		// no-op if the row was created with the flag already set, which
		// it was — this clears any stray =1 on OTHER rows).
		if err := domains.MarkPanelPrimary(ctx, d.ID); err != nil {
			return fmt.Errorf("mark panel-primary: %w", err)
		}
		fmt.Printf("panel-primary domain row created (id=%s, name=%s)\n", d.ID, hostname)
		if !mailOK {
			fmt.Printf("NOTE: %q uses a reserved TLD (RFC 6761); email_enabled=0. Set a routable hostname and re-run install.sh to activate mail.\n", hostname)
		}
		return nil

	default:
		return fmt.Errorf("find panel-primary: %w", err)
	}
}

// hostnameIsMailRoutable rejects RFC 6761 reserved TLDs where Stalwart's
// Domain/set would refuse with invalidPatch. Matches on the final label
// (case-insensitive). Empty hostname → false (defensive; the caller
// already validates emptiness earlier).
func hostnameIsMailRoutable(hostname string) bool {
	host := strings.ToLower(strings.TrimRight(strings.TrimSpace(hostname), "."))
	if host == "" {
		return false
	}
	lastDot := strings.LastIndex(host, ".")
	var tld string
	if lastDot < 0 {
		tld = host
	} else {
		tld = host[lastDot+1:]
	}
	switch tld {
	case "local", "localhost", "invalid", "test", "example":
		return false
	}
	return true
}

// findFirstAdminID scans up to 100 users looking for the first admin.
// Returns "" if none found (install.sh retries later).
func findFirstAdminID(ctx context.Context, users repository.UserRepository) (string, error) {
	list, _, err := users.List(ctx, repository.ListOptions{Limit: 100})
	if err != nil {
		return "", fmt.Errorf("list users: %w", err)
	}
	for _, u := range list {
		if u.IsAdmin {
			return u.ID, nil
		}
	}
	return "", nil
}
