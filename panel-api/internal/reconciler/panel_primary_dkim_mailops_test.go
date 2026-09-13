package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-286 AC5 / JAB-288 AC5: the reconciler's periodic enable convergence
// (ensurePanelPrimaryDKIM / ensureTenantEmailEnabled) is the fourth caller of
// the shared domainmailops.Enable lifecycle. These contract tests exercise the
// same failure matrix the REST and CLI adapters do — happy, agent-failure, and
// incomplete-Agent-response (fail-closed, never persist) — proving the periodic
// path routes through the Module rather than copying the Agent→database→DNS
// ordering (JAB-286 AC4).

// fakeMailEnableAgent answers domain.email_enable with a configurable response
// so the failure matrix can drive the ok / incomplete / error branches.
type fakeMailEnableAgent struct {
	calls      int
	returnErr  bool
	incomplete bool // ok=true but empty public key
}

func (f *fakeMailEnableAgent) Call(_ context.Context, command string, _ any) (json.RawMessage, error) {
	if command != "domain.email_enable" {
		return nil, nil
	}
	f.calls++
	if f.returnErr {
		return nil, errors.New("simulated agent failure")
	}
	if f.incomplete {
		// ok=true but no public key — the Agent half-answered; Enable must
		// never persist this (it would overwrite the last usable state).
		return json.RawMessage(`{"ok":true,"dkim_selector":"jabali","dkim_public_key":""}`), nil
	}
	return json.RawMessage(`{"ok":true,"dkim_selector":"jabali","dkim_public_key":"MIIBdummykey"}`), nil
}

// mailOpsTestReconciler wires the fakes the enable convergence reaches through
// mailOpsDeps: domains (UpdateEmailState), dnsZones + dnsRecords (managed DNS),
// serverSettings, and the agent. SSL repos are left nil exactly as the
// reconciler omits them.
func mailOpsTestReconciler(agent *fakeMailEnableAgent, dom *models.Domain) (*Reconciler, *fakeDNSRecordRepo) {
	domainRepo := &fakeDomainRepo{domains: map[string]*models.Domain{dom.ID: dom}}
	zoneRepo := &fakeDNSZoneRepo{zones: map[string]*models.DNSZone{
		"zone1": {ID: "zone1", Name: dom.Name, DomainID: dom.ID},
	}}
	recordRepo := &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{}}
	r := &Reconciler{
		domains:        domainRepo,
		dnsZones:       zoneRepo,
		dnsRecords:     recordRepo,
		serverSettings: &fakeSettingsRepo{srv: &models.ServerSettings{}},
		agent:          agent,
		log:            slog.New(slog.DiscardHandler),
	}
	return r, recordRepo
}

func mailOpsTestDomain(isPanelPrimary bool) *models.Domain {
	return &models.Domain{
		ID:             "dom1",
		Name:           "tenant.example.com", // TLD "com" → mail-routable
		EmailEnabled:   true,
		IsPanelPrimary: isPanelPrimary,
		// DkimSelector nil → not yet provisioned → convergence fires.
	}
}

// runEnableMatrix drives one of the two periodic-enable entry points through
// the shared failure matrix and asserts the Module's fail-closed contract.
func runEnableMatrix(t *testing.T, isPanelPrimary bool, invoke func(r *Reconciler, ctx context.Context, d *models.Domain)) {
	t.Helper()
	ctx := context.Background()

	t.Run("happy: agent ok → persists pair and publishes managed DNS", func(t *testing.T) {
		agent := &fakeMailEnableAgent{}
		dom := mailOpsTestDomain(isPanelPrimary)
		r, records := mailOpsTestReconciler(agent, dom)

		invoke(r, ctx, dom)

		if agent.calls != 1 {
			t.Fatalf("expected exactly 1 agent call, got %d", agent.calls)
		}
		if dom.DkimSelector == nil || *dom.DkimSelector != "jabali" {
			t.Fatalf("expected DkimSelector persisted as jabali, got %v", dom.DkimSelector)
		}
		if dom.DkimPublicKey == nil || *dom.DkimPublicKey == "" {
			t.Fatalf("expected DkimPublicKey persisted, got %v", dom.DkimPublicKey)
		}
		// Managed-DNS writes run only after a successful persist (the declared
		// order inside Enable). Records present ⇒ the whole agent→db→DNS
		// ordering ran through the Module.
		if len(records.records) == 0 {
			t.Fatalf("expected managed DNS records published, got none")
		}
	})

	t.Run("agent error → nothing persisted, no DNS", func(t *testing.T) {
		agent := &fakeMailEnableAgent{returnErr: true}
		dom := mailOpsTestDomain(isPanelPrimary)
		r, records := mailOpsTestReconciler(agent, dom)

		invoke(r, ctx, dom)

		if dom.DkimSelector != nil {
			t.Fatalf("agent failure must not persist a selector, got %v", *dom.DkimSelector)
		}
		if len(records.records) != 0 {
			t.Fatalf("agent failure must not publish DNS, got %d records", len(records.records))
		}
	})

	t.Run("incomplete agent response → fail-closed, never overwrite state", func(t *testing.T) {
		agent := &fakeMailEnableAgent{incomplete: true}
		dom := mailOpsTestDomain(isPanelPrimary)
		r, records := mailOpsTestReconciler(agent, dom)

		invoke(r, ctx, dom)

		if dom.DkimSelector != nil {
			t.Fatalf("incomplete response must not persist a selector, got %v", *dom.DkimSelector)
		}
		if dom.DkimPublicKey != nil {
			t.Fatalf("incomplete response must not persist a public key, got %v", *dom.DkimPublicKey)
		}
		if len(records.records) != 0 {
			t.Fatalf("incomplete response must not publish DNS, got %d records", len(records.records))
		}
	})
}

func TestEnsurePanelPrimaryDKIM_RoutesThroughMailOps(t *testing.T) {
	runEnableMatrix(t, true, func(r *Reconciler, ctx context.Context, d *models.Domain) {
		r.ensurePanelPrimaryDKIM(ctx, d)
	})
}

func TestEnsureTenantEmailEnabled_RoutesThroughMailOps(t *testing.T) {
	runEnableMatrix(t, false, func(r *Reconciler, ctx context.Context, d *models.Domain) {
		r.ensureTenantEmailEnabled(ctx, d)
	})
}

// TestPanelPrimaryDKIM_SourceRoutesThroughModule source-pins the routing: both
// entry points must call the shared domainmailops.Enable via mailOpsDeps, and
// the direct agent command literal must no longer live in this file (it now
// lives inside domainmailops). If someone re-inlines the agent→persist→DNS
// ordering here, this fails — the behavioral matrix above would keep passing on
// a re-inlined copy that still fail-closes, so this pin is what actually holds
// AC4/AC5's "invokes the shared Implementation" requirement.
func TestPanelPrimaryDKIM_SourceRoutesThroughModule(t *testing.T) {
	src, err := os.ReadFile("panel_primary_dkim.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	s := string(src)
	if n := strings.Count(s, "domainmailops.Enable(ctx, r.mailOpsDeps(), domain)"); n != 2 {
		t.Fatalf("expected both entry points to route through domainmailops.Enable (found %d call sites)", n)
	}
	if strings.Contains(s, `"domain.email_enable"`) {
		t.Fatalf("panel_primary_dkim.go still issues domain.email_enable directly; " +
			"the agent call must route through domainmailops.Enable")
	}
}
