// security.crowdsec.appsec.refresh — GH #2380. Re-renders jabali-appsec.yaml
// with the panel reconciler's current webmail host list, keeping every other
// setting as the live file has it, and reloads crowdsec on a real change.
//
// The webmail hosts (mail.<domain>, autoconfig.<domain>, autodiscover.<domain>
// and an applied custom mail hostname) are skipped by AppSec: Bulwark sends a
// message's HTML in its JMAP request, and CRS scores that as XSS. The panel
// rewrites the list whenever a domain's mail vhost comes or goes, but only an
// update or a geoblock, bot-detection or exclusion change re-rendered this
// file. Until one of those ran, a new domain's webmail was inspected, and an
// HTML message sent from it banned the sender's IP.
//
// The panel reconciler calls this when the list changes, after a panel
// restart or a failed call, and every 15 minutes to repair a config rendered
// since from another list. Each call compares before it writes, so one that
// finds the file current changes nothing and reloads nothing.
package commands

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// appsecConfigMu serializes the verbs that write the AppSec configs
// (jabali-appsec.yaml, jabali-bot-exempt.yaml): geoblock, bot detection, the
// bot exemptions, the operator exclusions and this refresh. Each reads the
// live file, renders from it, writes, checks, and may put the old file back.
// Two at once could render from a header the other is replacing, or put back
// a file the other just wrote, and lose a geoblock or exclusion change.
// `jabali appsec render-config` runs in another process and is not covered.
var appsecConfigMu sync.Mutex

// csAppSecRefreshHandler is the verb. Without jabali-appsec.yaml, AppSec is
// not set up here and nothing is written.
func csAppSecRefreshHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	appsecConfigMu.Lock()
	defer appsecConfigMu.Unlock()
	changed, prev, err := writeAppsecRuleExclusions(readRuleExclusions())
	if err != nil {
		return nil, csInternal("re-render the AppSec config", err)
	}
	if !changed {
		return map[string]any{"changed": false}, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if verr := validateCrowdsecConfig(rctx); verr != nil {
		// Put the file back, so the next reload (of anything) cannot load
		// this one. The running crowdsec still has the old config.
		_ = writeAppsecFile(appsecRulePath, string(prev))
		return nil, csInternal("crowdsec -t refused the AppSec config, nothing applied", verr)
	}
	if err := reloadCrowdsec(rctx); err != nil {
		return nil, err
	}
	return map[string]any{"changed": true}, nil
}
