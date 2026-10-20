package reconciler

import (
	"context"
	"slices"
	"testing"
)

// The agent gets the admin's lists that are in the catalog and this host's
// public IPs; an unchanged set is not sent again within the interval, and
// turning every list off is sent.
func TestReconcileCrowdsecLists_SendsTheSet(t *testing.T) {
	r, ag, _, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	s, _ := r.serverSettings.Get(context.Background())
	s.SecurityEnabled = true
	s.PublicIPv4 = "185.10.20.30"
	s.CrowdsecBlocklists = "firehol_level3, gone_list,firehol_level1"

	run := func() {
		ctx, rr := ensureRun(context.Background(), RunNormal)
		r.reconcileCrowdsecLists(ctx)
		rr.finish()
	}
	run()
	call, ok := findAgentCall(ag, "security.crowdsec.lists.apply")
	if !ok {
		t.Fatal("security.crowdsec.lists.apply was not called")
	}
	p := call.params.(map[string]any)
	if got := p["enabled"].([]string); !slices.Equal(got, []string{"firehol_level1", "firehol_level3"}) {
		t.Fatalf("enabled = %v, want [firehol_level1 firehol_level3]", got)
	}
	if got := p["protect"].([]string); !slices.Equal(got, []string{"185.10.20.30"}) {
		t.Fatalf("protect = %v, want the public IP", got)
	}

	before := len(ag.calls)
	run()
	if len(ag.calls) != before {
		t.Fatal("an unchanged set was sent again")
	}

	s.CrowdsecBlocklists = ""
	run()
	last := ag.calls[len(ag.calls)-1]
	if got, _ := last.params.(map[string]any)["enabled"].([]string); last.method != "security.crowdsec.lists.apply" || got == nil || len(got) != 0 {
		t.Fatalf("last call %s %v, want lists.apply with an empty, non-nil set", last.method, last.params)
	}
}

// With the security module off, CrowdSec is not the panel's to manage.
func TestReconcileCrowdsecLists_SecurityOffSendsNothing(t *testing.T) {
	r, ag, _, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	s, _ := r.serverSettings.Get(context.Background())
	s.SecurityEnabled = false
	s.CrowdsecBlocklists = "firehol_level1"
	r.reconcileCrowdsecLists(context.Background())
	if _, ok := findAgentCall(ag, "security.crowdsec.lists.apply"); ok {
		t.Fatal("lists were sent with the security module off")
	}
}
