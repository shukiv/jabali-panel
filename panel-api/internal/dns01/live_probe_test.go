//go:build live

package dns01

import (
	"context"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
)

// Live probe (not run in CI): exercises the real LookupNSExternal walk
// against public DNS for domains from the JAB-235 ticket.
func TestLiveAuthoritativeRoute(t *testing.T) {
	cfg := Config{LookupNS: dnsverify.LookupNSExternal}
	for _, d := range []string{"movi.co.il", "premiumhaifa.co.il", "pinat.pcc.co.il", "google.com"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		r := AuthoritativeRoute(ctx, cfg, d)
		cancel()
		t.Logf("%s → provider=%s zone=%s reason=%q", d, r.Provider, r.Zone, r.Reason)
	}
}
