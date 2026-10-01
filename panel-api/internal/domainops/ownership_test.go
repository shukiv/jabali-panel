package domainops

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ownershipPolicy is a fake OwnershipPolicyReader.
type ownershipPolicy struct {
	require bool
	err     error
}

func (p ownershipPolicy) GetSettings(context.Context) (models.DomainOwnershipSettings, error) {
	return models.DomainOwnershipSettings{ID: 1, RequireProof: p.require}, p.err
}

func verifiedDomain(name, owner string) *models.Domain {
	d := &models.Domain{ID: "id-" + name, Name: name, UserID: owner}
	d.OwnershipStatus = models.OwnershipVerified
	return d
}

func pendingDomain(name, owner string) *models.Domain {
	d := &models.Domain{ID: "id-" + name, Name: name, UserID: owner}
	d.OwnershipStatus = models.OwnershipPending
	return d
}

func TestOwnershipVerified(t *testing.T) {
	for _, st := range []string{"", models.OwnershipPending, "Verified", "verified ", "bogus"} {
		d := &models.Domain{}
		d.OwnershipStatus = st
		if OwnershipVerified(d) {
			t.Fatalf("status %q must count as pending", st)
		}
	}
	if OwnershipVerified(nil) {
		t.Fatal("a nil domain must count as pending")
	}
	if !OwnershipVerified(verifiedDomain("a.test", "u")) {
		t.Fatal("verified must count as verified")
	}
}

func TestDecideOwnership(t *testing.T) {
	ctx := context.Background()
	on := ownershipPolicy{require: true}

	cases := []struct {
		name     string
		deps     OwnershipDeps
		domain   string
		admin    bool
		assert   *OwnershipAssertion
		verified bool
		method   string
	}{
		{name: "a tenant's new name is pending", deps: OwnershipDeps{Domains: newCreateStore(), Policy: on},
			domain: "shop.example.com"},
		{name: "a policy read error keeps it pending (fail closed)",
			deps:   OwnershipDeps{Domains: newCreateStore(), Policy: ownershipPolicy{require: false, err: errors.New("db down")}},
			domain: "shop.example.com"},
		{name: "a nil policy means proof is required", deps: OwnershipDeps{Domains: newCreateStore()},
			domain: "shop.example.com"},
		{name: "the policy off verifies as policy_off", deps: OwnershipDeps{Policy: ownershipPolicy{require: false}},
			domain: "shop.example.com", verified: true, method: models.OwnershipMethodPolicyOff},
		{name: "an admin actor verifies as admin", deps: OwnershipDeps{Policy: on},
			domain: "shop.example.com", admin: true, verified: true, method: models.OwnershipMethodAdmin},
		{name: "an assertion verifies with its method", deps: OwnershipDeps{Policy: on},
			domain: "shop.example.com", assert: &OwnershipAssertion{Method: models.OwnershipMethodMigration},
			verified: true, method: models.OwnershipMethodMigration},
		{name: "a same-owner verified parent covers a subdomain",
			deps:   OwnershipDeps{Domains: newCreateStore(verifiedDomain("example.com", "u1")), Policy: on},
			domain: "shop.example.com", verified: true, method: models.OwnershipMethodParent},
		{name: "a same-owner PENDING parent does not",
			deps:   OwnershipDeps{Domains: newCreateStore(pendingDomain("example.com", "u1")), Policy: on},
			domain: "shop.example.com"},
		{name: "a verified parent of another owner without delegation does not",
			deps:   OwnershipDeps{Domains: newCreateStore(verifiedDomain("example.com", "u2")), Policy: on},
			domain: "shop.example.com"},
		{name: "a delegating verified parent of another owner covers",
			deps: OwnershipDeps{Domains: newCreateStore(func() *models.Domain {
				d := verifiedDomain("example.com", "u2")
				d.AllowSubdomainDelegation = true
				return d
			}()), Policy: on},
			domain: "shop.example.com", verified: true, method: models.OwnershipMethodParent},
		{name: "the NEAREST hosted ancestor decides: a pending b.example.com blocks a verified example.com",
			deps: OwnershipDeps{Domains: newCreateStore(verifiedDomain("example.com", "u1"),
				pendingDomain("b.example.com", "u1")), Policy: on},
			domain: "c.b.example.com"},
		{name: "an unhosted middle label is skipped on the way up",
			deps:   OwnershipDeps{Domains: newCreateStore(verifiedDomain("example.com", "u1")), Policy: on},
			domain: "c.b.example.com", verified: true, method: models.OwnershipMethodParent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := DecideOwnership(ctx, tc.deps, tc.domain, "u1", tc.admin, tc.assert)
			if err != nil {
				t.Fatalf("decide: %v", err)
			}
			if dec.Verified != tc.verified || dec.Method != tc.method {
				t.Fatalf("want verified=%v method=%q, got %+v", tc.verified, tc.method, dec)
			}
		})
	}

	t.Run("an assertion with an unknown method is refused", func(t *testing.T) {
		for _, m := range []string{"", models.OwnershipMethodDNSTXT, models.OwnershipMethodParent, models.OwnershipMethodLegacy, "anything"} {
			_, err := DecideOwnership(ctx, OwnershipDeps{Policy: on}, "a.test", "u1", false, &OwnershipAssertion{Method: m})
			if !errors.Is(err, ErrOwnershipAssertion) {
				t.Fatalf("method %q: want ErrOwnershipAssertion, got %v", m, err)
			}
		}
	})

	t.Run("an ancestor lookup error keeps the name pending", func(t *testing.T) {
		dec, err := DecideOwnership(ctx, OwnershipDeps{Domains: erroringFinder{}, Policy: on}, "shop.example.com", "u1", false, nil)
		if err != nil || dec.Verified {
			t.Fatalf("want pending, got %+v %v", dec, err)
		}
	})
}

