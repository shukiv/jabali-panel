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
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// cliParseLeafNotAfter extracts the leaf certificate + its NotAfter from a PEM
// chain. Mirrors parseLeafNotAfter in internal/api/ssl.go. Never logs the input.
func cliParseLeafNotAfter(certPEM []byte) (*x509.Certificate, time.Time, error) {
	rest := certPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("parse certificate: %w", err)
		}
		return leaf, leaf.NotAfter, nil
	}
	return nil, time.Time{}, errors.New("no CERTIFICATE block found in --cert file")
}

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

			// Panel-side validation, same as the handler: parse the leaf and
			// confirm it covers the domain before touching the agent.
			leaf, notAfter, err := cliParseLeafNotAfter(certPEM)
			if err != nil {
				return fmt.Errorf("invalid certificate: %w", err)
			}
			if leaf.VerifyHostname(dom.Name) != nil {
				return fmt.Errorf("certificate does not cover %s", dom.Name)
			}

			raw, err := sharedAgent.Call(ctx, "ssl.install_custom", map[string]any{
				"domain":   dom.Name,
				"cert_pem": string(certPEM),
				"key_pem":  string(keyPEM),
			})
			if err != nil {
				return fmt.Errorf("ssl.install_custom: %w", err)
			}
			var res struct {
				CertPath string `json:"cert_path"`
				KeyPath  string `json:"key_path"`
			}
			if uerr := json.Unmarshal(raw, &res); uerr != nil || res.CertPath == "" {
				return fmt.Errorf("agent returned no cert path")
			}

			// Authoritative: switch mode to custom (sets ssl_enabled shadow too).
			if err := domainRepoFromDB().UpdateSSLMode(ctx, dom.ID, models.SSLModeCustom); err != nil {
				return fmt.Errorf("switch ssl_mode: %w", err)
			}
			// Upsert the cert row (status=custom, paths, parsed expiry).
			cert, _ := sslRepoFromDB().FindByDomainID(ctx, dom.ID)
			if cert == nil {
				newCert := &models.SSLCertificate{
					ID:        ids.NewULID(),
					DomainID:  dom.ID,
					Status:    models.SSLStatusCustom,
					CertPath:  &res.CertPath,
					KeyPath:   &res.KeyPath,
					ExpiresAt: &notAfter,
				}
				if err := sslRepoFromDB().Create(ctx, newCert); err != nil {
					return fmt.Errorf("create cert row: %w", err)
				}
			} else if err := sslRepoFromDB().UpdateCustom(ctx, cert.ID, res.CertPath, res.KeyPath, notAfter); err != nil {
				return fmt.Errorf("update cert row: %w", err)
			}

			cliAuditOK(ctx, "ssl.install_custom", "domain", dom.ID, &dom.UserID)

			out := map[string]any{
				"domain":     dom.Name,
				"status":     models.SSLStatusCustom,
				"cert_path":  res.CertPath,
				"key_path":   res.KeyPath,
				"expires_at": notAfter,
			}
			if jsonOutput {
				return printJSON(out)
			}
			fmt.Printf("Installed custom cert for %s\n  cert:    %s\n  key:     %s\n  expires: %s\n  (reconciler will re-render the vhost)\n",
				dom.Name, res.CertPath, res.KeyPath, notAfter.Format(time.RFC3339))
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&domainSpec, "domain", "", "domain name or id (required)")
	f.StringVar(&certFile, "cert", "", "path to the certificate PEM file (leaf + chain) (required)")
	f.StringVar(&keyFile, "key", "", "path to the private key PEM file (required)")
	return cmd
}
