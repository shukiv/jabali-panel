package reconciler

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeAliasRepo is a minimal WebDomainAliasRepository for the reconciler
// dispatch tests — only ListHostnamesByDomainID is exercised (GH #1625).
type fakeAliasRepo struct{ byDomain map[string][]string }

func (f *fakeAliasRepo) ListHostnamesByDomainID(_ context.Context, domainID string) ([]string, error) {
	return f.byDomain[domainID], nil
}
func (f *fakeAliasRepo) Create(context.Context, *models.WebDomainAlias) error { return nil }
func (f *fakeAliasRepo) FindByID(context.Context, string) (*models.WebDomainAlias, error) {
	return nil, nil
}
func (f *fakeAliasRepo) ListByDomain(context.Context, string) ([]models.WebDomainAlias, error) {
	return nil, nil
}
func (f *fakeAliasRepo) FindByHostname(context.Context, string) (*models.WebDomainAlias, error) {
	return nil, nil
}
func (f *fakeAliasRepo) Delete(context.Context, string) error { return nil }

// GH #1625: aliases must reach the agent's domain.create payload (so nginx
// renders them into server_name) AND participate in the dispatch-change gate,
// so adding/removing an alias re-dispatches while an unchanged set does not.
func TestCreateDomainOnAgent_AliasesReachAgentAndTripGate(t *testing.T) {
	r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	ar := &fakeAliasRepo{byDomain: map[string][]string{dom.ID: {"shop.example.net"}}}
	r.WithWebDomainAliases(ar)

	r.createDomainOnAgent(context.Background(), dom, false)
	call, ok := findAgentCall(ag, "domain.create")
	if !ok {
		t.Fatal("domain.create was not dispatched")
	}
	params := call.params.(map[string]any)
	aliases, aOK := params["aliases"].([]string)
	if !aOK || len(aliases) != 1 || aliases[0] != "shop.example.net" {
		t.Fatalf("aliases param = %#v, want [shop.example.net]", params["aliases"])
	}

	// Unchanged alias set on the next periodic tick — no re-dispatch.
	r.createDomainOnAgent(context.Background(), dom, false)
	if got := countDomainCreate(ag); got != 1 {
		t.Fatalf("unchanged alias set must not re-dispatch, got %d", got)
	}

	// Adding an alias changes the assembled params → the hash flips → re-dispatch.
	ar.byDomain[dom.ID] = []string{"shop.example.net", "blog.example.net"}
	r.createDomainOnAgent(context.Background(), dom, false)
	if got := countDomainCreate(ag); got != 2 {
		t.Fatalf("changed alias set must re-dispatch, got %d", got)
	}
}
