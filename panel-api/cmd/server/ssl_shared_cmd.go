package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// jabali ssl shared … — CLI for JAB-170 shared wildcard/multi-SAN certificates
// (upload once, serve from many domains). Closes the #128 custom-cert-CLI gap
// for the shared case.

func sharedCertRepoFromDB() repository.SharedCertificateRepository {
	return repository.NewSharedCertificateRepository(sharedDB)
}

// cliHostMatchesSAN mirrors the API's wildcard-aware matcher (x509.VerifyHostname
// semantics): exact, or a single-label wildcard.
func cliHostMatchesSAN(san, host string) bool {
	san = strings.ToLower(strings.TrimSpace(san))
	host = strings.ToLower(strings.TrimSpace(host))
	if san == "" || host == "" {
		return false
	}
	if san == host {
		return true
	}
	if strings.HasPrefix(san, "*.") {
		base := san[2:]
		if i := strings.IndexByte(host, '.'); i > 0 && host[i+1:] == base {
			return true
		}
	}
	return false
}

func cliCertCoversHost(sansJSON *string, host string) bool {
	if sansJSON == nil {
		return false
	}
	var sans []string
	if json.Unmarshal([]byte(*sansJSON), &sans) != nil {
		return false
	}
	for _, s := range sans {
		if cliHostMatchesSAN(s, host) {
			return true
		}
	}
	return false
}

func newSSLSharedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shared",
		Short: "Manage shared wildcard/multi-SAN certificates (upload once, serve many domains)",
	}
	cmd.AddCommand(
		newSSLSharedListCmd(),
		newSSLSharedUploadCmd(),
		newSSLSharedAttachCmd(),
		newSSLSharedDetachCmd(),
		newSSLSharedDeleteCmd(),
	)
	return cmd
}

func newSSLSharedListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List shared certificates + their attached-domain counts",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			repo := sharedCertRepoFromDB()
			certs, err := repo.ListAll(ctx)
			if err != nil {
				return fmt.Errorf("list shared certs: %w", err)
			}
			if jsonOutput {
				return printJSON(certs)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tSANS\tEXPIRES\tATTACHED")
			for i := range certs {
				n, _ := repo.CountAttachedDomains(ctx, certs[i].ID)
				sans := ""
				if certs[i].SANs != nil {
					var s []string
					_ = json.Unmarshal([]byte(*certs[i].SANs), &s)
					sans = strings.Join(s, ",")
				}
				exp := "-"
				if certs[i].ExpiresAt != nil {
					exp = certs[i].ExpiresAt.UTC().Format("2006-01-02")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", certs[i].ID, certs[i].Name, sans, exp, n)
			}
			return w.Flush()
		},
	}
}