type erroringFinder struct{}

func (erroringFinder) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, errors.New("db down")
}

func TestApplyOwnershipDecision(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)

	var p models.OwnershipState
	if err := ApplyOwnershipDecision(&p, OwnershipDecision{}, now); err != nil {
		t.Fatal(err)
	}
	if p.OwnershipStatus != models.OwnershipPending || p.OwnershipMethod != "" || !hex64.MatchString(p.OwnershipToken) ||
		p.OwnershipPendingSince == nil || !p.OwnershipPendingSince.Equal(now) ||
		p.OwnershipNextCheckAt == nil || !p.OwnershipNextCheckAt.Equal(now) || p.OwnershipVerifiedAt != nil {
		t.Fatalf("unexpected pending state: %+v", p)
	}

	var v models.OwnershipState
	if err := ApplyOwnershipDecision(&v, OwnershipDecision{Verified: true, Method: models.OwnershipMethodAdmin}, now); err != nil {
		t.Fatal(err)
	}
	if v.OwnershipStatus != models.OwnershipVerified || v.OwnershipMethod != models.OwnershipMethodAdmin ||
		v.OwnershipVerifiedAt == nil || v.OwnershipPendingSince != nil || !hex64.MatchString(v.OwnershipToken) {
		t.Fatalf("unexpected verified state: %+v", v)
	}
	if v.OwnershipToken == p.OwnershipToken {
		t.Fatal("two rows must never share a token")
	}
}

// fakeLookups builds OwnershipLookups from canned answers.
func fakeLookups(txt []dnsverify.TXTAnswer, ns map[string][]string, nsQueried map[string]bool) (OwnershipLookups, *[]string) {
	var asked []string
	return OwnershipLookups{
		TXT: func(_ context.Context, name string) []dnsverify.TXTAnswer {
			asked = append(asked, "TXT "+name)
			return txt
		},
		NS: func(_ context.Context, name string) ([]string, bool) {
			asked = append(asked, "NS "+name)
			return ns[name], nsQueried[name]
		},
	}, &asked
}

