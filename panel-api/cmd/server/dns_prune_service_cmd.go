package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// dns_prune_service_cmd.go — JAB-226 cleanup half.
//
// The import filter now drops cPanel's own service hostnames, but boxes
// migrated BEFORE that fix already carry them in dns_records. Measured on one
// destination: 763 such rows across 69 domains.
//
// They are not cosmetic. The per-domain Let's Encrypt certificate builds its SAN
// from DNS, so each one becomes a hostname that must pass HTTP-01. Until the
// source nameservers stop being authoritative those names still resolve to the
// OLD box and 404 the challenge — and one failed name fails the WHOLE
// certificate, leaving the domain on a self-signed origin. Behind Cloudflare
// Full (strict) that is a 526 the moment the domain cuts over.
//
// So this has to run BEFORE a cutover, not after.

func newDNSPruneServiceCmd() *cobra.Command {
	var apply bool
	var zoneFilter string

	cmd := &cobra.Command{
		Use:   "prune-service-records",
		Short: "Remove imported cPanel service subdomains (cpanel/webmail/whm/…) from DNS",
		Long: "Removes DNS records for hostnames that only ever meant something on cPanel —\n" +
			"cpanel, whm, webdisk, ftp, cpcalendars, cpcontacts, webmail, autoconfig,\n" +
			"autodiscover, mail — which a migration imported before the JAB-226 filter existed.\n\n" +
			"Nothing on jabali serves them, and because the per-domain Let's Encrypt SAN is\n" +
			"built from DNS, each one is a hostname that must pass HTTP-01. Pre-cutover they\n" +
			"still resolve to the OLD box and 404, and one failure fails the whole certificate.\n" +
			"Run this BEFORE cutting a migrated domain over.\n\n" +
			"Dry-run by default. Re-run with --apply to delete.",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 120*time.Second)
			defer cancel()

			zonesRepo := repository.NewDNSZoneRepository(sharedDB)
			recsRepo := repository.NewDNSRecordRepository(sharedDB)

			zones, err := zonesRepo.ListAll(ctx)
			if err != nil {
				return fmt.Errorf("list zones: %w", err)
			}

			type victim struct{ zone, name, typ, id string }
			var victims []victim

			for i := range zones {
				z := zones[i]
				if zoneFilter != "" && !strings.EqualFold(z.Name, zoneFilter) {
					continue
				}
				recs, rErr := recsRepo.ListByZoneID(ctx, z.ID)
				if rErr != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "  ! %s: list records: %v\n", z.Name, rErr)
					continue
				}
				for _, r := range recs {
					// Reuse the SAME predicates the importer now filters on, so
					// the cleanup and the filter can never disagree about what
					// counts as a service hostname.
					if cpanel.IsCPanelServiceRecordName(r.Name) ||
						cpanel.IsMailInfraRecordName(r.Name, r.Type) {
						victims = append(victims, victim{z.Name, r.Name, r.Type, r.ID})
					}
				}
			}

			sort.Slice(victims, func(i, j int) bool {
				if victims[i].zone != victims[j].zone {
					return victims[i].zone < victims[j].zone
				}
				return victims[i].name < victims[j].name
			})

			if len(victims) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No imported cPanel service records found.")
				return nil
			}

			if jsonOutput {
				out := make([]map[string]string, 0, len(victims))
				for _, v := range victims {
					out = append(out, map[string]string{"zone": v.zone, "name": v.name, "type": v.typ})
				}
				return printJSON(map[string]any{"records": out, "total": len(victims), "applied": apply})
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ZONE\tNAME\tTYPE")
			for _, v := range victims {
				fmt.Fprintf(w, "%s\t%s\t%s\n", v.zone, v.name, v.typ)
			}
			_ = w.Flush()

			if !apply {
				fmt.Fprintf(cmd.OutOrStdout(),
					"\n%d record(s) would be removed. This is a DRY RUN — nothing changed.\n"+
						"Re-run with --apply to delete them, then let the reconciler push the zones.\n",
					len(victims))
				return nil
			}

			deleted, failed := 0, 0
			for _, v := range victims {
				if dErr := recsRepo.Delete(ctx, v.id); dErr != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "  ! %s/%s %s: %v\n", v.zone, v.name, v.typ, dErr)
					failed++
					continue
				}
				deleted++
			}
			cliAuditOK(ctx, "dns.prune_service_records", "dns_records", fmt.Sprintf("%d", deleted), nil)
			fmt.Fprintf(cmd.OutOrStdout(), "\nremoved %d record(s), %d failed.\n"+
				"The reconciler rewrites each zone in PowerDNS on its next tick (upsert is a full\n"+
				"replace), so the certificate SAN shrinks once that has run.\n", deleted, failed)
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "actually delete (default is a dry run)")
	cmd.Flags().StringVar(&zoneFilter, "zone", "", "limit to one zone (default: every zone)")
	return cmd
}
