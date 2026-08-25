package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ---------- fakes ----------

// fakeDomainRepo is the minimum DomainRepository the CLI email helpers
// exercise: FindByID, FindByName, UpdateEmailState. Everything else is
// a stub that panics if exercised — a signal that a test drifted into
// territory not covered by this file.
type fakeDomainRepo struct {
	byID   map[string]*models.Domain
	byName map[string]*models.Domain
	// state capture
	lastEmailState *repository.DomainEmailState
	updateErr      error
}

func newFakeDomainRepo(doms ...*models.Domain) *fakeDomainRepo {
	f := &fakeDomainRepo{byID: map[string]*models.Domain{}, byName: map[string]*models.Domain{}}
	for _, d := range doms {
		cp := *d
		f.byID[d.ID] = &cp
		f.byName[d.Name] = &cp
	}
	return f
}

func (f *fakeDomainRepo) FindByIDs(_ context.Context, ids []string) ([]models.Domain, error) {
	var out []models.Domain
	for _, id := range ids {
		if d, ok := f.byID[id]; ok {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (f *fakeDomainRepo) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if d, ok := f.byID[id]; ok {
		cp := *d
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeDomainRepo) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if d, ok := f.byName[name]; ok {
		cp := *d
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeDomainRepo) UpdateMailProvider(_ context.Context, _ string, _ repository.DomainMailProvider) error {
	return nil
}

func (f *fakeDomainRepo) UpdateSSLMode(context.Context, string, string) error { return nil }
func (f *fakeDomainRepo) SetSharedCertificate(context.Context, string, *string, string) error {
	return nil
}

func (f *fakeDomainRepo) UpdateEmailState(_ context.Context, id string, state repository.DomainEmailState) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	cp := state
	f.lastEmailState = &cp
	if d, ok := f.byID[id]; ok {
		d.EmailEnabled = state.Enabled
		d.DkimSelector = state.DkimSelector
		d.DkimPublicKey = state.DkimPublicKey
		d.EmailEnabledAt = state.EmailEnabledAt
	}
	return nil
}

// --- unused stubs (DomainRepository compliance) ---

func (*fakeDomainRepo) Create(context.Context, *models.Domain) error { return nil }
func (*fakeDomainRepo) List(context.Context, repository.ListOptions) ([]models.Domain, int64, error) {
	return nil, 0, nil
}
func (*fakeDomainRepo) ListByUserID(context.Context, string, repository.ListOptions) ([]models.Domain, int64, error) {
	return nil, 0, nil
}
func (*fakeDomainRepo) BulkSetEnabledByUserID(context.Context, string, bool) (int64, error) {
	return 0, nil
}
func (*fakeDomainRepo) Update(context.Context, *models.Domain) error { return nil }
func (*fakeDomainRepo) Delete(context.Context, string) error         { return nil }
func (*fakeDomainRepo) ListForRegistrarRefresh(context.Context, time.Time, int) ([]models.Domain, error) {
	return nil, nil
}
func (*fakeDomainRepo) SetRegistrarExpiry(context.Context, string, *time.Time, time.Time) error {
	return nil
}
func (*fakeDomainRepo) CountByUserID(context.Context, string) (int64, error) {
	return 0, nil
}
func (*fakeDomainRepo) CountByPHPPoolID(context.Context, string) (int64, error) {
	return 0, nil
}
func (*fakeDomainRepo) SetPHPPoolID(context.Context, string, *string) error { return nil }
func (*fakeDomainRepo) UpdatePHPSettings(context.Context, string, repository.DomainPHPSettings) error {
	return nil
}
func (*fakeDomainRepo) FindPanelPrimary(context.Context) (*models.Domain, error) {
	return nil, repository.ErrPanelPrimaryNotFound
}
func (*fakeDomainRepo) MarkPanelPrimary(context.Context, string) error { return nil }
func (*fakeDomainRepo) SetListenIPs(context.Context, string, repository.DomainListenIPs) error {
	return nil
}

func (*fakeDomainRepo) UpdateCatchallTarget(context.Context, string, *string) error {
	return nil
}

func (*fakeDomainRepo) UpdateDisclaimer(context.Context, string, bool, *string) error {
	return nil
}

func (*fakeDomainRepo) UpdateCacheEnabled(context.Context, string, bool) error {
	return nil
}
func (*fakeDomainRepo) UpdateCachePath(context.Context, string, string) error           { return nil }
func (*fakeDomainRepo) UpdateCacheTTL(context.Context, string, int) error               { return nil }
func (*fakeDomainRepo) UpdateCacheQueryAllowlist(context.Context, string, string) error { return nil }

func (*fakeDomainRepo) UpdateSkipAutoSAN(context.Context, string, bool) error {
	return nil
}

func (*fakeDomainRepo) UpdateMTASTSEnabled(context.Context, string, bool) (uint64, error) {
	return 0, nil
}

func (*fakeDomainRepo) UpdateMTASTSAppliedID(context.Context, string, uint64) error {
	return nil
}

func (*fakeDomainRepo) UpdatePHPPoolID(context.Context, string, *string) error {
	return nil
}

func (*fakeDomainRepo) UpdateDNSSECEnabled(context.Context, string, bool) error {
	return nil
}

func (*fakeDomainRepo) UpdateGhostState(context.Context, string, string, time.Time, *string) error {
	return nil
}

func (*fakeDomainRepo) ListForGhostCheck(context.Context, time.Time, int) ([]models.Domain, error) {
	return nil, nil
}

// fakeDNSZoneRepo / fakeDNSRecordRepo are minimal — enableDomainEmail
// only calls FindByDomainID + ListByZoneID + Create, and disable only
// calls FindByDomainID + DeleteByZoneIDAndManagedBy. Everything else
// panics.
type fakeDNSZoneRepo struct {
	byDomain map[string]*models.DNSZone
}

func (f *fakeDNSZoneRepo) FindByDomainID(_ context.Context, domainID string) (*models.DNSZone, error) {
	if z, ok := f.byDomain[domainID]; ok {
		cp := *z
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}
func (*fakeDNSZoneRepo) Create(context.Context, *models.DNSZone) error { return nil }
func (*fakeDNSZoneRepo) Update(context.Context, *models.DNSZone) error { return nil }
func (*fakeDNSZoneRepo) Delete(context.Context, string) error          { return nil }
func (*fakeDNSZoneRepo) FindByID(context.Context, string) (*models.DNSZone, error) {
	return nil, repository.ErrNotFound
}
func (*fakeDNSZoneRepo) FindByName(context.Context, string) (*models.DNSZone, error) {
	return nil, repository.ErrNotFound
}
func (*fakeDNSZoneRepo) ListAll(context.Context) ([]models.DNSZone, error) {
	return nil, nil
}
func (f *fakeDNSZoneRepo) FindByDomainIDs(ctx context.Context, domainIDs []string) ([]models.DNSZone, error) {
	var out []models.DNSZone
	for _, id := range domainIDs {
		if z, err := f.FindByDomainID(ctx, id); err == nil && z != nil {
			out = append(out, *z)
		}
	}
	return out, nil
}

type fakeDNSRecordRepo struct {
	records     map[string][]models.DNSRecord // zoneID → rows
	deleteCalls []string                      // captured ManagedBy values
	createCalls []models.DNSRecord
	createErr   error
}

func (f *fakeDNSRecordRepo) ListByZoneID(_ context.Context, zoneID string) ([]models.DNSRecord, error) {
	return f.records[zoneID], nil
}
func (f *fakeDNSRecordRepo) CountByZoneIDs(_ context.Context, zoneIDs []string) (map[string]int64, error) {
	out := make(map[string]int64)
	for _, zid := range zoneIDs {
		if n := len(f.records[zid]); n > 0 {
			out[zid] = int64(n)
		}
	}
	return out, nil
}
func (f *fakeDNSRecordRepo) Create(_ context.Context, rec *models.DNSRecord) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.createCalls = append(f.createCalls, *rec)
	f.records[rec.ZoneID] = append(f.records[rec.ZoneID], *rec)
	return nil
}
func (f *fakeDNSRecordRepo) DeleteByZoneIDAndManagedBy(_ context.Context, zoneID, managedBy string) error {
	f.deleteCalls = append(f.deleteCalls, managedBy)
	rows := f.records[zoneID]
	out := rows[:0]
	for _, r := range rows {
		if r.ManagedBy != nil && *r.ManagedBy == managedBy {
			continue
		}
		out = append(out, r)
	}
	f.records[zoneID] = out
	return nil
}
func (*fakeDNSRecordRepo) FindByID(context.Context, string) (*models.DNSRecord, error) {
	return nil, repository.ErrNotFound
}
func (*fakeDNSRecordRepo) Update(context.Context, *models.DNSRecord) error { return nil }
func (*fakeDNSRecordRepo) Delete(context.Context, string) error            { return nil }
func (*fakeDNSRecordRepo) DeleteByZoneID(context.Context, string) error    { return nil }

// ---------- helpers ----------

// okEnableResponse returns an agent-shape success body with a stubbed
// selector + public key. The public key isn't a real Ed25519 value,
// but the helper only checks for non-emptiness.
func okEnableResponse() json.RawMessage {
	return json.RawMessage(`{"ok":true,"dkim_selector":"jabali","dkim_public_key":"v=DKIM1;k=ed25519;p=AAAA"}`)
}

func newEmailDepsFor(dom *models.Domain, zone *models.DNSZone, agentCall agentCaller) (domainEmailDeps, *fakeDomainRepo, *fakeDNSZoneRepo, *fakeDNSRecordRepo) {
	dr := newFakeDomainRepo(dom)
	zr := &fakeDNSZoneRepo{byDomain: map[string]*models.DNSZone{}}
	rr := &fakeDNSRecordRepo{records: map[string][]models.DNSRecord{}}
	if zone != nil {
		zr.byDomain[dom.ID] = zone
	}
	deps := domainEmailDeps{
		domains:    dr,
		dnsZones:   zr,
		dnsRecords: rr,
		call:       agentCall,
	}
	return deps, dr, zr, rr
}

// testZone returns a zone + a simple bootstrap (M4) row list so tests
// can observe conflict / idempotency branches without rebuilding the
// full dnscompile.BootstrapRecords output.
func testZone(zoneID, domainID, domainName string) *models.DNSZone {
	return &models.DNSZone{
		ID:       zoneID,
		DomainID: domainID,
		Name:     domainName,
	}
}

// ---------- tests ----------

// TestEnableDomainEmail_HappyPath — agent generates DKIM, row is
// flipped, DNS records are published into an empty zone.
func TestEnableDomainEmail_HappyPath(t *testing.T) {
	t.Parallel()
	dom := testDomain("dom1", "example.com", false)
	zone := testZone("zone1", dom.ID, dom.Name)

	var calledCmd string
	agent := func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		calledCmd = cmd
		return okEnableResponse(), nil
	}
	deps, dr, _, rr := newEmailDepsFor(dom, zone, agent)

	resp, warnings, err := enableDomainEmailDirect(context.Background(), deps, dr.byID[dom.ID])
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if calledCmd != "domain.email_enable" {
		t.Errorf("agent should fire domain.email_enable, got %q", calledCmd)
	}
	if resp.DkimSelector != "jabali" {
		t.Errorf("selector = %q, want jabali", resp.DkimSelector)
	}
	if dr.lastEmailState == nil || !dr.lastEmailState.Enabled {
		t.Error("UpdateEmailState must flip enabled=true")
	}
	if dr.lastEmailState.DkimSelector == nil || *dr.lastEmailState.DkimSelector != "jabali" {
		t.Error("UpdateEmailState must write the DKIM selector")
	}
	if len(warnings) != 0 {
		t.Errorf("clean zone should produce 0 warnings, got %v", warnings)
	}
	// dnscompile.BuildEmailRecords emits 15 rows: DKIM + autoconfig +
	// autodiscover + 5 cal/carddav SRVs + 4 client SRVs + TLS-RPT + 2 CAA
	// (GH #134).
	if len(rr.createCalls) != 15 {
		t.Errorf("expected 15 DNS records inserted, got %d", len(rr.createCalls))
	}
	// All must be tagged managed_by="m6".
	for _, r := range rr.createCalls {
		if r.ManagedBy == nil || *r.ManagedBy != dnscompile.EmailRecordsManagedBy {
			t.Errorf("record %s/%s not tagged managed_by=%q: %+v", r.Type, r.Name, dnscompile.EmailRecordsManagedBy, r.ManagedBy)
		}
	}
}

// TestEnableDomainEmail_AgentFailureDoesNotMutateDB verifies the
// ordering rule: a failed agent call must leave email_enabled alone so
// the next retry starts clean.
func TestEnableDomainEmail_AgentFailureDoesNotMutateDB(t *testing.T) {
	t.Parallel()
	dom := testDomain("dom1", "example.com", false)

	agent := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return nil, errors.New("stalwart-down")
	}
	deps, dr, _, _ := newEmailDepsFor(dom, nil, agent)

	_, _, err := enableDomainEmailDirect(context.Background(), deps, dr.byID[dom.ID])
	if err == nil {
		t.Fatal("expected enable to fail when agent errors")
	}
	if dr.lastEmailState != nil {
		t.Error("UpdateEmailState must NOT run when agent call failed")
	}
	if dr.byID[dom.ID].EmailEnabled {
		t.Error("email_enabled must stay false after agent failure")
	}
}