func TestCheckOwnership(t *testing.T) {
	ctx := context.Background()
	const token = "abc123"
	want := "jabali-verify=" + token
	ok := func(recs ...string) dnsverify.TXTAnswer {
		return dnsverify.TXTAnswer{Definitive: true, Records: recs}
	}
	nothing := dnsverify.TXTAnswer{Definitive: true}
	down := dnsverify.TXTAnswer{}
	ownNS := []string{"ns1.panel.test", "NS2.panel.test."}

	cases := []struct {
		name      string
		txt       []dnsverify.TXTAnswer
		ns        map[string][]string
		nsQueried map[string]bool
		result    string
	}{
		{name: "three resolvers agree", txt: []dnsverify.TXTAnswer{ok(want), ok(want), ok(want)}, result: models.OwnershipResultVerified},
		{name: "two of three are enough", txt: []dnsverify.TXTAnswer{ok(want), down, ok("other", want)}, result: models.OwnershipResultVerified},
		{name: "one resolver alone never verifies", txt: []dnsverify.TXTAnswer{ok(want), nothing, nothing}, result: models.OwnershipResultPropagating},
		{name: "one resolver with two unreachable is still only propagating", txt: []dnsverify.TXTAnswer{ok(want), down, down}, result: models.OwnershipResultPropagating},
		{name: "a value that merely contains the token does not match",
			txt: []dnsverify.TXTAnswer{ok(want + "x"), ok(" " + want), ok("jabali-verify=ABC123")}, result: models.OwnershipResultMismatch},
		{name: "no record anywhere", txt: []dnsverify.TXTAnswer{nothing, nothing, nothing}, result: models.OwnershipResultNotFound},
		{name: "nameservers that are all ours", txt: []dnsverify.TXTAnswer{nothing, nothing, nothing},
			ns: map[string][]string{"shop.example.com": {"ns1.panel.test", "ns2.panel.test"}}, nsQueried: map[string]bool{"shop.example.com": true},
			result: models.OwnershipResultNSPointsHere},
		{name: "one foreign nameserver is not 'points here'", txt: []dnsverify.TXTAnswer{nothing, nothing, nothing},
			ns: map[string][]string{"shop.example.com": {"ns1.panel.test", "ns.elsewhere.test"}}, nsQueried: map[string]bool{"shop.example.com": true},
			result: models.OwnershipResultNotFound},
		{name: "no answer for the name while the TLD resolves", txt: []dnsverify.TXTAnswer{down, down, down},
			nsQueried: map[string]bool{"com": true}, result: models.OwnershipResultDNSUnresolvable},
		{name: "no answer at all", txt: []dnsverify.TXTAnswer{down, down, down}, result: models.OwnershipResultResolversUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			look, asked := fakeLookups(tc.txt, tc.ns, tc.nsQueried)
			if got := CheckOwnership(ctx, look, "Shop.Example.com", token, ownNS); got != tc.result {
				t.Fatalf("want %s, got %s", tc.result, got)
			}
			if (*asked)[0] != "TXT _jabali-challenge.shop.example.com" {
				t.Fatalf("must read the challenge name, asked %v", *asked)
			}
		})
	}

	t.Run("an empty token never verifies", func(t *testing.T) {
		look, _ := fakeLookups([]dnsverify.TXTAnswer{ok("jabali-verify="), ok("jabali-verify="), ok("jabali-verify=")}, nil, nil)
		if got := CheckOwnership(ctx, look, "a.test", "", ownNS); got == models.OwnershipResultVerified {
			t.Fatal("an empty token must never verify")
		}
	})
}

