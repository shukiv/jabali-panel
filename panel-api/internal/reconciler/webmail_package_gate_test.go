package reconciler

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// webmailGatePkgRepo is a minimal PackageRepository whose List returns a
// fixed slice (or an error) — enough to drive the GH #1628 slice-2 package
// entitlement gate. Everything else embeds the interface (nil) and is never
// called by reconcileWebmailVhosts.
type webmailGatePkgRepo struct {
	repository.PackageRepository
	pkgs    []models.HostingPackage
	listErr error
}

func (f *webmailGatePkgRepo) List(context.Context, repository.ListOptions) ([]models.HostingPackage, int64, error) {
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	return f.pkgs, int64(len(f.pkgs)), nil
}

func wmPtr(s string) *string { return &s }

// wmGateReconciler builds a webmail reconciler wired with the given domain,
// user, and package fakes plus SSL (required — reconcileWebmailVhosts returns
// early when sslCerts is nil) and a server-wide-enabled settings row.
func wmGateReconciler(dr *fakeDomainRepo, ur *fakeUserRepo, pr repository.PackageRepository, ag *fakeWebmailAgent) *Reconciler {
	r := New(dr, ur, ag, slog.Default(), Config{}).WithSSLCerts(newFakeSSLCertRepo()).WithPackages(pr)
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{WebmailEnabled: true}}
	return r
}

// TestWebmail_PackageEntitlementOff_RemovesVhost is the GH #1628 slice-2
// bug-layer test: a user whose hosting package has webmail DISABLED must not
// get a mail vhost even when the per-user toggle and the domain are both ON.
// Before slice 2 the reconciler ignored the package field, so this domain got
// a vhost_apply and the daemon started — RED until the package AND-gate lands.
func TestWebmail_PackageEntitlementOff_RemovesVhost(t *testing.T) {
	ag := &fakeWebmailAgent{}
	dr := newFakeDomainRepo()
	dr.domains["d1"] = &models.Domain{ID: "d1", Name: "example.com", UserID: "u1", EmailEnabled: true, WebmailEnabled: true}
	ur := &fakeUserRepo{users: map[string]*models.User{
		"u1": {ID: "u1", WebmailEnabled: true, PackageID: wmPtr("p1")},
	}}
	pr := &webmailGatePkgRepo{pkgs: []models.HostingPackage{{ID: "p1", WebmailEnabled: false}}}

	wmGateReconciler(dr, ur, pr, ag).reconcileWebmailVhosts(context.Background())

	assert.True(t, ag.has("webmail.vhost_remove"), "package webmail OFF must remove the domain's mail vhost")
	assert.False(t, ag.has("service.start"), "no webmail domain remains active → daemon must not start")
}

// TestWebmail_NoPackage_KeepsVhost pins the #282 exception: an account with NO
// hosting package (PackageID nil) keeps webmail ON — a deliberate deviation
// from #282's privileged-feature DENY default, decided in the #1628 plan. The
// package OFF in the repo is a decoy the user is not on.
func TestWebmail_NoPackage_KeepsVhost(t *testing.T) {
	ag := &fakeWebmailAgent{}
	dr := newFakeDomainRepo()
	dr.domains["d1"] = &models.Domain{ID: "d1", Name: "example.com", UserID: "u1", EmailEnabled: true, WebmailEnabled: true}
	ur := &fakeUserRepo{users: map[string]*models.User{
		"u1": {ID: "u1", WebmailEnabled: true, PackageID: nil},
	}}
	pr := &webmailGatePkgRepo{pkgs: []models.HostingPackage{{ID: "p1", WebmailEnabled: false}}}

	wmGateReconciler(dr, ur, pr, ag).reconcileWebmailVhosts(context.Background())

	assert.True(t, ag.has("service.start"), "no package = webmail ON (#282 exception) → daemon starts")
	assert.False(t, ag.has("webmail.vhost_remove"), "no package must not remove the vhost")
}

// TestWebmail_PackageListError_FailsOpen: a packages.List error must fail OPEN
// (treat everyone as ON), mirroring the pre-#1628 per-user gate — a transient
// DB blip must never tear down every tenant's webmail vhost.
func TestWebmail_PackageListError_FailsOpen(t *testing.T) {
	ag := &fakeWebmailAgent{}
	dr := newFakeDomainRepo()
	dr.domains["d1"] = &models.Domain{ID: "d1", Name: "example.com", UserID: "u1", EmailEnabled: true, WebmailEnabled: true}
	ur := &fakeUserRepo{users: map[string]*models.User{
		"u1": {ID: "u1", WebmailEnabled: true, PackageID: wmPtr("p1")},
	}}
	pr := &webmailGatePkgRepo{listErr: errors.New("db down")}

	wmGateReconciler(dr, ur, pr, ag).reconcileWebmailVhosts(context.Background())

	assert.True(t, ag.has("service.start"), "packages.List error fails open → webmail ON")
	assert.False(t, ag.has("webmail.vhost_remove"), "must not tear down vhosts on a package-list error")
}
