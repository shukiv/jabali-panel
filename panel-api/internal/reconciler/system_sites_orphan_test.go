package reconciler

import (
	"context"
	"testing"
)

// The Stalwart WebAdmin proxy (`jabali-stalwart-admin`) is a system site:
// while WebAdmin is on it has no domain row, and it is not an orphan (GH
// #2237), nor a zone whose recursor forwarder to drop.
func TestSweepAgentSites_StalwartWebAdminVhostIsNotAnOrphan(t *testing.T) {
	r, _, _, _, ag := mtaStsReconcilerForTest(t)
	sites := map[string]bool{"jabali-stalwart-admin": true, "stray.org": true}

	r.sweepAgentSites(context.Background(), sites, nil, nil, nil)

	if r.lastOrphanKey != "stray.org" {
		t.Fatalf("orphans reported %q, want only stray.org", r.lastOrphanKey)
	}
	for _, c := range ag.calls {
		if z, _ := c.Params["zone"].(string); z == "jabali-stalwart-admin" {
			t.Errorf("%s for the WebAdmin vhost", c.Command)
		}
	}
}
