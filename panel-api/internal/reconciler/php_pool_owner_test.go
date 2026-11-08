package reconciler

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2421: changing a domain's owner left it bound to the old owner's PHP
// pool, and PHP failed on it until its version was picked again.

func newPoolOwnerReconciler(domain *models.Domain, pools map[string]*models.PHPPool) (*Reconciler, *fakeDomainRepo, *fakePHPPoolRepo) {
	doms := &fakeDomainRepo{domains: map[string]*models.Domain{domain.ID: domain}}
	pp := &fakePHPPoolRepo{pools: pools}
	return &Reconciler{domains: doms, phpPools: pp, log: slog.Default()}, doms, pp
}

func poolOwnerFixture() map[string]*models.PHPPool {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return map[string]*models.PHPPool{
		"alice-default": {ID: "alice-default", UserID: "alice", PHPVersion: "8.3", Status: "active", CreatedAt: t0},
		"alice-8.2":     {ID: "alice-8.2", UserID: "alice", PHPVersion: "8.2", Status: "active", CreatedAt: t0.Add(time.Hour)},
		"bob-default":   {ID: "bob-default", UserID: "bob", PHPVersion: "8.3", Status: "active", CreatedAt: t0},
	}
}

func TestEnsureDomainPHPBinding_MovesDomainToOwnersPoolOfSameVersion(t *testing.T) {
	domain := &models.Domain{ID: "d1", Name: "site.example", UserID: "bob", PHPPoolID: strp("alice-8.2")}
	r, doms, pp := newPoolOwnerReconciler(domain, poolOwnerFixture())

	r.ensureDomainPHPBinding(context.Background(), domain)

	bound := doms.domains["d1"].PHPPoolID
	if bound == nil || *bound == "alice-8.2" {
		t.Fatalf("domain still bound to the old owner's pool: %v", bound)
	}
	pool := pp.pools[*bound]
	if pool == nil || pool.UserID != "bob" || pool.PHPVersion != "8.2" {
		t.Fatalf("bound to %+v, want a PHP 8.2 pool of bob's", pool)
	}
	if pool.Status != "pending" {
		t.Errorf("new pool status = %q, want pending (the pool pass applies it)", pool.Status)
	}
	if domain.PHPPoolID == nil || *domain.PHPPoolID != *bound {
		t.Errorf("in-memory domain not updated for the rest of the pass: %v", domain.PHPPoolID)
	}
}

func TestEnsureDomainPHPBinding_OwnersDefaultServesItsVersion(t *testing.T) {
	domain := &models.Domain{ID: "d1", Name: "site.example", UserID: "bob", PHPPoolID: strp("alice-default")}
	r, doms, pp := newPoolOwnerReconciler(domain, poolOwnerFixture())

	r.ensureDomainPHPBinding(context.Background(), domain)

	if got := doms.domains["d1"].PHPPoolID; got == nil || *got != "bob-default" {
		t.Fatalf("bound to %v, want bob-default", got)
	}
	if len(pp.pools) != 3 {
		t.Errorf("%d pools, want 3: no pool is created for the owner's default version", len(pp.pools))
	}
}

func TestEnsureDomainPHPBinding_KeepsOwnPool(t *testing.T) {
	domain := &models.Domain{ID: "d1", Name: "site.example", UserID: "alice", PHPPoolID: strp("alice-8.2")}
	r, doms, pp := newPoolOwnerReconciler(domain, poolOwnerFixture())

	r.ensureDomainPHPBinding(context.Background(), domain)

	if got := doms.domains["d1"].PHPPoolID; got == nil || *got != "alice-8.2" {
		t.Fatalf("binding changed to %v, want alice-8.2", got)
	}
	if len(pp.pools) != 3 {
		t.Errorf("%d pools, want 3", len(pp.pools))
	}
}

// An owner without a pool yet: the binding waits for the pool pass to create
// the owner's default pool.
func TestEnsureDomainPHPBinding_OwnerWithoutPoolWaits(t *testing.T) {
	domain := &models.Domain{ID: "d1", Name: "site.example", UserID: "carol", PHPPoolID: strp("alice-8.2")}
	r, doms, pp := newPoolOwnerReconciler(domain, poolOwnerFixture())

	r.ensureDomainPHPBinding(context.Background(), domain)

	if got := doms.domains["d1"].PHPPoolID; got == nil || *got != "alice-8.2" {
		t.Fatalf("binding changed to %v with no pool to move to", got)
	}
	if len(pp.pools) != 3 {
		t.Errorf("%d pools, want 3: a versioned pool needs the owner's default", len(pp.pools))
	}
}