func TestNextOwnershipCheck(t *testing.T) {
	start := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		age  time.Duration
		wait time.Duration
	}{
		{0, time.Minute},
		{4 * time.Minute, time.Minute},
		{10 * time.Minute, 5 * time.Minute},
		{45 * time.Minute, 15 * time.Minute},
		{3 * time.Hour, time.Hour},
		{6 * 24 * time.Hour, time.Hour},
		{8 * 24 * time.Hour, 24 * time.Hour},
	}
	for _, tc := range cases {
		now := start.Add(tc.age)
		if got := NextOwnershipCheck(&start, now); !got.Equal(now.Add(tc.wait)) {
			t.Fatalf("age %s: want +%s, got +%s", tc.age, tc.wait, got.Sub(now))
		}
	}
}

func TestOwnershipExpiry(t *testing.T) {
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	d := pendingDomain("a.test", "u1")
	d.OwnershipPendingSince = &since
	if at := DomainOwnershipExpires(d); at == nil || !at.Equal(since.Add(14*24*time.Hour)) {
		t.Fatalf("a never-verified pending domain expires 14 days in, got %v", at)
	}

	verifiedOnce := *d
	vt := since.Add(-time.Hour)
	verifiedOnce.OwnershipVerifiedAt = &vt
	if DomainOwnershipExpires(&verifiedOnce) != nil {
		t.Fatal("a revoked domain (verified before) must not expire on its own")
	}
	for _, mut := range []func(*models.Domain){
		func(x *models.Domain) { x.IsPanelPrimary = true },
		func(x *models.Domain) { x.ManagedBy = models.DomainManagedByDockerApp },
		func(x *models.Domain) { x.OwnershipStatus = models.OwnershipVerified },
		func(x *models.Domain) { x.OwnershipPendingSince = nil },
	} {
		c := *d
		mut(&c)
		if DomainOwnershipExpires(&c) != nil {
			t.Fatalf("must not expire: %+v", c)
		}
	}
}

func TestPreviewGate(t *testing.T) {
	d := pendingDomain("a.test", "u1")
	d.OwnershipToken = "t1"
	g := PreviewGate(d)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(g) {
		t.Fatalf("gate must be 32 hex chars, got %q", g)
	}
	d2 := *d
	d2.OwnershipToken = "t2"
	if PreviewGate(&d2) == g {
		t.Fatal("a new token must change the gate")
	}
	if strings.Contains(g, "t1") {
		t.Fatal("the gate must not carry the token itself")
	}
}

func TestCreateOwnership(t *testing.T) {
	ctx := context.Background()
	uname := "alice"
	owner := &models.User{ID: "u1", Username: &uname}
	wild := `["*.example.com"]`
	deps := func(s *createStore) CreateDeps {
		return CreateDeps{
			Domains: s, Users: createOwners{"u1": owner}, Ownership: ownershipPolicy{require: true},
			SharedCerts: &fakeCertLister{certs: []models.SharedCertificate{{ID: "c1", SANs: &wild}}},
		}
	}
	base := CreateInput{OwnerID: "u1", Name: "shop.example.com"}

	t.Run("a tenant's unproven name is stored pending and only the reconcile runs", func(t *testing.T) {
		s, l := newCreateStore(), &hookLog{}
		res, err := Create(ctx, deps(s), l.hooks(), base)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if got := strings.Join(l.calls, ","); got != "schedule" {
			t.Fatalf("a pending domain must get no inline SSL, no mail enable, no shared attach: %s", got)
		}
		d := res.Domain
		if !res.OwnershipPending || d.OwnershipStatus != models.OwnershipPending || d.OwnershipToken == "" ||
			d.OwnershipPendingSince == nil || d.SSLMode != models.SSLModeLE || res.SharedCert != nil {
			t.Fatalf("unexpected pending create: %+v %+v", res, d.OwnershipState)
		}
	})

	t.Run("an admin's name is verified and the fast paths run", func(t *testing.T) {
		s, l := newCreateStore(), &hookLog{}
		in := base
		in.ActorIsAdmin = true
		res, err := Create(ctx, deps(s), l.hooks(), in)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if res.OwnershipPending || res.Domain.OwnershipMethod != models.OwnershipMethodAdmin || res.SharedCert == nil {
			t.Fatalf("want a verified admin create with the shared cert attached, got %+v", res)
		}
	})

	t.Run("an assertion verifies without skipping the tenant name guards", func(t *testing.T) {
		other := verifiedDomain("example.com", "u2")
		s, l := newCreateStore(other), &hookLog{}
		in := base
		in.Ownership = &OwnershipAssertion{Method: models.OwnershipMethodRestore}
		if _, err := Create(ctx, deps(s), l.hooks(), in); !errors.Is(err, ErrDomainConflictsTenant) {
			t.Fatalf("the cross-tenant guard must still run for a non-admin actor, got %v", err)
		}
		s = newCreateStore()
		res, err := Create(ctx, deps(s), l.hooks(), in)
		if err != nil || res.Domain.OwnershipMethod != models.OwnershipMethodRestore {
			t.Fatalf("want verified by restore, got %v %+v", err, res)
		}
	})
}