// TestEnableDomainEmail_BadAgentResponse — ok=false or missing DKIM
// fields must fail before mutating anything.
func TestEnableDomainEmail_BadAgentResponse(t *testing.T) {
	t.Parallel()
	dom := testDomain("dom1", "example.com", false)

	agent := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true,"dkim_selector":"","dkim_public_key":""}`), nil
	}
	deps, dr, _, _ := newEmailDepsFor(dom, nil, agent)

	_, _, err := enableDomainEmailDirect(context.Background(), deps, dr.byID[dom.ID])
	if err == nil || !strings.Contains(err.Error(), "agent bad response") {
		t.Fatalf("expected bad-response error, got %v", err)
	}
	if dr.lastEmailState != nil {
		t.Error("UpdateEmailState must NOT run on bad agent response")
	}
}

// TestEnableDomainEmail_ConflictSurfacesWarning — a user-edited row
// in the same (name, type) slot must become a warning (not an error)
// and must NOT be overwritten.
func TestEnableDomainEmail_ConflictSurfacesWarning(t *testing.T) {
	t.Parallel()
	dom := testDomain("dom1", "example.com", false)
	zone := testZone("zone1", dom.ID, dom.Name)

	agent := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return okEnableResponse(), nil
	}
	deps, dr, _, rr := newEmailDepsFor(dom, zone, agent)

	// Pre-seed a user-edited CNAME at "autoconfig" — M6 wants to write
	// exactly here, so we expect a conflict warning.
	rr.records[zone.ID] = []models.DNSRecord{{
		ID:      "user-rec",
		ZoneID:  zone.ID,
		Name:    "autoconfig",
		Type:    "CNAME",
		Content: "some-other-host.example.net.",
		Managed: false,
	}}

	_, warnings, err := enableDomainEmailDirect(context.Background(), deps, dr.byID[dom.ID])
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("expected at least one conflict warning for the user-edited autoconfig CNAME")
	}
	foundConflictWarn := false
	for _, w := range warnings {
		if strings.Contains(w, "autoconfig") && strings.Contains(w, "user-edited") {
			foundConflictWarn = true
			break
		}
	}
	if !foundConflictWarn {
		t.Errorf("expected a user-edited conflict warning for autoconfig, got: %v", warnings)
	}
	// Existing user-edited row must still be present; the other two M6
	// records should have been inserted.
	stored := rr.records[zone.ID]
	var userRec *models.DNSRecord
	for i := range stored {
		if stored[i].ID == "user-rec" {
			userRec = &stored[i]
		}
	}
	if userRec == nil {
		t.Error("user-edited record must not be deleted or overwritten")
	}
}

// TestEnableDomainEmail_NoZoneWarnsButSucceeds — when the domain has
// no zone on file (non-PowerDNS install) we can still succeed, but the
// warning list surfaces that DNS autoconfig was skipped.
func TestEnableDomainEmail_NoZoneWarnsButSucceeds(t *testing.T) {
	t.Parallel()
	dom := testDomain("dom1", "example.com", false)
	// zone = nil → no zone mapping for this domain.

	agent := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return okEnableResponse(), nil
	}
	deps, dr, _, rr := newEmailDepsFor(dom, nil, agent)

	_, warnings, err := enableDomainEmailDirect(context.Background(), deps, dr.byID[dom.ID])
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "no zone") {
		t.Errorf("expected 'no zone' warning, got %v", warnings)
	}
	if len(rr.createCalls) != 0 {
		t.Errorf("no records should be created when the zone is missing, got %d", len(rr.createCalls))
	}
}

// TestDisableDomainEmail_HappyPath — agent reload + row flip +
// managed_by="m6" delete. DKIM columns MUST be untouched (ADR-0043).
func TestDisableDomainEmail_HappyPath(t *testing.T) {
	t.Parallel()
	selector, pub := "jabali", "v=DKIM1;k=ed25519;p=AAAA"
	enabledAt := time.Now().UTC()
	dom := testDomain("dom1", "example.com", true)
	dom.DkimSelector = &selector
	dom.DkimPublicKey = &pub
	dom.EmailEnabledAt = &enabledAt
	zone := testZone("zone1", dom.ID, dom.Name)

	var calledCmd string
	agent := func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		calledCmd = cmd
		return json.RawMessage(`{"ok":true}`), nil
	}
	deps, dr, _, rr := newEmailDepsFor(dom, zone, agent)
	// Seed a managed_by="m6" row so we can prove the delete scoped to it.
	m6 := dnscompile.EmailRecordsManagedBy
	rr.records[zone.ID] = []models.DNSRecord{{
		ID: "m6-rec", ZoneID: zone.ID, Name: "autoconfig", Type: "CNAME",
		Content: "mail", Managed: true, ManagedBy: &m6,
	}, {
		ID: "m4-rec", ZoneID: zone.ID, Name: "@", Type: "MX",
		Content: "10 mail.example.com.", Managed: true, // M4 bootstrap has ManagedBy=NULL
	}}

	err := disableDomainEmailDirect(context.Background(), deps, dr.byID[dom.ID])
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if calledCmd != "domain.email_disable" {
		t.Errorf("agent should fire domain.email_disable, got %q", calledCmd)
	}
	if dr.lastEmailState == nil || dr.lastEmailState.Enabled {
		t.Error("UpdateEmailState must flip enabled=false")
	}
	// DKIM selector / public-key must NOT have been nulled (ADR-0043).
	if dr.lastEmailState.DkimSelector != nil {
		t.Error("DKIM selector must be left alone on disable (ADR-0043)")
	}
	if dr.lastEmailState.DkimPublicKey != nil {
		t.Error("DKIM public key must be left alone on disable (ADR-0043)")
	}
	// Delete must have been scoped to managed_by="m6".
	if len(rr.deleteCalls) != 1 || rr.deleteCalls[0] != m6 {
		t.Errorf("delete must scope by managed_by=m6, got %v", rr.deleteCalls)
	}
	// The M4 row must have survived.
	survived := false
	for _, r := range rr.records[zone.ID] {
		if r.ID == "m4-rec" {
			survived = true
		}
	}
	if !survived {
		t.Error("M4 bootstrap row (ManagedBy=NULL) must survive disable")
	}
}

// TestDisableDomainEmail_AgentFailureDoesNotMutate — same ordering
// rule as enable: failed agent call leaves everything alone.
func TestDisableDomainEmail_AgentFailureDoesNotMutate(t *testing.T) {
	t.Parallel()
	dom := testDomain("dom1", "example.com", true)

	agent := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return nil, errors.New("agent-down")
	}
	deps, dr, _, rr := newEmailDepsFor(dom, nil, agent)

	err := disableDomainEmailDirect(context.Background(), deps, dr.byID[dom.ID])
	if err == nil {
		t.Fatal("expected disable to fail when agent errors")
	}
	if dr.lastEmailState != nil {
		t.Error("UpdateEmailState must NOT run when agent call failed")
	}
	if len(rr.deleteCalls) != 0 {
		t.Error("DNS delete must NOT fire when agent call failed")
	}
	if !dr.byID[dom.ID].EmailEnabled {
		t.Error("email_enabled must stay true after agent failure")
	}
}

func (*fakeDomainRepo) AttachDockerApp(context.Context, string, string, models.NginxRules) error {
	return nil
}
func (*fakeDomainRepo) DetachDockerApp(context.Context, string, bool) error {
	return nil
}

func (r *fakeDomainRepo) RewriteDocRootPrefix(context.Context, string, string, string) (int64, error) {
	return 0, nil
}
