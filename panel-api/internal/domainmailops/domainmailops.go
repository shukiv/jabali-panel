// Package domainmailops is the shared Domain Mail enable/disable lifecycle
// (JAB-288): one implementation of agent provisioning (domain.email_enable /
// domain.email_disable — the agent generates the Ed25519 DKIM keypair and
// registers/deregisters the domain in Stalwart), state persistence
// (domains.UpdateEmailState), managed-only M6 DNS writes, optional SSL SAN
// scheduling, and best-effort warnings — that the REST handler
// (internal/api/domain_email.go), the domain-creation auto-enable path, and the
// operator CLI (cmd/server/domain_email_cmd.go) all route through, so the
// orchestration order can't drift between adapters.
//
// Before this module the three adapters each carried their own copy of the
// enable/disable flow; the CLI copy omitted the SSL SAN cert-row flip the REST
// path performs (self-healing via ReconcileSSLSANDrift, so a latency gap, not a
// correctness one). Consolidating removes the drift risk.
//
// Authorization and response shaping stay in the adapters (the REST handler
// checks claims and builds the DNS-hint list; the CLI is admin-by-construction
// and prints). Every entry point takes an already-loaded, already-authorized
// *models.Domain and mutates it in place on a successful enable so the caller
// can echo the new state without re-fetching.
//
// RotateDKIM (JAB-286) shares the DKIM-rotation lifecycle the REST handler and
// operator CLI both drove: agent domain.email_dkim_rotate → persist the new
// public key → wipe + republish the M6-managed DNS.
//
// Not yet routed through here: the reconciler's per-tick provisioning
// (internal/reconciler/panel_primary_dkim.go — ensurePanelPrimaryDKIM /
// ensureTenantEmailEnabled + its own managed-DNS convergence), which carries
// its own reserved-TLD and already-provisioned guards and a distinct panel-cert
// lineage. JAB-288 and JAB-286 stay module-parents until that fourth caller is
// migrated.
package domainmailops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// agentTimeout bounds the agent call budget for email_enable/disable. Both
// talk to Stalwart over the panel's admin loopback and (on enable) generate an
// Ed25519 DKIM keypair — all fast. 30s is generous. Matches the value the REST
// handler and reconciler used before extraction.
const agentTimeout = 30 * time.Second

// rotateTimeout bounds the agent budget for domain.email_dkim_rotate. Longer
// than agentTimeout: rotation generates a fresh Ed25519 keypair, snapshots the
// old private key to disk, and reloads Stalwart — heavier than a plain enable.
// Matches the 60s the CLI already wrapped its rotate call in; the REST rotate
// had no cap at all, so routing through here adds a benign upper bound on the
// request path.
const rotateTimeout = 60 * time.Second

// CallFunc runs one agent command. Both adapters' agent callers already have
// this exact signature (agent.AgentInterface.Call and the CLI's agentCaller),
// so each passes its caller directly with no wrapper. Keeping the module on a
// func rather than agent.AgentInterface avoids pulling the agent package's
// concrete client into callers that only have the narrow func.
type CallFunc func(ctx context.Context, command string, params any) (json.RawMessage, error)

// SSLScheduler schedules an immediate domain reconcile. Declared here (rather
// than imported from internal/api) so this neutral module has no dependency on
// the api package that imports it.
type SSLScheduler interface {
	Schedule(domainID string)
}

// Deps is the set of collaborators the enable/disable flow needs. DNS repos,
// SSLCerts, and SSLReconciler are all optional — a nil DNS repo skips the M6
// DNS sync (a config without PowerDNS), and nil SSL deps skip the SAN cert-row
// flip (the CLI relies on ReconcileSSLSANDrift instead; unit tests wire
// neither).
type Deps struct {
	Domains        repository.DomainRepository
	DNSZones       repository.DNSZoneRepository
	DNSRecords     repository.DNSRecordRepository
	ServerSettings repository.ServerSettingsRepository
	SSLCerts       repository.SSLCertificateRepository
	SSLReconciler  SSLScheduler
	Call           CallFunc
}