// StampOwnership records Create's decision on a row a door builds itself.
func TestStampOwnership(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	tenant := &models.Domain{ID: "d1", UserID: "u1", Name: "shop.test"}
	if err := StampOwnership(context.Background(), OwnershipDeps{Policy: ownershipPolicy{require: true}}, tenant, false, now); err != nil {
		t.Fatal(err)
	}
	if tenant.OwnershipStatus != models.OwnershipPending || len(tenant.OwnershipToken) != 64 ||
		tenant.OwnershipPendingSince == nil || !tenant.OwnershipPendingSince.Equal(now) {
		t.Fatalf("a tenant's unproven name must be stamped pending with a token, got %+v", tenant.OwnershipState)
	}
	admin := &models.Domain{ID: "d2", UserID: "u1", Name: "panel.test"}
	if err := StampOwnership(context.Background(), OwnershipDeps{Policy: ownershipPolicy{require: true}}, admin, true, now); err != nil {
		t.Fatal(err)
	}
	if admin.OwnershipStatus != models.OwnershipVerified || admin.OwnershipMethod != models.OwnershipMethodAdmin {
		t.Fatalf("an admin's row must be stamped verified (admin), got %+v", admin.OwnershipState)
	}
}

// GH #1816 / ADR-0170 section 5: a rename's new name gets the create-time
// decision; the domain's own current name is never its parent; an unproven
// name is refused for a domain whose mail is registered.
func TestPlanRenameOwnership(t *testing.T) {
	ctx := context.Background()
	on := OwnershipDeps{Policy: ownershipPolicy{require: true}}
	verified := models.OwnershipState{OwnershipStatus: models.OwnershipVerified}
	self := &models.Domain{ID: "d1", UserID: "u1", Name: "example.com", OwnershipState: verified}
	parent := &models.Domain{ID: "d2", UserID: "u1", Name: "brand.test", OwnershipState: verified}
	selector := "jabali"
	withMail := *self
	withMail.DkimSelector = &selector

	cases := []struct {
		name     string
		d        *models.Domain
		newName  string
		admin    bool
		wantErr  error
		verified bool
		method   string
	}{
		{name: "an unproven name goes pending", d: self, newName: "other.test"},
		{name: "the domain's own name is not the new name's parent", d: self, newName: "shop.example.com"},
		{name: "a verified parent of the owner's covers it", d: self, newName: "shop.brand.test",
			verified: true, method: models.OwnershipMethodParent},
		{name: "an admin rename is verified", d: &withMail, newName: "other.test", admin: true,
			verified: true, method: models.OwnershipMethodAdmin},
		{name: "registered mail refuses an unproven name", d: &withMail, newName: "other.test",
			wantErr: ErrRenameUnprovenMail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := on
			deps.Domains = newCreateStore(self, parent)
			dec, err := PlanRenameOwnership(ctx, deps, tc.d, tc.newName, tc.admin)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if dec.Verified != tc.verified || dec.Method != tc.method {
				t.Fatalf("decision = %+v, want verified=%v method=%q", dec, tc.verified, tc.method)
			}
		})
	}
}
