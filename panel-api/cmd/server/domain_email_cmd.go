package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domain_email_cmd.go adds the two M6 domain subcommands to the
// existing `jabali domain` group (wired from newDomainCmd()).
//
// These are thin wrappers around the same agent RPCs + DNS-sync logic
// as the HTTP handlers in internal/api/domain_email.go. We duplicate
// the orchestration here (rather than calling through HTTP) so the CLI
// doesn't need a Kratos session — matches jabali domain enable / delete
// in cli_ops.go. The duplication is ~40 lines and maps 1:1 to the
// handler; the alternative (exporting a service from internal/api)
// would pull all of gin into the CLI build.

const cliDomainEmailAgentTimeout = 30 * time.Second

// domainEmailDeps bundles the repositories + agent-caller the helpers
// need. Having it as a struct means the test can swap everything out
// with fakes in one shot; production path gets the deps from globals
// via newDomainEmailDepsFromGlobals.
type domainEmailDeps struct {
	domains    repository.DomainRepository
	dnsZones   repository.DNSZoneRepository
	dnsRecords repository.DNSRecordRepository
	call       agentCaller
}

func newDomainEmailDepsFromGlobals() domainEmailDeps {
	return domainEmailDeps{
		domains:    domainRepoFromDB(),
		dnsZones:   repository.NewDNSZoneRepository(sharedDB),
		dnsRecords: repository.NewDNSRecordRepository(sharedDB),
		call:       callAgentMailbox, // reuses the mailbox_ops caller — same sharedAgent + timeout.
	}
}

// domainEmailSubcommands returns the two email-* leaves that belong
// under `jabali domain`. Called from newDomainCmd() so ordering stays
// with the other domain subcommands.
func domainEmailSubcommands() []*cobra.Command {
	return []*cobra.Command{
		newDomainEmailEnableCmd(),
		newDomainEmailDisableCmd(),
		newDomainEmailDKIMRotateCmd(),
	}
}

func newDomainEmailEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "email-enable <domain-name-or-id>",
		Short: "Enable email for a domain (generates DKIM + publishes DNS records)",
		Args:  cobra.ExactArgs(1),
		Long: `Flips email_enabled on the domain, runs the agent's domain.email_enable
command to generate an Ed25519 DKIM keypair and register the domain in
Stalwart, then publishes the M6-managed DNS records (DKIM, autoconfig,
autodiscover) into the panel's DNS zone.

Idempotent — calling it twice is harmless.`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			deps := newDomainEmailDepsFromGlobals()
			dom, err := resolveDomainSpec(ctx, deps.domains, args[0])
			if err != nil {
				return err
			}
			resp, warnings, err := enableDomainEmailDirect(ctx, deps, dom)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printJSON(map[string]any{
					"domain_name":     dom.Name,
					"email_enabled":   true,
					"dkim_selector":   resp.DkimSelector,
					"dkim_public_key": resp.DkimPublicKey,
					"warnings":        warnings,
				})
			}
			cliAuditOK(ctx, "domain.email_enable", "domain", dom.ID, &dom.UserID)
			fmt.Printf("Email enabled for %s\n", dom.Name)
			fmt.Printf("DKIM selector:   %s\n", resp.DkimSelector)
			fmt.Printf("DKIM public key: %s\n", resp.DkimPublicKey)
			for _, w := range warnings {
				fmt.Printf("warning: %s\n", w)
			}
			return nil
		},
	}
}

func newDomainEmailDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "email-disable <domain-name-or-id>",
		Short: "Disable email for a domain (keeps DKIM key per ADR-0043)",
		Args:  cobra.ExactArgs(1),
		Long: `Flips email_enabled off, reloads Stalwart, and removes the M6-managed
DNS records. The DKIM private key is preserved so a later re-enable
doesn't re-roll the key and invalidate cached DKIM signatures at
downstream receivers (ADR-0043).`,
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			deps := newDomainEmailDepsFromGlobals()
			dom, err := resolveDomainSpec(ctx, deps.domains, args[0])
			if err != nil {
				return err
			}
			if err := disableDomainEmailDirect(ctx, deps, dom); err != nil {
				return err
			}
			cliAuditOK(ctx, "domain.email_disable", "domain", dom.ID, &dom.UserID)
			fmt.Printf("Email disabled for %s\n", dom.Name)
			return nil
		},
	}
}

// domainEmailEnableResponse mirrors the agent's wire-level reply. Kept
// local to the CLI because it is never reused outside the direct helper.
type domainEmailEnableResponse struct {
	Ok            bool   `json:"ok"`
	DkimSelector  string `json:"dkim_selector"`
	DkimPublicKey string `json:"dkim_public_key"`
}

// enableDomainEmailDirect mirrors the POST /domains/:id/email HTTP
// handler. Returns the agent response + any DNS-sync warnings (empty
// slice if everything published cleanly).
func enableDomainEmailDirect(ctx context.Context, deps domainEmailDeps, dom *models.Domain) (*domainEmailEnableResponse, []string, error) {
	if deps.call == nil {
		return nil, nil, fmt.Errorf("agent not configured")
	}
	raw, err := deps.call(ctx, "domain.email_enable", map[string]any{
		"domain_id":   dom.ID,
		"domain_name": dom.Name,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("agent domain.email_enable: %w", err)
	}
	var resp domainEmailEnableResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, nil, fmt.Errorf("agent bad response: %w", err)
	}
	if !resp.Ok || resp.DkimSelector == "" || resp.DkimPublicKey == "" {
		return nil, nil, fmt.Errorf("agent bad response (ok=%v, selector=%q, pubkey-len=%d)", resp.Ok, resp.DkimSelector, len(resp.DkimPublicKey))
	}

	now := time.Now().UTC()
	selector, pubKey := resp.DkimSelector, resp.DkimPublicKey
	if err := deps.domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
		Enabled:        true,
		DkimSelector:   &selector,
		DkimPublicKey:  &pubKey,
		EmailEnabledAt: &now,
	}); err != nil {
		return nil, nil, fmt.Errorf("update email_enabled row: %w", err)
	}
	warnings := syncEmailDNSOnEnableDirect(ctx, deps, dom.ID, selector, pubKey)
	return &resp, warnings, nil
}

