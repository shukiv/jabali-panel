package reconciler

import (
	"context"
	"slices"
	"testing"
)

// A domain's vhost refuses AI crawlers only when it has the switch on, and
// the admin's list only when the list has an entry.
func TestCreateDomainOnAgent_SendsBotBlockFlags(t *testing.T) {
	for _, c := range []struct {
		name          string
		blockAI       bool
		blockedList   string
		wantAI, wantL bool
	}{
		{name: "nothing blocked"},
		{name: "AI crawlers", blockAI: true, wantAI: true},
		{name: "admin list", blockedList: "python-requests\nzgrab", wantL: true},
		{name: "only invalid entries", blockedList: "x;y\nab"},
	} {
		r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
		dom.BlockAICrawlers = c.blockAI
		s, _ := r.serverSettings.Get(context.Background())
		s.BlockedUserAgents = c.blockedList
		r.createDomainOnAgent(context.Background(), dom, true)
		call, ok := findAgentCall(ag, "domain.create")
		if !ok {
			t.Fatalf("%s: domain.create was not dispatched", c.name)
		}
		p := call.params.(map[string]any)
		if p["block_ai_crawlers"] != c.wantAI || p["block_listed_user_agents"] != c.wantL {
			t.Errorf("%s: block_ai_crawlers=%v block_listed_user_agents=%v, want %v/%v",
				c.name, p["block_ai_crawlers"], p["block_listed_user_agents"], c.wantAI, c.wantL)
		}
	}
}

// The fragment gets the admin's valid entries; an unchanged list is not sent
// again within the audit interval.
func TestReconcileNginxBots_SendsListOnce(t *testing.T) {
	r, ag, _, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	s, _ := r.serverSettings.Get(context.Background())
	s.BlockedUserAgents = "python-requests\nbad;entry\nZgrab\nzgrab"

	ctx, rr := ensureRun(context.Background(), RunNormal)
	r.ReconcileNginxBots(ctx)
	rr.finish()
	call, ok := findAgentCall(ag, "nginx.bots.apply")
	if !ok {
		t.Fatal("nginx.bots.apply was not called")
	}
	got, _ := call.params.(map[string]any)["blocked_user_agents"].([]string)
	if !slices.Equal(got, []string{"python-requests", "Zgrab"}) {
		t.Fatalf("sent %v, want [python-requests Zgrab]", got)
	}

	before := len(ag.calls)
	ctx, rr = ensureRun(context.Background(), RunNormal)
	r.ReconcileNginxBots(ctx)
	rr.finish()
	if len(ag.calls) != before {
		t.Fatal("an unchanged list was sent again")
	}

	s.BlockedUserAgents = ""
	ctx, rr = ensureRun(context.Background(), RunNormal)
	r.ReconcileNginxBots(ctx)
	rr.finish()
	if len(ag.calls) == before {
		t.Fatal("emptying the list did not reach the agent")
	}
	last := ag.calls[len(ag.calls)-1]
	if l, _ := last.params.(map[string]any)["blocked_user_agents"].([]string); last.method != "nginx.bots.apply" || len(l) != 0 {
		t.Fatalf("last call %s %v, want nginx.bots.apply with an empty list", last.method, last.params)
	}
}
