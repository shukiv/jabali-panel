package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sslorigin"
)

// ssl_readiness_cmd.go — JAB-224.
//
// Before pointing a migrated domain's DNS at this box, the operator needs one
// answer: will Cloudflare accept what nginx is about to serve? A domain still
// on jabali's self-signed placeholder answers every proxied request with
// "Error 526 Invalid SSL certificate" the instant it cuts over, and there is no
// way to notice beforehand — the origin serves the site perfectly to anyone who
// asks it directly.
//
// Reads the vhost rather than the ssl_certificates table on purpose: a domain
// that never had a certificate issued has no row, and those are precisely the
// domains that will 526.

const defaultNginxSitesDir = "/etc/nginx/sites-enabled"

func newSSLReadinessCmd() *cobra.Command {
	var nginxDir string
	var allDomains bool

	cmd := &cobra.Command{
		Use:   "readiness",
		Short: "Report domains whose origin certificate would fail Cloudflare Full (strict)",
		Long: "Inspects each domain's nginx vhost and reports the certificate it actually serves.\n" +
			"Domains still on the jabali self-signed placeholder will return HTTP 526 through\n" +
			"Cloudflare Full (strict) the moment they cut over. Run this BEFORE moving DNS.\n\n" +
			"Exits non-zero when any domain is not ready, so it can gate a cutover script.",
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			domainsRepo := repository.NewDomainRepository(sharedDB)
			doms, _, err := domainsRepo.List(ctx, repository.ListOptions{Limit: 10000, Sort: "name", Order: "asc"})
			if err != nil {
				return fmt.Errorf("list domains: %w", err)
			}

			var origins []sslorigin.Origin
			for _, d := range doms {
				origins = append(origins, sslorigin.Classify(d.Name, nginxDir))
			}
			sort.Slice(origins, func(i, j int) bool { return origins[i].Domain < origins[j].Domain })

			var notReady []sslorigin.Origin
			for _, o := range origins {
				if !o.Kind.SafeForStrictTLS() {
					notReady = append(notReady, o)
				}
			}

			if jsonOutput {
				if err := printJSON(map[string]any{
					"domains":   origins,
					"total":     len(origins),
					"not_ready": len(notReady),
				}); err != nil {
					return err
				}
				if len(notReady) > 0 {
					return fmt.Errorf("%d domain(s) not ready for Cloudflare Full (strict)", len(notReady))
				}
				return nil
			}

			shown := notReady
			if allDomains {
				shown = origins
			}
			if len(shown) == 0 {
				fmt.Printf("All %d domain(s) ready for Cloudflare Full (strict).\n", len(origins))
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "DOMAIN\tORIGIN\tDETAIL")
			for _, o := range shown {
				detail := o.Detail
				if detail == "" {
					detail = o.CertPath
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", o.Domain, o.Kind, detail)
			}
			if err := w.Flush(); err != nil {
				return err
			}

			if len(notReady) > 0 {
				fmt.Printf("\n%d of %d domain(s) would fail Cloudflare Full (strict).\n", len(notReady), len(origins))
				fmt.Println("Issue a real certificate before cutting DNS over:")
				for _, o := range notReady {
					if o.Kind == sslorigin.KindSelfSigned {
						fmt.Printf("  jabali ssl enable %s\n", o.Domain)
					}
				}
				return fmt.Errorf("%d domain(s) not ready for Cloudflare Full (strict)", len(notReady))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&nginxDir, "nginx-dir", defaultNginxSitesDir, "directory holding the enabled nginx vhosts")
	cmd.Flags().BoolVar(&allDomains, "all", false, "list every domain, not only the ones that would fail")
	return cmd
}
