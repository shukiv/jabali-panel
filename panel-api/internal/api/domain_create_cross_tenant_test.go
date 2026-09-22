package api

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestCreateDomainOp_CrossTenantSuffix pins the GH #1789 guard at the reporter's
// actual door — the POST /domains orchestration (createDomainOp), not just the
// CrossTenantSuffixCollision leaf. It proves the wiring: a non-admin tenant is
// refused with 409 domain_conflicts_tenant (and no row persisted) both when the
// claim nests under another tenant's domain (parent direction — the reporter's
// evil.example.com under example.com) and when it wraps another tenant's
// subdomain (child direction), while an admin actor is NOT gated. Deleting the
// `if !in.ActorIsAdmin { … }` block turns the non-admin cases green-through
// (dcDomains has no exact-name uniqueness, so the create would succeed) → RED,
// so the wiring itself is falsifiable, per [test at the bug's layer].
func TestCreateDomainOp_CrossTenantSuffix(t *testing.T) {
	uname := "alice"
	const other = "u-other"

	// owner is an ordinary, eligible tenant so the guard (which runs before
	// owner-eligibility) is what decides the parent/child cases.
	newOwner := func() *models.User { return &models.User{ID: "u-alice", Username: &uname} }

	runCreate := func(seed map[string]*models.Domain, name string, actorAdmin bool) (*createDomainError, *dcDomains) {
		owner := newOwner()
		users := newAbUsers(owner)
		dom := newDCDomains()
		for n, d := range seed {
			dom.byName[n] = d
		}
		h := &domainHandler{cfg: DomainHandlerConfig{Users: users, Domains: dom}}
		_, oerr := createDomainOp(context.Background(), h, createDomainInput{
			OwnerID:       owner.ID,
			Name:          name,
			ActorIsAdmin:  actorAdmin,
			MailProvider:  models.MailProviderNone,
			SSLMode:       models.SSLModeNone,
			SkipInlineSSL: true,
		})
		return oerr, dom
	}

	t.Run("non-admin claim under another tenant's domain is refused (parent)", func(t *testing.T) {
		seed := map[string]*models.Domain{"example.com": {Name: "example.com", UserID: other}}
		oerr, dom := runCreate(seed, "evil.example.com", false)
		if oerr == nil || oerr.Status != 409 || oerr.Code != "domain_conflicts_tenant" {
			t.Fatalf("want 409 domain_conflicts_tenant, got %+v", oerr)
		}
		if len(dom.created) != 0 {
			t.Fatalf("no domain must be persisted on refusal, got %d", len(dom.created))
		}
	})

	t.Run("non-admin claim wrapping another tenant's subdomain is refused (child)", func(t *testing.T) {
		seed := map[string]*models.Domain{"sub.example.com": {Name: "sub.example.com", UserID: other}}
		oerr, dom := runCreate(seed, "example.com", false)
		if oerr == nil || oerr.Status != 409 || oerr.Code != "domain_conflicts_tenant" {
			t.Fatalf("want 409 domain_conflicts_tenant, got %+v", oerr)
		}
		if len(dom.created) != 0 {
			t.Fatalf("no domain must be persisted on refusal, got %d", len(dom.created))
		}
	})

	t.Run("same-owner nesting is allowed", func(t *testing.T) {
		seed := map[string]*models.Domain{"example.com": {Name: "example.com", UserID: "u-alice"}}
		oerr, _ := runCreate(seed, "shop.example.com", false)
		if oerr != nil && oerr.Code == "domain_conflicts_tenant" {
			t.Fatalf("same-owner nesting must not trip the cross-tenant guard, got %+v", oerr)
		}
	})

	t.Run("admin actor bypasses the guard", func(t *testing.T) {
		seed := map[string]*models.Domain{"example.com": {Name: "example.com", UserID: other}}
		oerr, _ := runCreate(seed, "evil.example.com", true)
		if oerr != nil && oerr.Code == "domain_conflicts_tenant" {
			t.Fatalf("admin actor must not be gated by the cross-tenant guard, got %+v", oerr)
		}
	})

	// GH #1812: the same non-admin claim that is refused in the parent case above
	// is allowed once the parent's owner opts that domain into subdomain
	// delegation. Asserts the cross-tenant guard did not trip (other create-path
	// errors are tolerated, as in the same-owner case) — proving the create door
	// honors the flag end-to-end, not just the leaf.
	t.Run("non-admin claim under a DELEGATED parent passes the guard", func(t *testing.T) {
		seed := map[string]*models.Domain{
			"example.com": {Name: "example.com", UserID: other, AllowSubdomainDelegation: true},
		}
		oerr, _ := runCreate(seed, "sub1.example.com", false)
		if oerr != nil && oerr.Code == "domain_conflicts_tenant" {
			t.Fatalf("delegated parent must not trip the cross-tenant guard, got %+v", oerr)
		}
	})
}
