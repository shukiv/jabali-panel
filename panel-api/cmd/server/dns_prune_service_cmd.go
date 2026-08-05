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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
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

			mailOn, mErr := mailEnabledZones(ctx)
			if mErr != nil {
				// Without this set the command cannot tell jabali's own mail
				// records from imported ones, and the difference is whether it
				// deletes working autodiscovery. Refuse rather than guess.
				return fmt.Errorf("read mail-enabled domains: %w", mErr)
			}

			type victim struct{ zone, name, typ, id string }
			var victims []victim
			var kept []victim
			var ours []victim

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

				// Names this zone's MX records point at, and the names that have
				// an address record of their own. See mxTargetLabels.
				mxTargets, addressed := mailRoutingNames(z.Name, recs)

				for _, r := range recs {
					// Reuse the SAME predicates the importer now filters on, so
					// the cleanup and the filter can never disagree about what
					// counts as a service hostname.
					if !cpanel.IsCPanelServiceRecordName(r.Name) &&
						!cpanel.IsMailInfraRecordName(r.Name, r.Type) {
						continue
					}
					if mailOn[strings.ToLower(z.Name)] && jabaliPublishesMailRecord(r.Name, r.Type) {
						ours = append(ours, victim{z.Name, r.Name, r.Type, r.ID})
						continue
					}
					if wouldDanglingMX(r.Name, r.Type, mxTargets, addressed) {
						kept = append(kept, victim{z.Name, r.Name, r.Type, r.ID})
						continue
					}
					victims = append(victims, victim{z.Name, r.Name, r.Type, r.ID})
				}
			}

			sort.Slice(victims, func(i, j int) bool {
				if victims[i].zone != victims[j].zone {
					return victims[i].zone < victims[j].zone
				}
				return victims[i].name < victims[j].name
			})

			sort.Slice(kept, func(i, j int) bool {
				if kept[i].zone != kept[j].zone {
					return kept[i].zone < kept[j].zone
				}
				return kept[i].name < kept[j].name
			})

			sort.Slice(ours, func(i, j int) bool {
				if ours[i].zone != ours[j].zone {
					return ours[i].zone < ours[j].zone
				}
				return ours[i].name < ours[j].name
			})

			// Never silent about what was skipped: a cleanup that quietly leaves
			// rows behind reads as "done" when it is not, and the operator needs
			// to know these names still count toward the certificate SAN.
			reportKept := func() {
				if len(ours) > 0 {
					zn := map[string]bool{}
					for _, o := range ours {
						zn[o.zone] = true
					}
					fmt.Fprintf(cmd.OutOrStdout(),
						"\nSKIPPED %d record(s) across %d mail-enabled zone(s): jabali publishes these\n"+
							"itself (MX, mail, autodiscovery, DKIM/SPF/DMARC). They are not imported cPanel\n"+
							"leftovers, and deleting them would break mail-client autoconfiguration with\n"+
							"nothing to republish them.\n", len(ours), len(zn))
				}
				if len(kept) == 0 {
					return
				}
				zonesKept := map[string]bool{}
				for _, k := range kept {
					zonesKept[k.zone] = true
				}
				fmt.Fprintf(cmd.OutOrStdout(),
					"\nKEPT %d record(s) across %d zone(s): an MX in the same zone points at them,\n"+
						"and no other address record provides the name. Deleting them would leave the MX\n"+
						"unresolvable and external senders could not deliver.\n"+
						"Enable mail on these domains in the panel (which publishes jabali's own\n"+
						"mail A record), then re-run — they will be prunable once the name has a\n"+
						"replacement.\n", len(kept), len(zonesKept))
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "KEPT ZONE\tNAME\tTYPE")
				for _, k := range kept {
					fmt.Fprintf(w, "%s\t%s\t%s\n", k.zone, k.name, k.typ)
				}
				_ = w.Flush()
			}

			if len(victims) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No imported cPanel service records found.")
				reportKept()
				return nil
			}

			if jsonOutput {
				out := make([]map[string]string, 0, len(victims))
				for _, v := range victims {
					out = append(out, map[string]string{"zone": v.zone, "name": v.name, "type": v.typ})
				}
				keptOut := make([]map[string]string, 0, len(kept))
				for _, k := range kept {
					keptOut = append(keptOut, map[string]string{"zone": k.zone, "name": k.name, "type": k.typ})
				}
				oursOut := make([]map[string]string, 0, len(ours))
				for _, o := range ours {
					oursOut = append(oursOut, map[string]string{"zone": o.zone, "name": o.name, "type": o.typ})
				}
				return printJSON(map[string]any{
					"records": out, "total": len(victims), "applied": apply,
					"kept_mx_targets": keptOut, "kept_total": len(kept),
					"skipped_jabali_published": oursOut, "skipped_total": len(ours),
				})
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
				reportKept()
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
			reportKept()
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "actually delete (default is a dry run)")
	cmd.Flags().StringVar(&zoneFilter, "zone", "", "limit to one zone (default: every zone)")
	return cmd
}