// Sentinel errors so adapters can map a failure back to their transport: the
// REST handler turns them into 5xx/502 via errors.Is, the CLI into an operator
// message. Wrapped with %w so errors.Is works through them.
var (
	ErrAgentUnconfigured = errors.New("domainmailops: agent unconfigured")
	ErrAgentFailed       = errors.New("domainmailops: agent call failed")
	ErrAgentBadResponse  = errors.New("domainmailops: agent returned bad response")
	// ErrEmailNotEnabled is returned by RotateDKIM when the domain has no
	// email enabled — there is no existing key to rotate.
	ErrEmailNotEnabled = errors.New("domainmailops: email not enabled")
	// ErrPersistFailed wraps a repository failure while writing the rotated
	// key, so the adapter can tell a persistence fault from an agent one.
	ErrPersistFailed = errors.New("domainmailops: persist new dkim key failed")
)

// Enable runs the shared "flip email on for this domain" flow: invokes
// domain.email_enable on the agent (which generates the Ed25519 DKIM keypair
// and registers the domain in Stalwart), persists the new state via
// UpdateEmailState, best-effort-syncs the M6 DNS records, and — when SSL deps
// are wired — flips any issued/self-signed cert row to renewing so the next
// reconciler tick re-issues with mail.<domain> + autoconfig.<domain> SANs.
//
// On nil err the passed dom is mutated to reflect the new state so the caller
// can echo it back without re-fetching. On non-nil err nothing has been
// written to the DB. Wrapped errors come from ErrAgent{Unconfigured,Failed,
// BadResponse}. Returns selector, public key, and accumulated DNS/SSL warnings.
func Enable(ctx context.Context, d Deps, dom *models.Domain) (selector, pubKey string, warnings []string, err error) {
	if d.Call == nil {
		return "", "", nil, ErrAgentUnconfigured
	}
	agentCtx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	raw, err := d.Call(agentCtx, "domain.email_enable", map[string]any{
		"domain_id":   dom.ID,
		"domain_name": dom.Name,
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: %v", ErrAgentFailed, err)
	}
	var resp struct {
		Ok            bool   `json:"ok"`
		DKIMSelector  string `json:"dkim_selector"`
		DKIMPublicKey string `json:"dkim_public_key"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", nil, fmt.Errorf("%w: unmarshal: %v", ErrAgentBadResponse, err)
	}
	if !resp.Ok || resp.DKIMSelector == "" || resp.DKIMPublicKey == "" {
		return "", "", nil, fmt.Errorf("%w: ok=%v selector=%q pubkey-len=%d",
			ErrAgentBadResponse, resp.Ok, resp.DKIMSelector, len(resp.DKIMPublicKey))
	}

	selector, pubKey = resp.DKIMSelector, resp.DKIMPublicKey
	now := time.Now().UTC()
	if err := d.Domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
		Enabled:        true,
		DkimSelector:   &selector,
		DkimPublicKey:  &pubKey,
		EmailEnabledAt: &now,
	}); err != nil {
		return "", "", nil, fmt.Errorf("update email_enabled row: %w", err)
	}

	// DNS sync is best-effort. Warnings flow back to the caller; a DNS-side
	// failure does NOT roll back the email_enabled flip (the mailbox system
	// still works without the convenience records).
	warnings = SyncManagedDNSOnEnable(ctx, d, dom.ID, selector, pubKey)

	// Mutate caller's Domain struct so the response reflects new state.
	dom.EmailEnabled = true
	dom.DkimSelector = &selector
	dom.DkimPublicKey = &pubKey
	dom.EmailEnabledAt = &now

	// M6.1: trigger SSL re-issuance so mail.<domain> + autoconfig.<domain>
	// land on the cert. Best-effort — any failure is logged and added to
	// warnings, never blocks the email_enabled flip. Skipped when SSL deps
	// aren't wired (the CLI path; ReconcileSSLSANDrift covers it on a tick).
	if msg := triggerSSLSANExpansion(ctx, d, dom); msg != "" {
		warnings = append(warnings, msg)
	}

	return selector, pubKey, warnings, nil
}

// Disable runs the shared "flip email off" flow: the agent is authoritative
// for the Stalwart-side teardown, so on an agent failure the DB row is left
// alone (email_enabled=1) for the operator to retry — the same delete-ordering
// rule as mailbox.delete. On success it flips the row off (keeping the DKIM
// key material per ADR-0043 so a re-enable doesn't re-roll the key) and removes
// the M6-managed DNS records best-effort.
func Disable(ctx context.Context, d Deps, dom *models.Domain) error {
	if d.Call == nil {
		return ErrAgentUnconfigured
	}
	agentCtx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()
	if _, err := d.Call(agentCtx, "domain.email_disable", map[string]any{
		"domain_id":   dom.ID,
		"domain_name": dom.Name,
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrAgentFailed, err)
	}
	if err := d.Domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
		Enabled:        false,
		EmailEnabledAt: nil,
		// Deliberately pass nil selector/pubkey — disable keeps the DKIM
		// material so re-enable doesn't re-roll the key (ADR-0043).
	}); err != nil {
		return fmt.Errorf("update email_enabled row: %w", err)
	}

	// Clean up M6-managed DNS records. M4 bootstrap records (A/MX/SPF/DMARC
	// with ManagedBy=NULL) and any user-edited rows survive. Best-effort — the
	// ManagedBy-scoped WHERE can never hit the wrong rows even if retried.
	DeleteManagedDNSOnDisable(ctx, d, dom.ID)

	// Mutate caller's struct so a subsequent read reflects the new state.
	dom.EmailEnabled = false
	dom.EmailEnabledAt = nil
	return nil
}

// RotateResult carries the agent's DKIM rotation output for the adapter to
// echo. Selector is the stable "jabali" selector; the old public key + backup
// path are informational (the operator removes the .old key after DNS
// propagation).
type RotateResult struct {
	Selector         string
	OldDKIMPublicKey string
	NewDKIMPublicKey string
	OldKeyBackupPath string
}

// RotateDKIM rotates the domain's DKIM keypair (ADR-0043 §"Rotation";
// operator-driven, not automatic). The agent generates a fresh Ed25519 key,
// snapshots the old private key, and reloads Stalwart; we persist the new
// public key under the stable "jabali" selector, then wipe + republish the
// M6-managed DNS so the new DKIM TXT replaces the old at
// jabali._domainkey.<domain>. The reconciler re-converges on its next tick too,
// so the immediate republish is a belt-and-suspenders UI refresh.
//
// Ordering mirrors Enable and is the whole point of the guard sequence: agent
// first (a failed rotate or an incomplete response leaves the DB row and DNS
// untouched — the last usable key stays authoritative), then persist, then
// best-effort DNS. On nil err the passed dom is mutated (selector + public key)
// so the caller can echo the new state without re-fetching.
//
// The persist step writes the domain's *existing* email_enabled_at back
// unchanged: UpdateEmailState always assigns that column from the struct, so a
// zero value would NULL it on every rotation even though email stays enabled.
// Rotation is timestamp-neutral.
//
// Wrapped errors: ErrEmailNotEnabled, ErrAgentUnconfigured, ErrAgentFailed,
// ErrAgentBadResponse, ErrPersistFailed. DNS conflict / missing-zone stay
// best-effort []string warnings (same contract as Enable).
func RotateDKIM(ctx context.Context, d Deps, dom *models.Domain) (RotateResult, []string, error) {
	if !dom.EmailEnabled {
		return RotateResult{}, nil, ErrEmailNotEnabled
	}
	if d.Call == nil {
		return RotateResult{}, nil, ErrAgentUnconfigured
	}
	agentCtx, cancel := context.WithTimeout(ctx, rotateTimeout)
	defer cancel()
	raw, err := d.Call(agentCtx, "domain.email_dkim_rotate", map[string]any{
		"domain_name": dom.Name,
	})
	if err != nil {
		return RotateResult{}, nil, fmt.Errorf("%w: %v", ErrAgentFailed, err)
	}
	var resp struct {
		OldDKIMPublicKey string `json:"old_dkim_public_key"`
		NewDKIMPublicKey string `json:"new_dkim_public_key"`
		OldKeyBackupPath string `json:"old_key_backup_path"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return RotateResult{}, nil, fmt.Errorf("%w: unmarshal: %v", ErrAgentBadResponse, err)
	}
	if resp.NewDKIMPublicKey == "" {
		// Incomplete response — never overwrite the last usable key.
		return RotateResult{}, nil, fmt.Errorf("%w: no new dkim public key", ErrAgentBadResponse)
	}

	selector := dnscompile.EmailRecordsSelector
	pubKey := resp.NewDKIMPublicKey
	if err := d.Domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
		Enabled:        true,
		DkimSelector:   &selector,
		DkimPublicKey:  &pubKey,
		EmailEnabledAt: dom.EmailEnabledAt, // preserve — see doc comment
	}); err != nil {
		return RotateResult{}, nil, fmt.Errorf("%w: %v", ErrPersistFailed, err)
	}

	// Mutate caller's struct so the response reflects the rotated key.
	dom.DkimSelector = &selector
	dom.DkimPublicKey = &pubKey

	// Wipe the old M6-managed records + republish so the new DKIM TXT replaces
	// the old one immediately (reconciler re-converges on its next tick too).
	DeleteManagedDNSOnDisable(ctx, d, dom.ID)
	warnings := SyncManagedDNSOnEnable(ctx, d, dom.ID, selector, pubKey)

	return RotateResult{
		Selector:         selector,
		OldDKIMPublicKey: resp.OldDKIMPublicKey,
		NewDKIMPublicKey: pubKey,
		OldKeyBackupPath: resp.OldKeyBackupPath,
	}, warnings, nil
}

