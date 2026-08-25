// ssl_custom_cmd.go — JAB-128: CLI parity for installing a bring-your-own
// (operator-supplied) SSL cert + key. Mirrors PUT /domains/:id/ssl/custom
// (installCustomSSL in internal/api/ssl.go), which was GUI-only. Unlike the
// other CLI gaps this one is NOT pure direct-DB: writing the root-owned cert
// files is the agent's job (ssl.install_custom), so the command requires the
// shared agent. After the agent installs the files we flip the domain to
// ssl_mode=custom and upsert the ssl_certificates row, exactly as the handler
// does; the running reconciler re-renders the vhost within a tick.

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func newSSLSetCustomCmd() *cobra.Command {
	var (
		domainSpec string
		certFile   string
		keyFile    string
	)
	cmd := &cobra.Command{
		Use:   "set-custom",
		Short: "Install an operator-supplied SSL cert + key (JAB-128)",
		Long: "Install a bring-your-own certificate for a domain. The cert must cover\n" +
			"the domain name. The agent writes the files, the domain switches to\n" +
			"ssl_mode=custom, and the reconciler re-renders the vhost within a tick.",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			if domainSpec == "" || certFile == "" || keyFile == "" {
				return fmt.Errorf("--domain, --cert and --key are all required")
			}
			certPEM, err := os.ReadFile(certFile)
			if err != nil {
				return fmt.Errorf("read --cert %q: %w", certFile, err)
			}
			keyPEM, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("read --key %q: %w", keyFile, err)
			}
			if len(certPEM) == 0 || len(keyPEM) == 0 {
				return fmt.Errorf("--cert and --key must be non-empty PEM files")
			}

			dom, err := resolveDomainSpec(ctx, domainRepoFromDB(), domainSpec)
			if err != nil {
				return err
			}

			// Route through the shared Domain Certificate Lifecycle operation
			// (JAB-345) — the same op the REST handler uses: parse + hostname
			// validation, agent install, the authoritative mode+row finalize on a
			// fresh context, and (REST-only) scheduling. nil scheduler: the CLI is
			// out-of-process from the in-process reconciler, which converges on its
			// next tick (JAB-292 limit).
			res, err := api.InstallCustomDomainCert(ctx, api.CustomCertDeps{
				Agent:    sharedAgent,
				Domains:  domainRepoFromDB(),
				SSLCerts: sslRepoFromDB(),
			}, dom, string(certPEM), string(keyPEM), nil)
			if err != nil {
				cliAuditErr(ctx, "ssl.install_custom", "domain", dom.ID, &dom.UserID)
				return err
			}
			cliAuditOK(ctx, "ssl.install_custom", "domain", dom.ID, &dom.UserID)

			out := map[string]any{
				"domain":     dom.Name,
				"status":     models.SSLStatusCustom,
				"cert_path":  res.CertPath,
				"key_path":   res.KeyPath,
				"expires_at": res.ExpiresAt,
			}
			if jsonOutput {
				return printJSON(out)
			}
			fmt.Printf("Installed custom cert for %s\n  cert:    %s\n  key:     %s\n  expires: %s\n  (reconciler will re-render the vhost)\n",
				dom.Name, res.CertPath, res.KeyPath, res.ExpiresAt.Format(time.RFC3339))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&domainSpec, "domain", "", "domain name or id (required)")
	f.StringVar(&certFile, "cert", "", "path to the certificate PEM file (leaf + chain) (required)")
	f.StringVar(&keyFile, "key", "", "path to the private key PEM file (required)")
	return cmd
}
