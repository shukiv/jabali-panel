package cftoken

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

const (
	userA = "01HZUSERA0000000000000000A"
	userB = "01HZUSERB0000000000000000B"
)

type fakeDomains struct {
	owners map[string]string // name → user id
	err    map[string]error
}

func (f fakeDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if err := f.err[name]; err != nil {
		return nil, err
	}
	if uid, ok := f.owners[name]; ok {
		return &models.Domain{Name: name, UserID: uid}, nil
	}
	return nil, repository.ErrNotFound
}

type fakeTokens struct {
	rows map[string]*models.UserCloudflareToken
	err  error
}

func (f *fakeTokens) Get(_ context.Context, uid string) (*models.UserCloudflareToken, error) {
	if f.err != nil {
		return nil, f.err
	}
	if row, ok := f.rows[uid]; ok {
		return row, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeTokens) Put(context.Context, *models.UserCloudflareToken) error { return nil }
func (f *fakeTokens) Delete(context.Context, string) error                   { return nil }

func seal(t *testing.T, key *ssokey.Key, tok string) []byte {
	t.Helper()
	b, err := key.Seal([]byte(tok))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixture: user A owns example.com and has a token; user B owns
// sub.example.com (nested under A's domain, as GH #1812 delegation or an
// admin allows) and has a token too; the server has its own.
func fixture(t *testing.T) (Resolver, *fakeTokens, *models.ServerSettings) {
	t.Helper()
	key := &ssokey.Key{}
	tokens := &fakeTokens{rows: map[string]*models.UserCloudflareToken{
		userA: {UserID: userA, TokenEnc: seal(t, key, "owner-a-token"), UpdatedAt: time.Unix(100, 0)},
		userB: {UserID: userB, TokenEnc: seal(t, key, "owner-b-token"), UpdatedAt: time.Unix(200, 0)},
	}}
	r := Resolver{
		Domains: fakeDomains{owners: map[string]string{"example.com": userA, "sub.example.com": userB}},
		Tokens:  tokens,
		Key:     key,
	}
	return r, tokens, &models.ServerSettings{CFAPITokenEnc: seal(t, key, "server-token")}
}

func tokensOf(s Set) []string {
	var out []string
	for _, c := range s.Candidates {
		out = append(out, string(c.Source)+":"+c.Token)
	}
	return out
}

func wantTokens(t *testing.T, got Set, want ...string) {
	t.Helper()
	g := tokensOf(got)
	if len(g) != len(want) {
		t.Fatalf("candidates = %v, want %v", g, want)
	}
	for i := range want {
		if g[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", g, want)
		}
	}
}

func TestFor_OwnerTokenFirstThenServer(t *testing.T) {
	r, _, srv := fixture(t)
	for _, name := range []string{"example.com", "www.example.com", "*.example.com", "WWW.Example.COM"} {
		set, err := r.For(context.Background(), srv, "example.com", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantTokens(t, set, "owner:owner-a-token", "server:server-token")
	}
}

// The isolation rule: a user's token is offered only for a name that user
// owns, on a certificate that user owns. Nesting puts both owners' names on
// one path; neither token may cross.
func TestFor_NeverCrossesTenants(t *testing.T) {
	r, _, srv := fixture(t)
	cases := []struct {
		name, lineage, certName string
		want                    []string
	}{
		// A's certificate carrying B's name: B's token is not exercised for
		// A's certificate, and A's token is not used for B's name.
		{"B's name on A's certificate", "example.com", "sub.example.com", []string{"server:server-token"}},
		{"below B's name on A's certificate", "example.com", "www.sub.example.com", []string{"server:server-token"}},
		// B's own certificate: B's token, never A's although A owns the parent.
		{"B's own certificate", "sub.example.com", "sub.example.com", []string{"owner:owner-b-token", "server:server-token"}},
		{"B's www", "sub.example.com", "www.sub.example.com", []string{"owner:owner-b-token", "server:server-token"}},
		// A certificate claiming B's lineage for A's name gets nothing of A's.
		{"A's name on B's certificate", "sub.example.com", "example.com", []string{"server:server-token"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set, err := r.For(context.Background(), srv, c.lineage, c.certName)
			if err != nil {
				t.Fatal(err)
			}
			wantTokens(t, set, c.want...)
		})
	}
}

// A lineage that is not a domain — a shared wildcard cert, a renewal conf
// written before GH #2032 that carries no lineage — gets the server token
// only, exactly as before.
func TestFor_NoTenantLineageIsServerOnly(t *testing.T) {
	r, _, srv := fixture(t)
	for _, lineage := range []string{"", "wildcard.example.com", "other.org"} {
		set, err := r.For(context.Background(), srv, lineage, "example.com")
		if err != nil {
			t.Fatalf("%q: %v", lineage, err)
		}
		wantTokens(t, set, "server:server-token")
	}
}

func TestFor_OwnerWithoutToken(t *testing.T) {
	r, tokens, srv := fixture(t)
	delete(tokens.rows, userA)
	set, err := r.For(context.Background(), srv, "example.com", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	wantTokens(t, set, "server:server-token")

	set, err = r.For(context.Background(), &models.ServerSettings{}, "example.com", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	wantTokens(t, set)
}

func TestFor_NoKeyNoTokens(t *testing.T) {
	r, _, srv := fixture(t)
	r.Key = nil
	set, err := r.For(context.Background(), srv, "example.com", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	wantTokens(t, set)
}

// A read that fails leaves the token out, keeps every other candidate, and
// reports the error so the caller does not cache the decision.
func TestFor_ReadFailures(t *testing.T) {
	boom := errors.New("db down")

	t.Run("domain lookup", func(t *testing.T) {
		r, _, srv := fixture(t)
		r.Domains = fakeDomains{owners: map[string]string{"example.com": userA}, err: map[string]error{"www.example.com": boom}}
		set, err := r.For(context.Background(), srv, "example.com", "www.example.com")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
		wantTokens(t, set, "server:server-token")
	})
	t.Run("lineage lookup", func(t *testing.T) {
		r, _, srv := fixture(t)
		r.Domains = fakeDomains{owners: map[string]string{"www.example.com": userA}, err: map[string]error{"example.com": boom}}
		set, err := r.For(context.Background(), srv, "example.com", "www.example.com")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
		wantTokens(t, set, "server:server-token")
	})
	t.Run("token row", func(t *testing.T) {
		r, tokens, srv := fixture(t)
		tokens.err = boom
		set, err := r.For(context.Background(), srv, "example.com", "example.com")
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want %v", err, boom)
		}
		wantTokens(t, set, "server:server-token")
	})
	t.Run("owner token sealed with another key", func(t *testing.T) {
		r, tokens, srv := fixture(t)
		other := &ssokey.Key{1}
		tokens.rows[userA].TokenEnc = seal(t, other, "owner-a-token")
		set, err := r.For(context.Background(), srv, "example.com", "example.com")
		if err == nil {
			t.Fatal("want an error for a token that cannot be unsealed")
		}
		wantTokens(t, set, "server:server-token")
	})
	t.Run("server token sealed with another key", func(t *testing.T) {
		r, _, srv := fixture(t)
		srv.CFAPITokenEnc = seal(t, &ssokey.Key{1}, "server-token")
		set, err := r.For(context.Background(), srv, "example.com", "example.com")
		if err == nil {
			t.Fatal("want an error for a token that cannot be unsealed")
		}
		wantTokens(t, set, "owner:owner-a-token")
	})
}

// Version changes whenever the tokens on offer change, so a cached route
// decision made with the old ones is dropped; it is stable otherwise.
func TestFor_Version(t *testing.T) {
	r, tokens, srv := fixture(t)
	ctx := context.Background()
	version := func() string {
		t.Helper()
		set, err := r.For(ctx, srv, "example.com", "example.com")
		if err != nil {
			t.Fatal(err)
		}
		return set.Version
	}

	base := version()
	if again := version(); again != base {
		t.Fatalf("version not stable: %q then %q", base, again)
	}

	tokens.rows[userA].UpdatedAt = time.Unix(101, 0)
	replaced := version()
	if replaced == base {
		t.Fatal("version unchanged after the owner replaced the token")
	}

	delete(tokens.rows, userA)
	removed := version()
	if removed == replaced {
		t.Fatal("version unchanged after the owner removed the token")
	}

	srv.CFAPITokenEnc = seal(t, r.Key, "server-token-2")
	if version() == removed {
		t.Fatal("version unchanged after the server token changed")
	}

	srv.CFAPITokenEnc = nil
	if version() == removed {
		t.Fatal("version unchanged after the server token was cleared")
	}
}
