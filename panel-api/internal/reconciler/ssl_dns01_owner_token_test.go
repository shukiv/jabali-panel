package reconciler

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dns01"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

type fakeUserCFTokens struct {
	rows map[string]*models.UserCloudflareToken
	err  error
}

func (f *fakeUserCFTokens) Get(_ context.Context, uid string) (*models.UserCloudflareToken, error) {
	if f.err != nil {
		return nil, f.err
	}
	if row, ok := f.rows[uid]; ok {
		return row, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeUserCFTokens) Put(context.Context, *models.UserCloudflareToken) error { return nil }
func (f *fakeUserCFTokens) Delete(context.Context, string) error                   { return nil }

// GH #2032: the domain owner's own token routes the challenge; saving one
// drops a cached "no provider" decision at once rather than after the
// negative TTL; another user's token never routes the domain.
func TestDNS01Route_OwnerToken(t *testing.T) {
	r, _, _, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com", "lia.ns.cloudflare.com"}, "")
	key := &ssokey.Key{}
	tokens := &fakeUserCFTokens{rows: map[string]*models.UserCloudflareToken{}}
	r.WithDNS01Key(key).WithCloudflareTokens(tokens)
	r.dns01NewFinder = func(token string) dns01.ZoneFinder {
		if token == "owner-token" {
			return fixedZoneFinder{id: "zone-owner"}
		}
		return fixedZoneFinder{id: ""}
	}
	sealed, err := key.Seal([]byte("owner-token"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &models.ServerSettings{}
	ctx := context.Background()

	if got := r.dns01Route(ctx, srv, dom.Name); got.Provider != dns01.ProviderNone {
		t.Fatalf("no token anywhere: provider = %s, want none", got.Provider)
	}

	tokens.rows[dom.UserID] = &models.UserCloudflareToken{UserID: dom.UserID, TokenEnc: sealed, UpdatedAt: time.Now()}
	got := r.dns01Route(ctx, srv, dom.Name)
	if got.Provider != dns01.ProviderCloudflare || got.CFZoneID != "zone-owner" {
		t.Fatalf("after the owner saved a token: route = %+v, want Cloudflare zone-owner", got)
	}

	delete(tokens.rows, dom.UserID)
	tokens.rows["u2"] = &models.UserCloudflareToken{UserID: "u2", TokenEnc: sealed, UpdatedAt: time.Now()}
	if got := r.dns01Route(ctx, srv, dom.Name); got.Provider != dns01.ProviderNone {
		t.Fatalf("only another user's token: route = %+v, want none", got)
	}
}

// A decision made while a token could not be read is not cached: once the
// read works again the owner's token routes the domain at once, not after
// the negative TTL.
func TestDNS01Route_TokenReadFailureNotCached(t *testing.T) {
	r, _, _, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com", "lia.ns.cloudflare.com"}, "")
	key := &ssokey.Key{}
	sealed, err := key.Seal([]byte("owner-token"))
	if err != nil {
		t.Fatal(err)
	}
	tokens := &fakeUserCFTokens{
		rows: map[string]*models.UserCloudflareToken{dom.UserID: {UserID: dom.UserID, TokenEnc: sealed, UpdatedAt: time.Now()}},
		err:  errors.New("db down"),
	}
	r.WithDNS01Key(key).WithCloudflareTokens(tokens)
	r.dns01NewFinder = func(string) dns01.ZoneFinder { return fixedZoneFinder{id: "zone-owner"} }
	srv := &models.ServerSettings{}

	if got := r.dns01Route(context.Background(), srv, dom.Name); got.Provider != dns01.ProviderNone {
		t.Fatalf("token unreadable: route = %+v, want none", got)
	}
	if _, cached := r.dns01RouteCache[dom.Name]; cached {
		t.Fatal("a decision made without every token was cached")
	}
	tokens.err = nil
	if got := r.dns01Route(context.Background(), srv, dom.Name); got.Provider != dns01.ProviderCloudflare {
		t.Fatalf("token readable again: route = %+v, want Cloudflare", got)
	}
}