// mailEnabledZones returns the lowercased names of domains that have mail
// enabled in the panel.
//
// On those, jabali publishes the mail infrastructure itself (dnscompile's
// bootstrap + email records), so the same hostnames the importer skips are the
// ones WE authored. Deleting them is not a cleanup, it is breaking live mail.
func mailEnabledZones(ctx context.Context) (map[string]bool, error) {
	var names []string
	if err := sharedDB.WithContext(ctx).
		Model(&models.Domain{}).
		Where("email_enabled = ?", true).
		Pluck("name", &names).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[strings.ToLower(strings.TrimSuffix(n, "."))] = true
	}
	return out, nil
}

// jabaliPublishesMailRecord reports whether, on a mail-enabled domain, this
// record is one jabali publishes for itself.
//
// The list is dnscompile's: the mail A/AAAA and MX from bootstrap, and the
// autodiscovery CNAMEs, client-service SRVs, DKIM/SPF/DMARC/TLS-RPT TXTs from
// the email record set. That is everything IsMailInfraRecordName matches EXCEPT
// webmail — jabali publishes no webmail record, so an imported one is still a
// dead cPanel hostname and still belongs in the SAN cleanup.
//
// Measured on a production host: all 26 zones the prune targeted were
// mail-enabled, and every remaining record was one of ours — 26 mail A records
// (held back by the dangling-MX guard) plus 51 autoconfig/autodiscover CNAMEs
// pointing at them, at the TTL dnscompile writes. The honest answer for that
// box was zero records to remove, not 51. Nothing would have republished them:
// the zone push is a full replace from dns_records.
func jabaliPublishesMailRecord(name, typ string) bool {
	if strings.EqualFold(name, "webmail") {
		return false
	}
	return cpanel.IsMailInfraRecordName(name, typ)
}

// mailRoutingNames returns, for one zone, the set of labels this zone's MX
// records point at, and the set of labels that own an A or AAAA record.
//
// Both are zone-relative labels ("mail"), because that is how records are
// stored, while MX content is an FQDN ("mail.example.com" or with a trailing
// dot). An MX target outside the zone is irrelevant here — deleting a record in
// THIS zone cannot break it.
func mailRoutingNames(zone string, recs []models.DNSRecord) (mxTargets, addressed map[string]bool) {
	mxTargets = map[string]bool{}
	addressed = map[string]bool{}
	suffix := "." + strings.ToLower(strings.TrimSuffix(zone, "."))

	for _, r := range recs {
		switch strings.ToUpper(r.Type) {
		case "MX":
			// Content may carry a priority prefix on some backends; the target
			// is the last field either way.
			f := strings.Fields(strings.TrimSpace(r.Content))
			if len(f) == 0 {
				continue
			}
			t := strings.ToLower(strings.TrimSuffix(f[len(f)-1], "."))
			switch {
			case t == strings.ToLower(strings.TrimSuffix(zone, ".")):
				mxTargets["@"] = true
			case strings.HasSuffix(t, suffix):
				mxTargets[strings.TrimSuffix(t, suffix)] = true
			}
		case "A", "AAAA":
			addressed[strings.ToLower(r.Name)] = true
		}
	}
	return mxTargets, addressed
}

// wouldDanglingMX reports whether deleting this record would leave an MX in the
// same zone pointing at a name that no longer resolves.
//
// This is the difference between a cosmetic cleanup and an outage. On a real
// migrated host, 69 of 71 domains had their ONLY mail.<zone> record as the
// cPanel-imported CNAME, and every one of those zones had MX -> mail.<zone>.
// Deleting them as "cPanel service records" would have made the MX target
// unresolvable, and external senders cannot deliver to a domain whose MX does
// not resolve. The certificate problem this command exists to fix is worth
// fixing; it is not worth trading for bounced mail.
//
// A record is safe to delete when something else still provides the name — an A
// or AAAA that survives (the address records are never service-hostname
// matches, so they are not in the deletion set).
func wouldDanglingMX(name, typ string, mxTargets, addressed map[string]bool) bool {
	n := strings.ToLower(name)
	if !mxTargets[n] {
		return false
	}
	// Deleting an address record for an MX target is only safe if another
	// address record for the same name remains; we cannot tell them apart here,
	// so treat any address record for an MX target as load-bearing.
	switch strings.ToUpper(typ) {
	case "A", "AAAA":
		return true
	case "CNAME":
		// A CNAME is how cPanel points mail.<zone> at the apex. Safe to drop
		// only if the name also has its own address record to fall back on.
		return !addressed[n]
	}
	return false
}
