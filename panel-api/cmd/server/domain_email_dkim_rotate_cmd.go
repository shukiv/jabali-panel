// `jabali domain email-dkim-rotate <domain>` cobra subcommand.
// ADR-0043 §"Rotation": rotation is CLI-triggered in v1, not
// automatic. Operator runs this when:
//   - DKIM key compromise suspected
//   - Periodic rotation policy (3-12 months recommended)
//   - Post-incident credential rotation
//
// Pipeline mirrors domain.email_enable's DNS publish path:
//  1. Resolve domain by name or ID
//  2. agent.domain.email_dkim_rotate — generates fresh keypair,
//     snapshots old key to <domain>.key.old, reloads Stalwart
//  3. UpdateEmailState writes the new dkim_public_key into
//     domains row
//  4. Wipe old M6-managed DNS records + republish so the new
//     DKIM TXT lands at jabali._domainkey.<domain>
//
// On agent failure: bail before DB + DNS writes (no partial state).
// On DB / DNS failure post-agent-success: surface as warning + leave
// keys + DB drifted; operator can re-run to converge.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type domainEmailDKIMRotateResponse struct {
	OldDKIMPublicKey string `json:"old_dkim_public_key,omitempty"`
	NewDKIMPublicKey string `json:"new_dkim_public_key"`
	OldKeyBackupPath string `json:"old_key_backup_path,omitempty"`
}

func newDomainEmailDKIMRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "email-dkim-rotate <domain-name-or-id>",
		Short: "Rotate the domain's DKIM keypair (ADR-0043; operator-driven, not automatic)",
		Args:  cobra.ExactArgs(1),
		Long: `Generates a fresh Ed25519 DKIM keypair for the domain, snapshots
the old private key to /etc/jabali-panel/dkim/<domain>.key.old,
atomically writes the new key, reloads Stalwart, then republishes
the DKIM DNS TXT record so verifiers see the new public key.

ADR-0043 §"Rotation": rotation is CLI-triggered in v1, not
automatic. The .old file persists across reboots; remove it once
DNS propagation is confirmed (operator-managed lifecycle).

Refuses domains where email is not yet enabled (no existing key
to rotate). Run domain email-enable first if needed.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			deps := newDomainEmailDepsFromGlobals()
			dom, err := resolveDomainSpec(ctx, deps.domains, args[0])
			if err != nil {
				return err
			}
			if !dom.EmailEnabled {
				return fmt.Errorf("email not enabled for %s — run 'jabali domain email-enable %s' first", dom.Name, dom.Name)
			}

			// Agent rotation — generates fresh key, snapshots old.
			raw, err := deps.call(ctx, "domain.email_dkim_rotate", map[string]any{
				"domain_name": dom.Name,
			})
			if err != nil {
				return fmt.Errorf("agent domain.email_dkim_rotate: %w", err)
			}
			var resp domainEmailDKIMRotateResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				return fmt.Errorf("agent bad response: %w", err)
			}
			if resp.NewDKIMPublicKey == "" {
				return fmt.Errorf("agent returned empty new DKIM public key")
			}

			// Persist new pubkey in domains row.
			selector := "jabali" // EmailRecordsSelector — kept stable across rotations
			if err := deps.domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
				Enabled:       true,
				DkimSelector:  &selector,
				DkimPublicKey: &resp.NewDKIMPublicKey,
			}); err != nil {
				return fmt.Errorf("update domains row with new dkim_public_key: %w", err)
			}

			// Wipe old M6-managed DNS records + republish so the
			// new DKIM TXT lands at jabali._domainkey.<domain>.
			deleteEmailDNSOnDisableDirect(ctx, deps, dom.ID)
			warnings := syncEmailDNSOnEnableDirect(ctx, deps, dom.ID, selector, resp.NewDKIMPublicKey)

			if jsonOutput {
				return printJSON(map[string]any{
					"domain_name":         dom.Name,
					"old_dkim_public_key": resp.OldDKIMPublicKey,
					"new_dkim_public_key": resp.NewDKIMPublicKey,
					"old_key_backup_path": resp.OldKeyBackupPath,
					"warnings":            warnings,
				})
			}
			cliAuditOK(ctx, "domain.email_dkim_rotate", "domain", dom.Name, &dom.UserID)
			fmt.Printf("DKIM rotated for %s\n", dom.Name)
			if resp.OldDKIMPublicKey != "" {
				fmt.Printf("Old DKIM TXT: %s\n", resp.OldDKIMPublicKey)
			}
			fmt.Printf("New DKIM TXT: %s\n", resp.NewDKIMPublicKey)
			if resp.OldKeyBackupPath != "" {
				fmt.Printf("Old key backup: %s (rm after DNS propagation confirmed)\n", resp.OldKeyBackupPath)
			}
			for _, w := range warnings {
				fmt.Printf("warning: %s\n", w)
			}
			return nil
		},
	}
}
