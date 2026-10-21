// security.appsec.operator.apply — GH #1650. Writes the operator-managed CRS
// before-plugin file (appseccfg.CRSPluginOperatorBeforePath) from the full set
// of rows the panel sends, then reloads crowdsec when the file changed.
//
// panel-api runs as `jabali` (JAB-357) and cannot write /var/lib/crowdsec or
// reload crowdsec. Before this verb, a row the panel added did nothing live
// until root next ran `jabali appsec render-config`.
//
// The body is rendered by appseccfg.RenderOperatorBeforeFile, the same function
// `render-config` uses, so the two writers produce byte-identical files for the
// same rows and never undo each other. That is also why an invalid row renders
// as a SKIPPED comment here instead of failing the call: the CLI does the same.
// Only the shape of the request is refused (a missing list, an oversized list).
//
// This verb never touches the built-in file (CRSPluginBeforePath); the agent's
// boot writer owns that one (security_appsec_before.go).
//
// Exclusions that name an AppSec rule (crowdsecurity/vpatch-…) cannot live in
// the before-plugin: CrowdSec turns those off in a pre_eval hook of
// jabali-appsec.yaml. The verb re-renders that file with them, keeping every
// other setting as the live file has it, and checks it with `crowdsec -t`
// before the reload: a config crowdsec cannot load would take AppSec down.
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// Overridable in tests.
var (
	appsecOperatorBeforePath = appseccfg.CRSPluginOperatorBeforePath
	appsecCrowdsecDataDir    = "/var/lib/crowdsec/data"
)

func appsecOperatorApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p appseccfg.OperatorApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := p.Check(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	// Gate on the CRS data tree, like the boot writer: absent on hosts without
	// crowdsec, and creating it here would plant a config for nothing to load.
	if _, err := os.Stat(appsecCrowdsecDataDir); err != nil {
		return appseccfg.OperatorApplyResult{Skipped: "crowdsec is not installed"}, nil
	}

	prevBefore, _ := readFileOK(appsecOperatorBeforePath)
	beforeChanged, err := writeOperatorBeforeFile(appsecOperatorBeforePath,
		appseccfg.RenderOperatorBeforeFile(p.Exclusions, p.HostModes))
	if err != nil {
		return nil, csInternal("write operator before-plugin", err)
	}
	appsecChanged, prevAppsec, err := writeAppsecRuleExclusions(appseccfg.RuleNameExclusions(p.Exclusions))
	if err != nil {
		return nil, csInternal("write AppSec config", err)
	}

	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res := appseccfg.OperatorApplyResult{Changed: beforeChanged || appsecChanged}
	if named := appseccfg.RuleNameExclusions(p.Exclusions); len(named) > 0 {
		res.UnknownRules = unknownAppsecRules(rctx, named)
	}
	if !res.Changed {
		return res, nil
	}
	if appsecChanged {
		if verr := validateCrowdsecConfig(rctx); verr != nil {
			// Put both files back, so the next reload (of anything) cannot
			// load this one. The running crowdsec still has the old config.
			_ = writeAppsecFile(appsecRulePath, string(prevAppsec))
			_, _ = writeOperatorBeforeFile(appsecOperatorBeforePath, string(prevBefore))
			return nil, csInternal("crowdsec -t refused the AppSec config, nothing applied", verr)
		}
	}

	if out, err := execCommandContext(rctx, "systemctl", "reload", "crowdsec").CombinedOutput(); err != nil {
		if out2, err2 := execCommandContext(rctx, "systemctl", "restart", "crowdsec").CombinedOutput(); err2 != nil {
			// The file is written, so the next reload loads it. Report the
			// failure so the panel can log it rather than assume it is live.
			return nil, csInternal("crowdsec reload and restart failed",
				fmt.Errorf("reload: %v (%s); restart: %v (%s)",
					err, strings.TrimSpace(string(out)), err2, strings.TrimSpace(string(out2))))
		}
	}
	res.Reloaded = true
	return res, nil
}

// writeAppsecRuleExclusions re-renders jabali-appsec.yaml with list, the
// exclusions that name an AppSec rule. Everything else is kept as the live file
// has it: the geoblock and bot-detection settings from its header, and the
// panel-host scoping of its panel-path allowlist. Write-on-diff. Without the
// file, AppSec is not set up and there is nothing to change. prev is the file
// as it was, for a rollback.
func writeAppsecRuleExclusions(list []appseccfg.Exclusion) (changed bool, prev []byte, err error) {
	prev, ok := readFileOK(appsecRulePath)
	if !ok {
		return false, nil, nil
	}
	panelHost, scoped := appseccfg.PanelHostFromConfig(string(prev))
	if scoped && panelHost == "" {
		// Scoped to a host that does not read back: the agent's own renders
		// scope it to this one.
		panelHost = appsecPanelHost()
	}
	if scoped && panelHost == "" {
		return false, prev, errors.New("the live AppSec config scopes the panel allowlist to a host that cannot be read back; not re-rendering it, which would widen it")
	}
	geoMode, countries := readGeoblockState()
	body := renderJabaliAppsecWith(geoMode, countries, readBotDetectionMode(), readBotScope(), panelHost, list)
	if string(prev) == body {
		return false, prev, nil
	}
	if err := writeFileAtomic(appsecRulePath, []byte(body), 0o644); err != nil {
		return false, prev, err
	}
	return true, prev, nil
}

// unknownAppsecRules returns the rule names in list that no installed AppSec
// rule has, sorted. CrowdSec removes a rule that is not loaded without a word
// (crowdsec -t passes too), so this is how the operator learns of a typo, or of
// a rule the hub has not installed here. nil when cscli cannot answer.
func unknownAppsecRules(ctx context.Context, list []appseccfg.Exclusion) []string {
	raw, err := runCscliJSON(ctx, "appsec-rules", "list")
	if err != nil {
		return nil
	}
	var resp struct {
		Rules []struct {
			Name string `json:"name"`
		} `json:"appsec-rules"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.Rules) == 0 {
		return nil
	}
	installed := make(map[string]bool, len(resp.Rules))
	for _, r := range resp.Rules {
		installed[r.Name] = true
	}
	var unknown []string
	for _, e := range list {
		id := strings.TrimSpace(e.RuleID)
		if installed[id] || appseccfg.ValidateExclusion(e) != nil {
			continue
		}
		installed[id] = true // report each name once
		unknown = append(unknown, id)
	}
	sort.Strings(unknown)
	return unknown
}

// writeOperatorBeforeFile makes path hold body, or removes it when body is ""
// (no exclusions and no host modes, so a since-removed entry cannot linger
// live). Returns whether anything on disk changed.
func writeOperatorBeforeFile(path, body string) (bool, error) {
	if body == "" {
		err := os.Remove(path)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, fs.ErrNotExist):
			return false, nil
		default:
			return false, err
		}
	}
	if existing, err := os.ReadFile(path); err == nil && string(existing) == body {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := writeFileAtomically(path, []byte(body), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func init() {
	Default.Register(appseccfg.OperatorApplyVerb, appsecOperatorApplyHandler)
}
