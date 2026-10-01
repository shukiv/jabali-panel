package reconciler

import (
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// verifiedOwnership is the ownership state a fixture domain needs to take the
// normal (verified) reconcile path. A zero OwnershipState reads as pending, as
// the gates fail closed on anything but the exact "verified" status (GH #1816).
var verifiedOwnership = models.OwnershipState{
	OwnershipStatus: models.OwnershipVerified,
	OwnershipMethod: models.OwnershipMethodLegacy,
}

func pendingOwnership() models.OwnershipState {
	return models.OwnershipState{
		OwnershipStatus: models.OwnershipPending,
		OwnershipToken:  strings.Repeat("ab", 32),
	}
}

// zoneDeletes counts dns.zone.delete calls for zone (its params are a
// map[string]string, which indexesOf does not read).
func zoneDeletes(calls []fakeCall, zone string) int {
	n := 0
	for _, c := range calls {
		if c.method != "dns.zone.delete" {
			continue
		}
		if p, _ := c.params.(map[string]string); p["zone"] == zone {
			n++
		}
	}
	return n
}

// domainCreateFor returns the params of the last domain.create for name.
func domainCreateFor(t *testing.T, calls []fakeCall, name string) map[string]any {
	t.Helper()
	idx := indexesOf(calls, "domain.create", "domain", name)
	if len(idx) == 0 {
		t.Fatalf("no domain.create for %s", name)
	}
	p, _ := calls[idx[len(idx)-1]].params.(map[string]any)
	return p
}

// GH #1816: a pending domain is kept to its pending shape by ReconcileAll —
// no published zone, no recursor forward, no ACME, no webmail, and a vhost
// that carries the ownership gate — while a verified domain on the same pass
// is converged as before.
func TestPendingDomain_ReconcileAllConvergesThePendingShape(t *testing.T) {
	r, ag := steadyStateFixture(t, true)
	dom := domainByID(t, r, "domain-1")
	dom.OwnershipState = pendingOwnership()

	calls, _ := tickCalls(t, r, ag)

	if n := len(indexesOf(calls, "dns.zone.upsert", "zone", "example.com")); n != 0 {
		t.Fatalf("a pending domain's zone must not be published, got %d upserts", n)
	}
	if n := zoneDeletes(calls, "example.com"); n != 1 {
		t.Fatalf("a pending domain's zone must be removed from the authoritative server once, got %d", n)
	}
	if n := len(indexesOf(calls, "pdns.recursor_add_zone", "zone", "example.com")); n != 0 {
		t.Fatalf("a pending domain must get no recursor forward, got %d adds", n)
	}
	if n := len(indexesOf(calls, "pdns.recursor_remove_zone", "zone", "example.com")); n != 1 {
		t.Fatalf("a pending domain's recursor forward must be removed, got %d", n)
	}
	if n := len(indexesOf(calls, "webmail.vhost_apply", "domain_name", "example.com")); n != 0 {
		t.Fatalf("a pending domain must get no webmail vhost, got %d", n)
	}
	if n := len(indexesOf(calls, "ssl.issue", "domain", "example.com")); n != 0 {
		t.Fatalf("a pending domain must never reach a CA, got %d ssl.issue", n)
	}
	if n := len(indexesOf(calls, "ssl.self_sign", "domain", "example.com")); n != 1 {
		t.Fatalf("a pending domain's issued cert must be replaced by a self-signed placeholder, got %d", n)
	}
	p := domainCreateFor(t, calls, "example.com")
	if got, want := p["ownership_gate"], domainops.PreviewGate(dom); got != want {
		t.Fatalf("pending vhost ownership_gate = %v, want %s", got, want)
	}

	// The verified domain on the same pass is untouched by the gate.
	if n := len(indexesOf(calls, "dns.zone.upsert", "zone", "shop.example.net")); n != 1 {
		t.Fatalf("a verified domain's zone must still be pushed, got %d", n)
	}
	if _, ok := domainCreateFor(t, calls, "shop.example.net")["ownership_gate"]; ok {
		t.Fatal("a verified domain's vhost must carry no ownership_gate")
	}
}

// GH #1816: pending is a desired state, not a skip. A verified domain that
// goes back to pending (admin revoke) loses its published zone and its
// forward on the next pass, a steady pending domain costs no repeat calls,
// and verifying it again re-publishes at once.
func TestPendingDomain_RevokeUnpublishesAndVerifyRepublishes(t *testing.T) {
	r, ag := steadyStateFixture(t, false)
	dom := domainByID(t, r, "domain-1")

	calls, _ := tickCalls(t, r, ag)
	if len(indexesOf(calls, "dns.zone.upsert", "zone", "example.com")) != 1 ||
		len(indexesOf(calls, "pdns.recursor_add_zone", "zone", "example.com")) != 1 {
		t.Fatal("precondition: a verified domain's zone and forward are published")
	}

	dom.OwnershipState = pendingOwnership()
	calls, _ = tickCalls(t, r, ag)
	if zoneDeletes(calls, "example.com") != 1 {
		t.Fatal("a revoked domain's zone must be removed from the authoritative server")
	}
	if len(indexesOf(calls, "pdns.recursor_remove_zone", "zone", "example.com")) != 1 {
		t.Fatal("a revoked domain's recursor forward must be removed")
	}
	if len(indexesOf(calls, "dns.zone.upsert", "zone", "example.com")) != 0 {
		t.Fatal("a revoked domain's zone must not be pushed")
	}
	if domainCreateFor(t, calls, "example.com")["ownership_gate"] == nil {
		t.Fatal("a revoked domain's vhost must be re-rendered with the ownership gate")
	}

	calls, _ = tickCalls(t, r, ag)
	if zoneDeletes(calls, "example.com") != 0 ||
		len(indexesOf(calls, "pdns.recursor_remove_zone", "zone", "example.com")) != 0 {
		t.Fatal("a steady pending domain must not repeat its removals every tick")
	}

	dom.OwnershipState = verifiedOwnership
	calls, _ = tickCalls(t, r, ag)
	if len(indexesOf(calls, "dns.zone.upsert", "zone", "example.com")) != 1 {
		t.Fatal("a verified domain's zone must be published on the next pass")
	}
	if len(indexesOf(calls, "pdns.recursor_add_zone", "zone", "example.com")) != 1 {
		t.Fatal("a verified domain's recursor forward must be added on the next pass")
	}
	if _, ok := domainCreateFor(t, calls, "example.com")["ownership_gate"]; ok {
		t.Fatal("a verified domain's vhost must drop the ownership gate")
	}
}

// GH #1816: ReconcileOne (every create, every edit) takes the same pending
// branch.
func TestPendingDomain_ReconcileOne(t *testing.T) {
	r, ag, dom := plannerFixture(t)
	dom.OwnershipState = pendingOwnership()

	if err := r.ReconcileOne(context.Background(), dom.ID); err != nil {
		t.Fatal(err)
	}
	ag.mu.Lock()
	calls := append([]fakeCall(nil), ag.calls...)
	ag.mu.Unlock()
	if n := countMethod(ag, "dns.zone.upsert"); n != 0 {
		t.Fatalf("ReconcileOne must not publish a pending zone, got %d upserts", n)
	}
	if n := countMethod(ag, "pdns.recursor_add_zone"); n != 0 {
		t.Fatalf("ReconcileOne must not forward a pending zone, got %d", n)
	}
	if zoneDeletes(calls, "example.com") != 1 {
		t.Fatal("ReconcileOne must remove a pending zone from the authoritative server")
	}
	if domainCreateFor(t, calls, "example.com")["ownership_gate"] != domainops.PreviewGate(dom) {
		t.Fatal("ReconcileOne must render a pending vhost with its ownership gate")
	}
}

// GH #1816: a pending docker-app domain has no preview URL, so its proxy
// vhost is taken down rather than gated.
func TestPendingDomain_DockerAppVhostRemoved(t *testing.T) {
	r, ag, dom := plannerFixture(t)
	dom.ManagedBy = models.DomainManagedByDockerApp
	dom.OwnershipState = pendingOwnership()

	if err := r.ReconcileOne(context.Background(), dom.ID); err != nil {
		t.Fatal(err)
	}
	if countMethod(ag, "docker_app.vhost_remove") != 1 {
		t.Fatal("a pending docker-app domain's vhost must be removed")
	}
	if countMethod(ag, "docker_app.vhost_apply") != 0 || countMethod(ag, "domain.create") != 0 {
		t.Fatal("a pending docker-app domain must get no vhost")
	}
}

// GH #1816: a pending domain in shared-cert mode must not present the
// shared (CA-issued) certificate on its vhost.
func TestPendingDomain_SharedCertNotPresented(t *testing.T) {
	r, ag, dom := plannerFixture(t)
	shared := "shared-1"
	dom.SSLMode = models.SSLModeShared
	dom.SharedCertificateID = &shared
	dom.OwnershipState = pendingOwnership()

	if err := r.ReconcileOne(context.Background(), dom.ID); err != nil {
		t.Fatal(err)
	}
	ag.mu.Lock()
	calls := append([]fakeCall(nil), ag.calls...)
	ag.mu.Unlock()
	sharedPath, _ := sharedCertPaths(shared)
	if got := domainCreateFor(t, calls, "example.com")["ssl_cert_path"]; got == sharedPath {
		t.Fatalf("a pending domain must not present the shared certificate, got %v", got)
	}
}

// GH #1816: an unproven name gets no DKIM2 signature and no noreply@ relay
// identity (which would let the tenant's site send as that name).
func TestPendingDomain_NoMailSideEffects(t *testing.T) {
	dkimAgent := &fakeDkim2Agent{}
	rd := dkim2Reconciler(dkimAgent, &models.ServerSettings{}, []models.Domain{
		{OwnershipState: verifiedOwnership, Name: "proven.example", EmailEnabled: true},
		{OwnershipState: pendingOwnership(), Name: "pending.example", EmailEnabled: true},
	})
	rd.reconcileDKIM2(context.Background())
	for _, c := range dkimAgent.calls {
		if c["domain_name"] == "pending.example" {
			t.Fatal("a pending domain must not be DKIM2-converged")
		}
	}
	if len(dkimAgent.calls) != 1 {
		t.Fatalf("dkim2 calls = %d, want 1 (the verified domain)", len(dkimAgent.calls))
	}

	smAgent := &fakeSendmailAgent{}
	mailboxes := &fakeSendmailMailboxRepo{
		byEmail:     map[string]*models.Mailbox{},
		domainNames: map[string]string{"d1": "site.tld", "d2": "pending.tld"},
	}
	rs := sendmailTestReconciler(smAgent, mailboxes, []models.Domain{
		{OwnershipState: verifiedOwnership, ID: "d1", Name: "site.tld", UserID: "u1"},
		{OwnershipState: pendingOwnership(), ID: "d2", Name: "pending.tld", UserID: "u1"},
	})
	rs.reconcileSendmailCreds(context.Background())
	for _, mb := range mailboxes.created {
		if mb.DomainID == "d2" {
			t.Fatal("a pending domain must get no relay mailbox")
		}
	}
	if n := len(smAgent.byMethod("sendmail.cred.ensure")); n != 1 {
		t.Fatalf("cred.ensure calls = %d, want 1 (the verified domain)", n)
	}
	if n := len(smAgent.byMethod("sendmail.cred.remove")); n != 1 {
		t.Fatalf("cred.remove calls = %d, want 1 (the pending domain)", n)
	}
}

// GH #1816: a domain that goes back to pending loses its relay identity.
// The cred file is removed, and the relay mailbox gets a password the
// tenant never saw (the old one sat in a file the tenant's PHP could read).
// A steady pending domain costs no repeat calls, and verifying it again
// hands the rotated password to a new cred file.
func TestPendingDomain_RelayIdentityRetiredAndRestored(t *testing.T) {
	agent := &fakeSendmailAgent{}
	mailboxes := &fakeSendmailMailboxRepo{
		byEmail:     map[string]*models.Mailbox{},
		domainNames: map[string]string{"d1": "site.tld"},
	}
	r := sendmailTestReconciler(agent, mailboxes, []models.Domain{
		{OwnershipState: verifiedOwnership, ID: "d1", Name: "site.tld", UserID: "u1"},
	})
	ctx := context.Background()
	r.reconcileSendmailCreds(ctx)
	ensures := agent.byMethod("sendmail.cred.ensure")
	if len(ensures) != 1 {
		t.Fatalf("precondition: one cred ensure, got %d", len(ensures))
	}
	oldPassword := ensures[0].params["password"]

	dom := &r.domains.(*fakeSendmailDomainRepo).rows[0]
	dom.OwnershipState = pendingOwnership()
	r.reconcileSendmailCreds(ctx)
	removes := agent.byMethod("sendmail.cred.remove")
	if len(removes) != 1 || removes[0].params["domain"] != "site.tld" || removes[0].params["username"] != "alice" {
		t.Fatalf("a pending domain's cred file must be removed once, got %+v", removes)
	}
	if len(mailboxes.rotated) != 1 {
		t.Fatalf("a pending domain's relay password must be rotated, got %d rotations", len(mailboxes.rotated))
	}

	r.reconcileSendmailCreds(ctx)
	if n := len(agent.byMethod("sendmail.cred.remove")); n != 1 || len(mailboxes.rotated) != 1 {
		t.Fatalf("a steady pending domain must not repeat the retire (removes=%d rotations=%d)", n, len(mailboxes.rotated))
	}

	dom.OwnershipState = verifiedOwnership
	r.reconcileSendmailCreds(ctx)
	ensures = agent.byMethod("sendmail.cred.ensure")
	if len(ensures) != 2 {
		t.Fatalf("a verified domain must get its cred file back, got %d ensures", len(ensures))
	}
	if ensures[1].params["password"] == oldPassword {
		t.Fatal("the restored cred file must carry the rotated password, not the one the tenant saw")
	}
}

// GH #1816: a domain that goes back to pending loses a published MTA-STS
// policy (its own vhost, which carries no ownership gate).
func TestPendingDomain_MTAStsDisabled(t *testing.T) {
	r, ag, dom := plannerFixture(t)
	dom.MTASTSEnabled = true
	dom.MTASTSId = 3
	dom.MTASTSAppliedId = 3
	dom.OwnershipState = pendingOwnership()

	if err := r.ReconcileOne(context.Background(), dom.ID); err != nil {
		t.Fatal(err)
	}
	if countMethod(ag, "mail.mtasts.disable") != 1 {
		t.Fatal("a pending domain's MTA-STS policy must be disabled")
	}
	if countMethod(ag, "mail.mtasts.apply") != 0 {
		t.Fatal("a pending domain must get no MTA-STS policy")
	}
	if dom.MTASTSAppliedId != 0 {
		t.Fatalf("the applied id must be cleared so verification re-applies, got %d", dom.MTASTSAppliedId)
	}
}