// disableDomainEmailDirect mirrors DELETE /domains/:id/email. Aborts
// before flipping the row when the agent call fails — same ordering
// rule as the HTTP handler, so partial teardown stays impossible.
func disableDomainEmailDirect(ctx context.Context, deps domainEmailDeps, dom *models.Domain) error {
	if deps.call == nil {
		return fmt.Errorf("agent not configured")
	}
	if _, err := deps.call(ctx, "domain.email_disable", map[string]any{
		"domain_id":   dom.ID,
		"domain_name": dom.Name,
	}); err != nil {
		return fmt.Errorf("agent domain.email_disable: %w", err)
	}
	if err := deps.domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
		Enabled:        false,
		EmailEnabledAt: nil,
		// selector + public key left alone (ADR-0043).
	}); err != nil {
		return fmt.Errorf("update email_enabled row: %w", err)
	}
	deleteEmailDNSOnDisableDirect(ctx, deps, dom.ID)
	return nil
}

// syncEmailDNSOnEnableDirect is the CLI-layer mirror of the HTTP
// handler's syncEmailDNSOnEnable. Best-effort per ADR-0013; errors
// become warnings surfaced in the CLI output rather than failing the
// command (the email_enable itself has already succeeded).
func syncEmailDNSOnEnableDirect(ctx context.Context, deps domainEmailDeps, domainID, selector, pubKey string) []string {
	zone, err := deps.dnsZones.FindByDomainID(ctx, domainID)
	if err != nil {
		if isNotFoundErr(err) {
			return []string{"DNS autoconfig skipped: no zone on file for this domain."}
		}
		slog.Error("cli m6 dns: load zone", "domain_id", domainID, "err", err)
		return []string{"DNS autoconfig failed to read the domain's zone."}
	}
	existing, err := deps.dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		slog.Error("cli m6 dns: list records", "zone_id", zone.ID, "err", err)
		return []string{"DNS autoconfig couldn't read existing records."}
	}
	var srv *models.ServerSettings
	if sharedDB != nil {
		srv, _ = repository.NewServerSettingsRepository(sharedDB).Get(ctx)
	}
	intended := dnscompile.BuildEmailRecords(zone.ID, zone.Name, selector, pubKey, srv, ids.NewULID, time.Now().UTC())

	var warnings []string
	for _, rec := range intended {
		if hasExistingM6RecordLocal(existing, rec.Name, rec.Type) {
			continue
		}
		if conflict := findConflictLocal(existing, rec.Name, rec.Type); conflict != nil {
			warnings = append(warnings,
				"A user-edited "+rec.Type+" record at "+rec.Name+
					" is blocking the autoconfig entry. Remove it in the DNS editor to let M6 manage this slot.")
			continue
		}
		r := rec
		if err := deps.dnsRecords.Create(ctx, &r); err != nil {
			slog.Error("cli m6 dns: create record", "zone_id", zone.ID, "name", rec.Name, "type", rec.Type, "err", err)
			warnings = append(warnings, "Failed to publish "+rec.Type+" record at "+rec.Name+".")
		}
	}
	return warnings
}

// deleteEmailDNSOnDisableDirect removes M6-managed records. Silent
// no-op when the zone isn't on file.
func deleteEmailDNSOnDisableDirect(ctx context.Context, deps domainEmailDeps, domainID string) {
	zone, err := deps.dnsZones.FindByDomainID(ctx, domainID)
	if err != nil {
		if !isNotFoundErr(err) {
			slog.Error("cli m6 dns: load zone on disable", "domain_id", domainID, "err", err)
		}
		return
	}
	if err := deps.dnsRecords.DeleteByZoneIDAndManagedBy(ctx, zone.ID, dnscompile.EmailRecordsManagedBy); err != nil {
		slog.Error("cli m6 dns: delete managed records", "zone_id", zone.ID, "err", err)
	}
}

// ---- local mirrors of internal/api package-private helpers ----

// isNotFoundErr is the CLI-side wrapper. repository.ErrNotFound is the
// only ErrNotFound that matters here; we don't need the HTTP handler's
// sentinel registry.
func isNotFoundErr(err error) bool {
	return err != nil && errors.Is(err, repository.ErrNotFound)
}

func hasExistingM6RecordLocal(records []models.DNSRecord, name, typ string) bool {
	for i := range records {
		r := &records[i]
		if r.Name == name && r.Type == typ && r.ManagedBy != nil && *r.ManagedBy == dnscompile.EmailRecordsManagedBy {
			return true
		}
	}
	return false
}

func findConflictLocal(records []models.DNSRecord, name, typ string) *models.DNSRecord {
	for i := range records {
		r := &records[i]
		if r.Name != name || r.Type != typ {
			continue
		}
		if r.ManagedBy != nil && *r.ManagedBy == dnscompile.EmailRecordsManagedBy {
			continue
		}
		return r
	}
	return nil
}
