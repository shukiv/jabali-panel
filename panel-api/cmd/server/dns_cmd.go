// Operator DNS CLI (JAB-119). Zone/record management was GUI + REST only
// (panel-api/internal/api/dns.go); a headless operator on a fresh host or in a
// script had no way to list a zone or add/fix a record without the browser.
// This mirrors those handlers over the same repositories and the same
// validation/conflict rules (api.ValidateDNSRecord / api.CheckDNSRecordConflict),
// so a record added here is byte-for-byte what the UI would have written.
//
// The DB is the source of truth. This command does NOT push into PowerDNS
// itself — it writes the row and the running reconciler converges the zone on
// its next tick (same as the UI when its in-process Schedule() nudge is
// missed; see cli_ops.go setDomainEnabledDirect). Mutations are CLI-audited.
//
// The mutation logic lives in repo-injected helpers (dnsRecordAdd/Update/Delete)
// so it is unit-testable with fakes; the cobra RunE wrappers only wire sharedDB
// repos, flags, and output.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func dnsZoneRepoFromDB() repository.DNSZoneRepository {
	return repository.NewDNSZoneRepository(sharedDB)
}

func dnsRecordRepoFromDB() repository.DNSRecordRepository {
	return repository.NewDNSRecordRepository(sharedDB)
}

// dnsResolveZone finds the zone whose apex equals the given domain name.
// Zone.Name is denormalised from domains.name at provisioning time, so a
// zone only exists once the reconciler has provisioned the domain.
func dnsResolveZone(ctx context.Context, zones repository.DNSZoneRepository, domain string) (*models.DNSZone, error) {
	z, err := zones.FindByName(ctx, domain)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("no DNS zone for %q — the domain may not be provisioned yet (the reconciler creates the zone on its next sync)", domain)
		}
		return nil, err
	}
	return z, nil
}

// dnsRecordSpec is the mutable field set a record add/update accepts.
type dnsRecordSpec struct {
	Name     string
	Type     string
	Content  string
	TTL      int
	Priority int
	Enabled  bool
}

// dnsRecordAdd validates + conflict-checks + persists a new record in the zone
// for domain. Returns the created record. Pure of cobra/flags for testability.
func dnsRecordAdd(ctx context.Context, zones repository.DNSZoneRepository, recs repository.DNSRecordRepository, domain string, spec dnsRecordSpec) (*models.DNSRecord, error) {
	zone, err := dnsResolveZone(ctx, zones, domain)
	if err != nil {
		return nil, err
	}
	rec := &models.DNSRecord{
		ID:        ids.NewULID(),
		ZoneID:    zone.ID,
		Name:      spec.Name,
		Type:      spec.Type,
		Content:   spec.Content,
		TTL:       spec.TTL,
		Priority:  spec.Priority,
		Managed:   false,
		IsEnabled: spec.Enabled,
	}
	// ValidateDNSRecord trims/upper-cases and defaults TTL 0 -> 300.
	if err := api.ValidateDNSRecord(rec); err != nil {
		return nil, err
	}
	if err := api.CheckDNSRecordConflict(ctx, recs, zone.ID, rec, ""); err != nil {
		return nil, err
	}
	if err := recs.Create(ctx, rec); err != nil {
		return nil, fmt.Errorf("create record: %w", err)
	}
	return rec, nil
}

// dnsLoadRecordInZone fetches a record and asserts it belongs to domain's zone
// and is not panel-managed (managed rows are owned by a feature, not hand-edits).
func dnsLoadRecordInZone(ctx context.Context, zones repository.DNSZoneRepository, recs repository.DNSRecordRepository, domain, recordID string) (*models.DNSRecord, error) {
	zone, err := dnsResolveZone(ctx, zones, domain)
	if err != nil {
		return nil, err
	}
	rec, err := recs.FindByID(ctx, recordID)
	if err != nil {
		return nil, fmt.Errorf("record not found: %w", err)
	}
	if rec.ZoneID != zone.ID {
		return nil, fmt.Errorf("record %s does not belong to zone %s", recordID, zone.Name)
	}
	if rec.Managed {
		return nil, fmt.Errorf("record %s is managed by the panel (%s); edit the owning feature instead of the raw record",
			rec.ID, managedByLabel(rec.ManagedBy))
	}
	return rec, nil
}