// SyncManagedDNSOnEnable publishes the M6 DNS record set (DKIM TXT, autoconfig/
// autodiscover, SRV, etc.) into the domain's zone. Best-effort: returns a slice
// of human-readable warning messages (missing zone, user-edited conflict, hard
// error) for the caller to surface; never returns an error. Exported so the
// REST DKIM-rotate path can republish through the same code.
func SyncManagedDNSOnEnable(ctx context.Context, d Deps, domainID, selector, pubKey string) []string {
	if d.DNSZones == nil || d.DNSRecords == nil {
		// DNS repos not wired — panel running without PowerDNS integration.
		return nil
	}
	zone, err := d.DNSZones.FindByDomainID(ctx, domainID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return []string{"DNS autoconfig skipped: no zone on file for this domain."}
		}
		slog.Error("m6 dns: load zone", "domain_id", domainID, "err", err)
		return []string{"DNS autoconfig failed to read the domain's zone."}
	}

	existing, err := d.DNSRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		slog.Error("m6 dns: list records", "zone_id", zone.ID, "err", err)
		return []string{"DNS autoconfig couldn't read existing records."}
	}
	var srv *models.ServerSettings
	if d.ServerSettings != nil {
		srv, _ = d.ServerSettings.Get(ctx)
	}
	intended := dnscompile.BuildEmailRecords(zone.ID, zone.Name, selector, pubKey, srv, ids.NewULID, time.Now().UTC())

	var warnings []string
	for _, rec := range intended {
		// Skip if we've already placed this exact M6 row on a prior enable
		// (idempotent). Match by (name, type, managed_by).
		if hasExistingM6Record(existing, rec.Name, rec.Type) {
			continue
		}
		if conflict := findConflict(existing, rec.Name, rec.Type); conflict != nil {
			warnings = append(warnings,
				"A user-edited "+rec.Type+" record at "+rec.Name+" is blocking the "+
					"autoconfig entry. Remove it in the DNS editor or accept M6 may overwrite.")
			continue
		}
		r := rec
		if err := d.DNSRecords.Create(ctx, &r); err != nil {
			slog.Error("m6 dns: create record", "zone_id", zone.ID, "name", rec.Name, "type", rec.Type, "err", err)
			warnings = append(warnings, "Failed to publish "+rec.Type+" record at "+rec.Name+".")
		}
	}
	return warnings
}