func newSSLSharedUploadCmd() *cobra.Command {
	var name, certFile, keyFile string
	cmd := &cobra.Command{
		Use:     "upload --name <name> --cert <fullchain.pem> --key <privkey.pem>",
		Short:   "Upload a server-wide shared certificate (agent validates + writes it)",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" || certFile == "" || keyFile == "" {
				return fmt.Errorf("--name, --cert and --key are required")
			}
			certPEM, err := os.ReadFile(certFile)
			if err != nil {
				return fmt.Errorf("read cert: %w", err)
			}
			keyPEM, err := os.ReadFile(keyFile)
			if err != nil {
				return fmt.Errorf("read key: %w", err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			id := ids.NewULID()
			raw, err := sharedAgent.Call(ctx, "ssl.install_shared", map[string]any{
				"id": id, "cert_pem": string(certPEM), "key_pem": string(keyPEM),
			})
			if err != nil {
				return fmt.Errorf("ssl.install_shared: %w", err)
			}
			var res struct {
				CertPath  string   `json:"cert_path"`
				KeyPath   string   `json:"key_path"`
				SANs      []string `json:"sans"`
				ExpiresAt string   `json:"expires_at"`
			}
			if err := json.Unmarshal(raw, &res); err != nil || res.CertPath == "" {
				return fmt.Errorf("agent returned no cert path")
			}
			var expiresAt *time.Time
			if t, perr := time.Parse(time.RFC3339, res.ExpiresAt); perr == nil {
				expiresAt = &t
			}
			sansJSON, _ := json.Marshal(res.SANs)
			sansStr := string(sansJSON)
			now := time.Now().UTC()
			cert := &models.SharedCertificate{
				ID: id, Name: strings.TrimSpace(name), UserID: nil,
				CertPath: &res.CertPath, KeyPath: &res.KeyPath, SANs: &sansStr,
				ExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now,
			}
			if err := sharedCertRepoFromDB().Create(ctx, cert); err != nil {
				_, _ = sharedAgent.Call(ctx, "ssl.delete_shared", map[string]any{"id": id})
				return fmt.Errorf("store cert row: %w", err)
			}
			if jsonOutput {
				return printJSON(map[string]any{"id": id, "sans": res.SANs, "expires_at": res.ExpiresAt})
			}
			fmt.Printf("Uploaded shared certificate %s (%s)\n  SANs: %s\n  Attach with: jabali ssl shared attach --domain <name> --cert-id %s\n",
				id, name, strings.Join(res.SANs, ", "), id)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "human-readable name")
	cmd.Flags().StringVar(&certFile, "cert", "", "path to fullchain PEM")
	cmd.Flags().StringVar(&keyFile, "key", "", "path to private key PEM")
	return cmd
}

func newSSLSharedAttachCmd() *cobra.Command {
	var domain, certID string
	cmd := &cobra.Command{
		Use:     "attach --domain <name> --cert-id <id>",
		Short:   "Attach a domain to a shared certificate (must cover the domain)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if domain == "" || certID == "" {
				return fmt.Errorf("--domain and --cert-id are required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			dom, err := domainRepoFromDB().FindByName(ctx, domain)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("domain %q not found", domain)
				}
				return err
			}
			cert, err := sharedCertRepoFromDB().FindByID(ctx, certID)
			if err != nil || cert == nil {
				return fmt.Errorf("shared certificate %q not found", certID)
			}
			if !cliCertCoversHost(cert.SANs, dom.Name) {
				return fmt.Errorf("certificate %q does not cover %s (SANs don't match)", certID, dom.Name)
			}
			if err := domainRepoFromDB().SetSharedCertificate(ctx, dom.ID, &certID, models.SSLModeShared); err != nil {
				return fmt.Errorf("attach: %w", err)
			}
			fmt.Printf("Attached %s to shared cert %s. The reconciler will re-render the vhost within a tick.\n", dom.Name, certID)
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain name")
	cmd.Flags().StringVar(&certID, "cert-id", "", "shared certificate id")
	return cmd
}

func newSSLSharedDetachCmd() *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:     "detach --domain <name>",
		Short:   "Detach a domain from its shared certificate (reverts to LE)",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if domain == "" {
				return fmt.Errorf("--domain is required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			dom, err := domainRepoFromDB().FindByName(ctx, domain)
			if err != nil {
				if errors.Is(err, repository.ErrNotFound) {
					return fmt.Errorf("domain %q not found", domain)
				}
				return err
			}
			if err := domainRepoFromDB().SetSharedCertificate(ctx, dom.ID, nil, models.SSLModeLE); err != nil {
				return fmt.Errorf("detach: %w", err)
			}
			fmt.Printf("Detached %s (reverted to Let's Encrypt mode).\n", dom.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "domain name")
	return cmd
}

func newSSLSharedDeleteCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:     "delete --id <id>",
		Short:   "Delete a shared certificate (fails if domains are still attached)",
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			repo := sharedCertRepoFromDB()
			cert, err := repo.FindByID(ctx, id)
			if err != nil || cert == nil {
				return fmt.Errorf("shared certificate %q not found", id)
			}
			n, _ := repo.CountAttachedDomains(ctx, id)
			if n > 0 {
				return fmt.Errorf("%d domain(s) still attached — detach them first", n)
			}
			if err := repo.Delete(ctx, id); err != nil {
				return fmt.Errorf("delete: %w", err)
			}
			_, _ = sharedAgent.Call(ctx, "ssl.delete_shared", map[string]any{"id": id})
			fmt.Printf("Deleted shared certificate %s.\n", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "shared certificate id")
	return cmd
}