// dnsRecordUpdate mutates only the fields flagged in `set` (content/ttl/priority/
// enabled), re-validates, conflict-checks (excluding self), and persists.
func dnsRecordUpdate(ctx context.Context, zones repository.DNSZoneRepository, recs repository.DNSRecordRepository, domain, recordID string, spec dnsRecordSpec, set map[string]bool) (*models.DNSRecord, error) {
	rec, err := dnsLoadRecordInZone(ctx, zones, recs, domain, recordID)
	if err != nil {
		return nil, err
	}
	if set["content"] {
		rec.Content = spec.Content
	}
	if set["ttl"] {
		rec.TTL = spec.TTL
	}
	if set["priority"] {
		rec.Priority = spec.Priority
	}
	if set["enabled"] {
		rec.IsEnabled = spec.Enabled
	}
	if err := api.ValidateDNSRecord(rec); err != nil {
		return nil, err
	}
	if err := api.CheckDNSRecordConflict(ctx, recs, rec.ZoneID, rec, rec.ID); err != nil {
		return nil, err
	}
	if err := recs.Update(ctx, rec); err != nil {
		return nil, fmt.Errorf("update record: %w", err)
	}
	return rec, nil
}

// dnsRecordDelete removes a non-managed record from domain's zone.
func dnsRecordDelete(ctx context.Context, zones repository.DNSZoneRepository, recs repository.DNSRecordRepository, domain, recordID string) (*models.DNSRecord, error) {
	rec, err := dnsLoadRecordInZone(ctx, zones, recs, domain, recordID)
	if err != nil {
		return nil, err
	}
	if err := recs.Delete(ctx, rec.ID); err != nil {
		return nil, fmt.Errorf("delete record: %w", err)
	}
	return rec, nil
}

func managedByLabel(m *string) string {
	if m == nil {
		return "unknown"
	}
	return *m
}

// ---------- cobra wiring ----------

func newDNSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "DNS zones and records (dns_zones / dns_records): list / add / update / delete",
		Long: "Operator-facing DNS management, mirroring the admin GUI + REST API over the same\n" +
			"validation and conflict rules. Records are written to the DB; the reconciler pushes\n" +
			"them into PowerDNS on its next tick.",
	}
	cmd.AddCommand(newDNSZoneCmd(), newDNSRecordCmd(), newDNSAcmeHookCmd())
	return cmd
}

func newDNSZoneCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "zone", Short: "DNS zones: list / show"}
	cmd.AddCommand(newDNSZoneListCmd(), newDNSZoneShowCmd())
	return cmd
}

func newDNSZoneListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all DNS zones",
		Args:    cobra.NoArgs,
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			zones, err := dnsZoneRepoFromDB().ListAll(ctx)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(zones)
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSERIAL\tENABLED\tID")
			for _, z := range zones {
				fmt.Fprintf(tw, "%s\t%d\t%v\t%s\n", z.Name, z.Serial, z.IsEnabled, z.ID)
			}
			return tw.Flush()
		},
	}
}

func newDNSZoneShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <domain>",
		Short:   "Show a zone and its records",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			zone, err := dnsResolveZone(ctx, dnsZoneRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			recs, err := dnsRecordRepoFromDB().ListByZoneID(ctx, zone.ID)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(map[string]any{"zone": zone, "records": recs})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Zone %s  serial=%d  enabled=%v  id=%s\n",
				zone.Name, zone.Serial, zone.IsEnabled, zone.ID)
			printDNSRecords(recs)
			return nil
		},
	}
}

func newDNSRecordCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "record", Short: "DNS records: list / add / update / delete"}
	cmd.AddCommand(newDNSRecordListCmd(), newDNSRecordAddCmd(), newDNSRecordUpdateCmd(), newDNSRecordDeleteCmd())
	return cmd
}