// DeleteManagedDNSOnDisable removes M6-managed records (by managed_by marker).
// Silent no-op when DNS repos aren't wired or the zone isn't on file. Exported
// so the REST DKIM-rotate path can wipe-then-republish through the same code.
func DeleteManagedDNSOnDisable(ctx context.Context, d Deps, domainID string) {
	if d.DNSZones == nil || d.DNSRecords == nil {
		return
	}
	zone, err := d.DNSZones.FindByDomainID(ctx, domainID)
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			slog.Error("m6 dns: load zone on disable", "domain_id", domainID, "err", err)
		}
		return
	}
	if err := d.DNSRecords.DeleteByZoneIDAndManagedBy(ctx, zone.ID, dnscompile.EmailRecordsManagedBy); err != nil {
		slog.Error("m6 dns: delete managed records", "zone_id", zone.ID, "err", err)
	}
}

// triggerSSLSANExpansion flips the existing SSL cert row for this domain to
// status="renewing" so the reconciler re-issues with the new SANs on its next
// tick. Returns a human-readable warning on any non-fatal failure; empty string
// on success or when there's nothing to do (no SSL deps, no cert yet, or the
// cert is already in a transitional state).
//
// Only issued and self_signed certs are flipped. Other statuses (pending,
// renewing, pending_acme_retry, failed, revoked) are left alone — they're
// either already converging or in a state the operator must resolve manually.
func triggerSSLSANExpansion(ctx context.Context, d Deps, dom *models.Domain) string {
	if d.SSLCerts == nil {
		// SSL not wired (CLI path / unit tests). ReconcileSSLSANDrift adds the
		// mail SANs on its next pass, so this is a latency skip, not a gap.
		slog.Info("email_enable: SSL reconciliation skipped (SSLCerts not wired)", "domain", dom.Name)
		return ""
	}
	cert, err := d.SSLCerts.FindByDomainID(ctx, dom.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// No cert yet — the normal ssl_enabled=true flow issues one and
			// picks up the mail SANs on first issuance.
			return ""
		}
		slog.Warn("email_enable: lookup SSL cert failed", "domain", dom.Name, "err", err)
		return "SSL cert lookup failed; retry ssl reconcile manually"
	}
	if cert.Status != models.SSLStatusIssued && cert.Status != models.SSLStatusSelfSigned {
		// Already in a transitional state — reconciler will handle it.
		return ""
	}
	if err := d.SSLCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusRenewing, nil); err != nil {
		slog.Warn("email_enable: flip cert to renewing failed", "domain", dom.Name, "err", err)
		return "SSL cert flip-to-renewing failed; mail.<domain> may be missing from cert until manual renewal"
	}
	if d.SSLReconciler != nil {
		d.SSLReconciler.Schedule(dom.ID)
	}
	slog.Info("email_enable: SSL cert flipped to renewing for SAN expansion", "domain", dom.Name)
	return ""
}

// hasExistingM6Record reports whether an M6-managed row already sits at
// (name, type) — used to keep the enable sync idempotent.
func hasExistingM6Record(records []models.DNSRecord, name, typ string) bool {
	for i := range records {
		r := &records[i]
		if r.Name == name && r.Type == typ && r.ManagedBy != nil && *r.ManagedBy == dnscompile.EmailRecordsManagedBy {
			return true
		}
	}
	return false
}

// findConflict returns an existing row at (name, type) that M6 must NOT
// overwrite — a user-edited row (Managed=false) or a differently-managed panel
// record (e.g. M4 bootstrap). Returns nil when the slot is empty or already
// owned by M6 (use hasExistingM6Record for that case).
func findConflict(records []models.DNSRecord, name, typ string) *models.DNSRecord {
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
