package domainops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeAliasFinder struct {
	held    map[string]bool
	errFor  string
	err     error
	queried []string
}

func (f *fakeAliasFinder) FindByHostname(_ context.Context, host string) (*models.WebDomainAlias, error) {
	f.queried = append(f.queried, host)
	if f.err != nil && host == f.errFor {
		return nil, f.err
	}
	if f.held[host] {
		return &models.WebDomainAlias{Hostname: host}, nil
	}
	return nil, repository.ErrNotFound
}

func TestAliasCollision(t *testing.T) {
	ctx := context.Background()

	t.Run("every helper server_name is checked, after normalizing", func(t *testing.T) {
		f := &fakeAliasFinder{held: map[string]bool{"mail.shop.example.com": true}}
		hit, clash, err := AliasCollision(ctx, f, "  Shop.Example.COM ")
		if err != nil || !clash || hit != "mail.shop.example.com" {
			t.Fatalf("want a clash on mail.shop.example.com, got %q %v %v", hit, clash, err)
		}
	})

	t.Run("a free name scans all six candidates", func(t *testing.T) {
		f := &fakeAliasFinder{}
		if _, clash, err := AliasCollision(ctx, f, "shop.example.com"); clash || err != nil {
			t.Fatalf("want no clash, got %v %v", clash, err)
		}
		if len(f.queried) != 6 {
			t.Fatalf("want 6 candidates (apex + 5 helpers), got %v", f.queried)
		}
	})

	t.Run("a lookup error fails closed", func(t *testing.T) {
		store := errors.New("db down")
		f := &fakeAliasFinder{errFor: "www.shop.example.com", err: store}
		_, clash, err := AliasCollision(ctx, f, "shop.example.com")
		if !errors.Is(err, store) || clash {
			t.Fatalf("want the store error and no clash verdict, got %v %v", clash, err)
		}
	})

	t.Run("an unwired finder is no collision", func(t *testing.T) {
		if _, clash, err := AliasCollision(ctx, nil, "shop.example.com"); clash || err != nil {
			t.Fatalf("want no clash, got %v %v", clash, err)
		}
	})

	t.Run("the prefix list cannot be changed through its accessor", func(t *testing.T) {
		p := AliasHelperPrefixes()
		p[1] = "nothing."
		f := &fakeAliasFinder{held: map[string]bool{"mail.shop.example.com": true}}
		if _, clash, _ := AliasCollision(ctx, f, "shop.example.com"); !clash {
			t.Fatal("mutating the returned slice changed the guard")
		}
	})
}

type fakeSuffixFinder struct {
	byName  map[string]*models.Domain
	subs    []models.Domain
	nameErr error
	subErr  error
}

func (f *fakeSuffixFinder) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if f.nameErr != nil {
		return nil, f.nameErr
	}
	if d, ok := f.byName[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeSuffixFinder) FindStrictSubdomains(context.Context, string) ([]models.Domain, error) {
	return f.subs, f.subErr
}

func TestCrossTenantSuffixCollision(t *testing.T) {
	ctx := context.Background()

	t.Run("nesting under another tenant's domain clashes on the ancestor", func(t *testing.T) {
		f := &fakeSuffixFinder{byName: map[string]*models.Domain{"example.com": {Name: "example.com", UserID: "victim"}}}
		hit, clash, err := CrossTenantSuffixCollision(ctx, f, "evil.example.com", "attacker")
		if err != nil || !clash || hit != "example.com" {
			t.Fatalf("want a clash on example.com, got %q %v %v", hit, clash, err)
		}
	})

	t.Run("nesting under your own domain is allowed", func(t *testing.T) {
		f := &fakeSuffixFinder{byName: map[string]*models.Domain{"example.com": {Name: "example.com", UserID: "u1"}}}
		if _, clash, err := CrossTenantSuffixCollision(ctx, f, "blog.example.com", "u1"); clash || err != nil {
			t.Fatalf("want no clash, got %v %v", clash, err)
		}
	})

	t.Run("each ancestor must consent: a delegating parent does not cover a non-delegating grandparent", func(t *testing.T) {
		f := &fakeSuffixFinder{byName: map[string]*models.Domain{
			"b.a.com": {Name: "b.a.com", UserID: "t2", AllowSubdomainDelegation: true},
			"a.com":   {Name: "a.com", UserID: "t1"},
		}}
		hit, clash, err := CrossTenantSuffixCollision(ctx, f, "c.b.a.com", "t3")
		if err != nil || !clash || hit != "a.com" {
			t.Fatalf("want a clash on a.com, got %q %v %v", hit, clash, err)
		}
	})

	t.Run("wrapping another tenant's subdomain reports the claimant's own name", func(t *testing.T) {
		f := &fakeSuffixFinder{subs: []models.Domain{{Name: "secret.example.com", UserID: "victim"}}}
		hit, clash, err := CrossTenantSuffixCollision(ctx, f, "example.com", "attacker")
		if err != nil || !clash || hit != "example.com" {
			t.Fatalf("want a clash reported as example.com (never the victim's name), got %q %v %v", hit, clash, err)
		}
	})

	t.Run("a delegation flag never allows wrapping a subdomain", func(t *testing.T) {
		f := &fakeSuffixFinder{subs: []models.Domain{{Name: "x.example.com", UserID: "victim", AllowSubdomainDelegation: true}}}
		if _, clash, _ := CrossTenantSuffixCollision(ctx, f, "example.com", "attacker"); !clash {
			t.Fatal("the child direction must ignore delegation")
		}
	})

	t.Run("lookup errors fail closed in both directions", func(t *testing.T) {
		store := errors.New("db down")
		if _, clash, err := CrossTenantSuffixCollision(ctx, &fakeSuffixFinder{nameErr: store}, "evil.example.com", "u1"); !errors.Is(err, store) || clash {
			t.Fatalf("parent: want the store error, got %v %v", clash, err)
		}
		if _, clash, err := CrossTenantSuffixCollision(ctx, &fakeSuffixFinder{subErr: store}, "example.com", "u1"); !errors.Is(err, store) || clash {
			t.Fatalf("child: want the store error, got %v %v", clash, err)
		}
	})

	t.Run("an unwired finder is no collision", func(t *testing.T) {
		if _, clash, err := CrossTenantSuffixCollision(ctx, nil, "evil.example.com", "u1"); clash || err != nil {
			t.Fatalf("want no clash, got %v %v", clash, err)
		}
	})
}