func printDNSRecords(recs []models.DNSRecord) {
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tTYPE\tCONTENT\tTTL\tPRIO\tENABLED\tMANAGED")
	for _, r := range recs {
		managed := ""
		if r.Managed {
			managed = "managed"
			if r.ManagedBy != nil {
				managed += "(" + *r.ManagedBy + ")"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%v\t%s\n",
			r.ID, r.Name, r.Type, r.Content, r.TTL, r.Priority, r.IsEnabled, managed)
	}
	_ = tw.Flush()
}

func newDNSRecordListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list <domain>",
		Short:   "List records in a zone",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			zone, err := dnsResolveZone(ctx, dnsZoneRepoFromDB(), args[0])
			if err != nil {
				return err
			}
			recs, err := dnsRecordRepoFromDB().ListByZoneID(ctx, zone.ID)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(recs)
			}
			printDNSRecords(recs)
			return nil
		},
	}
}

func newDNSRecordAddCmd() *cobra.Command {
	var (
		name, rtype, content string
		ttl, priority        int
		disabled             bool
	)
	cmd := &cobra.Command{
		Use:     "add <domain> --name <name> --type <TYPE> --content <content>",
		Short:   "Add a record to a zone (same validation + conflict rules as the UI)",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			rec, err := dnsRecordAdd(ctx, dnsZoneRepoFromDB(), dnsRecordRepoFromDB(), args[0], dnsRecordSpec{
				Name: name, Type: rtype, Content: content, TTL: ttl, Priority: priority, Enabled: !disabled,
			})
			if err != nil {
				return err
			}
			cliAuditOK(ctx, "dns.record.add", "dns_record", rec.ID, nil)
			if jsonOutput {
				return printJSON(rec)
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Added %s %s -> %s (ttl=%d) id=%s. The reconciler pushes it into PowerDNS on its next tick.\n",
				rec.Type, rec.Name, rec.Content, rec.TTL, rec.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "record name ('@' for apex)")
	cmd.Flags().StringVar(&rtype, "type", "", "record type: A, AAAA, CNAME, MX, TXT, NS, SRV, CAA")
	cmd.Flags().StringVar(&content, "content", "", "record content (e.g. an IP, hostname, or TXT value)")
	cmd.Flags().IntVar(&ttl, "ttl", 0, "TTL seconds (60-604800; 0 uses default 300)")
	cmd.Flags().IntVar(&priority, "priority", 0, "priority (MX/SRV)")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "create the record disabled (kept in DB, not served)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("content")
	return cmd
}

func newDNSRecordUpdateCmd() *cobra.Command {
	var (
		content       string
		ttl, priority int
		enable        bool
	)
	cmd := &cobra.Command{
		Use:     "update <domain> <record-id>",
		Short:   "Update a record's content / ttl / priority / enabled flag",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			set := map[string]bool{
				"content":  cmd.Flags().Changed("content"),
				"ttl":      cmd.Flags().Changed("ttl"),
				"priority": cmd.Flags().Changed("priority"),
				"enabled":  cmd.Flags().Changed("enabled"),
			}
			rec, err := dnsRecordUpdate(ctx, dnsZoneRepoFromDB(), dnsRecordRepoFromDB(), args[0], args[1],
				dnsRecordSpec{Content: content, TTL: ttl, Priority: priority, Enabled: enable}, set)
			if err != nil {
				return err
			}
			cliAuditOK(ctx, "dns.record.update", "dns_record", rec.ID, nil)
			if jsonOutput {
				return printJSON(rec)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated record %s. The reconciler re-pushes the zone on its next tick.\n", rec.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&content, "content", "", "new content")
	cmd.Flags().IntVar(&ttl, "ttl", 0, "new TTL seconds (60-604800)")
	cmd.Flags().IntVar(&priority, "priority", 0, "new priority (MX/SRV)")
	cmd.Flags().BoolVar(&enable, "enabled", true, "set enabled flag")
	return cmd
}

func newDNSRecordDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:     "delete <domain> <record-id>",
		Short:   "Delete a record from a zone",
		Args:    cobra.ExactArgs(2),
		PreRunE: requireDB,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("re-run with --force to delete record %s", args[1])
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			rec, err := dnsRecordDelete(ctx, dnsZoneRepoFromDB(), dnsRecordRepoFromDB(), args[0], args[1])
			if err != nil {
				return err
			}
			cliAuditOK(ctx, "dns.record.delete", "dns_record", rec.ID, nil)
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted %s %s (%s). The reconciler re-pushes the zone on its next tick.\n",
				rec.Type, rec.Name, rec.ID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}
