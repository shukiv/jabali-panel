package reconciler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A domain's MTA-STS vhost (`<domain>-mta-sts`) is not an orphan site while
// the domain has a row. Once the domain is gone with no teardown pending, the
// vhost is removed: it names the deleted domain's certificate, so it breaks
// `nginx -t` for the whole server.
func TestSweepAgentSites_MTAStsVhosts(t *testing.T) {
	r, _, _, _, ag := mtaStsReconcilerForTest(t)
	sites := map[string]bool{
		"default": true, "live.org": true, "live.org-mail": true, "live.org-mta-sts": true,
		"off.org-mta-sts": true, "gone.org-mta-sts": true, "tomb.org-mta-sts": true, "stray.org": true,
	}
	enabled := map[string]*models.Domain{"live.org": {ID: "d1", Name: "live.org"}}
	disabled := map[string]*models.Domain{"off.org": {ID: "d2", Name: "off.org"}}
	pending := map[string]bool{"tomb.org": true}

	r.sweepAgentSites(context.Background(), sites, enabled, disabled, pending)

	var disabledFor []string
	for _, c := range ag.calls {
		switch c.Command {
		case "mail.mtasts.disable":
			disabledFor = append(disabledFor, c.Params["domain"].(string))
		case "pdns.recursor_remove_zone":
			if z, _ := c.Params["zone"].(string); strings.HasSuffix(z, "-mta-sts") {
				t.Errorf("recursor forwarder removal for the MTA-STS vhost %q", z)
			}
		}
	}
	if strings.Join(disabledFor, ",") != "gone.org" {
		t.Fatalf("mail.mtasts.disable for %v, want only gone.org", disabledFor)
	}
	if r.lastOrphanKey != "stray.org" {
		t.Fatalf("orphans reported %q, want only stray.org", r.lastOrphanKey)
	}
}

// A failed removal is retried on the next pass, and the vhost still isn't
// reported as an orphan.
func TestSweepAgentSites_MTAStsRemovalFailureRetries(t *testing.T) {
	r, _, _, _, ag := mtaStsReconcilerForTest(t)
	ag.failCmd = map[string]error{"mail.mtasts.disable": errors.New("nginx -t failed")}
	sites := map[string]bool{"gone.org-mta-sts": true}
	for i := 0; i < 2; i++ {
		r.sweepAgentSites(context.Background(), sites, nil, nil, nil)
	}
	n := 0
	for _, c := range ag.calls {
		if c.Command == "mail.mtasts.disable" {
			n++
		}
	}
	if n != 2 || r.lastOrphanKey != "" {
		t.Fatalf("removal attempts %d, orphans %q; want 2 attempts and no orphan", n, r.lastOrphanKey)
	}
}
