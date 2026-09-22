package api

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// stubDomainRepo satisfies repository.DomainRepository by embedding it (so only
// the two methods the cross-tenant guard calls need real bodies) and lets a test
// script the parent lookup (FindByName) and the child lookup
// (FindStrictSubdomains), including their failure branches.
type stubDomainRepo struct {
	repository.DomainRepository
	byName        map[string]*models.Domain // exact-name rows, for the parent walk
	subs          []models.Domain           // strict-subdomain rows, for the child scan
	findByNameErr error                     // non-ErrNotFound error → parent lookup fails
	subsErr       error                     // error → child lookup fails
}

func (s *stubDomainRepo) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if s.findByNameErr != nil {
		return nil, s.findByNameErr
	}
	if d, ok := s.byName[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

func (s *stubDomainRepo) FindStrictSubdomains(_ context.Context, _ string) ([]models.Domain, error) {
	if s.subsErr != nil {
		return nil, s.subsErr
	}
	return s.subs, nil
}

// CrossTenantSuffixCollision is the GH #1789 cross-tenant DNS subdomain-hijack
// guard. It must flag a claim that nests under (parent direction) or wraps
// (child direction) a DIFFERENTLY-owned domain, allow same-owner nesting, defer
// exact-name collisions to the unique index, and fail CLOSED on a lookup error.
func TestCrossTenantSuffixCollision(t *testing.T) {
	const me, other = "user-me", "user-other"
	boom := errors.New("db down")

	cases := []struct {
		name      string
		claim     string
		repo      *stubDomainRepo
		wantHit   string
		wantClash bool
		wantErr   bool
	}{
		{
			name:  "parent owned by another tenant clashes",
			claim: "evil.example.com",
			repo: &stubDomainRepo{byName: map[string]*models.Domain{
				"example.com": {Name: "example.com", UserID: other},
			}},
			wantHit:   "example.com",
			wantClash: true,
		},
		{
			name:  "parent owned by the same tenant is allowed",
			claim: "shop.example.com",
			repo: &stubDomainRepo{byName: map[string]*models.Domain{
				"example.com": {Name: "example.com", UserID: me},
			}},
			wantClash: false,
		},
		{
			// The hit must be the claimant's OWN name, never the conflicting
			// subdomain — echoing subs[i].Name would leak another tenant's zone
			// existence into the 409 body (GH #1789 leak guard).
			name:  "child owned by another tenant clashes",
			claim: "example.com",
			repo: &stubDomainRepo{subs: []models.Domain{
				{Name: "secret-staging.example.com", UserID: other},
			}},
			wantHit:   "example.com",
			wantClash: true,
		},
		{
			name:  "child owned by the same tenant is allowed",
			claim: "example.com",
			repo: &stubDomainRepo{subs: []models.Domain{
				{Name: "sub.example.com", UserID: me},
			}},
			wantClash: false,
		},
		{
			// GH #1812: the differently-owned parent opted into delegation, so a
			// tenant may self-service a subdomain of it. Without the flag this is
			// the "parent owned by another tenant clashes" case above — so this
			// case is RED on the pre-#1812 guard and proves the wiring.
			name:  "delegated parent (another tenant, opt-in) is allowed",
			claim: "sub1.example.com",
			repo: &stubDomainRepo{byName: map[string]*models.Domain{
				"example.com": {Name: "example.com", UserID: other, AllowSubdomainDelegation: true},
			}},
			wantClash: false,
		},
		{
			// Every differently-owned ancestor must independently consent. The
			// nearer ancestor delegates but the registrable root does not, so the
			// claim still clashes on the non-delegated root — the walk does NOT
			// short-circuit on the first delegated ancestor (GH #1812).
			name:  "mixed chain: nearer parent delegates, root does not — still clashes on root",
			claim: "c.b.a.com",
			repo: &stubDomainRepo{byName: map[string]*models.Domain{
				"b.a.com": {Name: "b.a.com", UserID: other, AllowSubdomainDelegation: true},
				"a.com":   {Name: "a.com", UserID: other, AllowSubdomainDelegation: false},
			}},
			wantHit:   "a.com",
			wantClash: true,
		},
		{
			// Delegation grants nesting UNDER a domain, never the right to claim a
			// parent zone OVER another tenant's subdomain. The child direction
			// ignores the flag entirely, so this stays a clash (GH #1812).
			name:  "child direction ignores delegation — claiming a parent over another's sub still clashes",
			claim: "example.com",
			repo: &stubDomainRepo{subs: []models.Domain{
				{Name: "secret.example.com", UserID: other, AllowSubdomainDelegation: true},
			}},
			wantHit:   "example.com",
			wantClash: true,
		},
		{
			name:      "no related domains — no clash",
			claim:     "fresh.example.net",
			repo:      &stubDomainRepo{},
			wantClash: false,
		},
		{
			name:  "exact same name is deferred to the unique index, not a suffix clash",
			claim: "example.com",
			repo: &stubDomainRepo{byName: map[string]*models.Domain{
				"example.com": {Name: "example.com", UserID: other},
			}},
			wantClash: false,
		},
		{
			name:    "parent lookup error fails closed",
			claim:   "evil.example.com",
			repo:    &stubDomainRepo{findByNameErr: boom},
			wantErr: true,
		},
		{
			name:    "child lookup error fails closed",
			claim:   "example.com",
			repo:    &stubDomainRepo{subsErr: boom},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hit, clash, err := CrossTenantSuffixCollision(context.Background(), tc.repo, tc.claim, me)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected a lookup error (fail closed), got clash=%v hit=%q err=nil", clash, hit)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if clash != tc.wantClash {
				t.Fatalf("clash = %v, want %v (hit=%q)", clash, tc.wantClash, hit)
			}
			if clash && hit != tc.wantHit {
				t.Fatalf("hit = %q, want %q", hit, tc.wantHit)
			}
		})
	}
}

// A nil repo means the feature is unwired: the guard fails OPEN (no collision)
// exactly like AliasCollision, so an unconfigured deployment is never bricked.
func TestCrossTenantSuffixCollision_NilRepo(t *testing.T) {
	hit, clash, err := CrossTenantSuffixCollision(context.Background(), nil, "evil.example.com", "user-me")
	if err != nil || clash || hit != "" {
		t.Fatalf("nil repo: got hit=%q clash=%v err=%v, want empty/false/nil", hit, clash, err)
	}
}
