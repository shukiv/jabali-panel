// `jabali domain email-dkim-rotate <domain>` cobra subcommand.
// ADR-0043 §"Rotation": rotation is CLI-triggered in v1, not
// automatic. Operator runs this when:
//   - DKIM key compromise suspected
//   - Periodic rotation policy (3-12 months recommended)
//   - Post-incident credential rotation
//
// The rotation lifecycle (agent domain.email_dkim_rotate → persist the
// new public key → wipe + republish the M6-managed DNS) lives in
// internal/domainmailops.RotateDKIM (JAB-286), shared with the REST
// handler so the two adapters can't drift on ordering or key handling.
// This command owns only argument parsing, the operator-friendly
// "not enabled" hint, and output shaping.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainmailops"
)

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
			dom, err := resolveDomainSpec(ctx, deps.Domains, args[0])
			if err != nil {
				return err
			}

			res, warnings, err := domainmailops.RotateDKIM(ctx, deps, dom)
			if err != nil {
				// Keep the operator-friendly hint for the common "not enabled"
				// case; the other sentinels carry enough detail on their own.
				if errors.Is(err, domainmailops.ErrEmailNotEnabled) {
					return fmt.Errorf("email not enabled for %s — run 'jabali domain email-enable %s' first", dom.Name, dom.Name)
				}
				return err
			}

			if jsonOutput {
				return printJSON(map[string]any{
					"domain_name":         dom.Name,
					"old_dkim_public_key": res.OldDKIMPublicKey,
					"new_dkim_public_key": res.NewDKIMPublicKey,
					"old_key_backup_path": res.OldKeyBackupPath,
					"warnings":            warnings,
				})
			}
			cliAuditOK(ctx, "domain.email_dkim_rotate", "domain", dom.Name, &dom.UserID)
			fmt.Printf("DKIM rotated for %s\n", dom.Name)
			if res.OldDKIMPublicKey != "" {
				fmt.Printf("Old DKIM TXT: %s\n", res.OldDKIMPublicKey)
			}
			fmt.Printf("New DKIM TXT: %s\n", res.NewDKIMPublicKey)
			if res.OldKeyBackupPath != "" {
				fmt.Printf("Old key backup: %s (rm after DNS propagation confirmed)\n", res.OldKeyBackupPath)
			}
			for _, w := range warnings {
				fmt.Printf("warning: %s\n", w)
			}
			return nil
		},
	}
}
