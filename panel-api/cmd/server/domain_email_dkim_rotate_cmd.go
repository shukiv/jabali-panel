// `jabali domain email-dkim-rotate <domain>` cobra subcommand.
// ADR-0043 §"Rotation": rotation is CLI-triggered in v1, not
// automatic. Operator runs this when:
//   - DKIM key compromise suspected
//   - Periodic rotation policy (3-12 months recommended)
//   - Post-incident credential rotation
//
// The rotation lifecycle (agent domain.email_dkim_rotate → persist the
// new public keys → wipe + republish the M6-managed DNS) lives in
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
		Short: "Rotate the domain's DKIM keys (operator-driven, not automatic)",
		Args:  cobra.ExactArgs(1),
		Long: `Gives the domain new DKIM keys, Ed25519 (selector jabali) and RSA
(selector jabalirsa): the mail server signs with them from the next
message, and the jabali._domainkey and jabalirsa._domainkey TXT
records are republished with the new public keys. A zone hosted
elsewhere needs the printed TXT values added at its DNS provider.

The selectors stay the same. Receivers that cached the old TXT
records fail the new signatures until the records expire, and a
message signed before the rotation and still waiting for a retry
fails at its receiver.

The previous private keys are kept in /etc/jabali-panel/dkim as
<domain>.key.old and <domain>.rsa.key.old. They are a record, not a
rollback: putting one back does not change the key the mail server
signs with.

If the RSA key cannot be rotated, the Ed25519 one still is and a
warning says so. Refuses domains where email is not yet enabled
(no existing key to rotate). Run domain email-enable first if needed.`,
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
				if errors.Is(err, domainmailops.ErrPersistFailed) {
					// The mail server already signs with the new keys.
					return fmt.Errorf("%w\nthe mail server signs with the new DKIM keys, but they could not be saved; publish them by hand:\n  jabali._domainkey TXT: %s\n  jabalirsa._domainkey TXT: %s",
						err, res.NewDKIMPublicKey, res.NewDKIMRSAPublicKey)
				}
				return err
			}

			if jsonOutput {
				return printJSON(map[string]any{
					"domain_name":             dom.Name,
					"old_dkim_public_key":     res.OldDKIMPublicKey,
					"new_dkim_public_key":     res.NewDKIMPublicKey,
					"old_key_backup_path":     res.OldKeyBackupPath,
					"old_dkim_rsa_public_key": res.OldDKIMRSAPublicKey,
					"new_dkim_rsa_public_key": res.NewDKIMRSAPublicKey,
					"warnings":                domainmailops.WarningMessages(warnings),
				})
			}
			cliAuditOK(ctx, "domain.email_dkim_rotate", "domain", dom.Name, &dom.UserID)
			fmt.Printf("DKIM rotated for %s\n", dom.Name)
			if res.OldDKIMPublicKey != "" {
				fmt.Printf("Old DKIM TXT: %s\n", res.OldDKIMPublicKey)
			}
			fmt.Printf("New DKIM TXT: %s\n", res.NewDKIMPublicKey)
			if res.NewDKIMRSAPublicKey != "" {
				if res.OldDKIMRSAPublicKey != "" {
					fmt.Printf("Old RSA DKIM TXT: %s\n", res.OldDKIMRSAPublicKey)
				}
				fmt.Printf("New RSA DKIM TXT: %s\n", res.NewDKIMRSAPublicKey)
			}
			if res.OldKeyBackupPath != "" {
				fmt.Printf("Previous private key kept at: %s\n", res.OldKeyBackupPath)
			}
			for _, w := range warnings {
				fmt.Printf("warning: %s\n", w.Message())
			}
			return nil
		},
	}
}
