package commands

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

const cscliRulesList = "cscli appsec-rules list -o json"

func vpatchRow() appseccfg.Exclusion {
	return appseccfg.Exclusion{
		Host: "shop.example.com", URIPrefix: "/api/",
		RuleID: "crowdsecurity/vpatch-CVE-2025-29927", Note: "Next.js",
	}
}

// liveAppsec writes the jabali-appsec.yaml that render-config writes for o, on
// a host whose rules dir holds crs.yaml, and returns o with that in-band list.
func liveAppsec(t *testing.T, o appseccfg.Opts) appseccfg.Opts {
	t.Helper()
	if err := os.MkdirAll(appsecRulesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsecRulesDir, "crs.yaml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	o.Inband = appseccfg.DetectInbandRules(appsecRulesDir)
	o.AdminAllowlist = true
	if err := os.WriteFile(appsecRulePath, []byte(appseccfg.Render(o)), 0o644); err != nil {
		t.Fatal(err)
	}
	return o
}

// A named exclusion goes into jabali-appsec.yaml, and the file is then exactly
// what render-config writes for the same rows: the geoblock, bot detection and
// the panel allowlist stay as they were. The id row still goes to the
// before-plugin. The new config is checked before the reload.
func TestOperatorApply_RuleNameRendersIntoAppsecConfig(t *testing.T) {
	before, calls := operatorApplyEnvExec(t, nil, nil)
	live := liveAppsec(t, appseccfg.Opts{Mode: "allow", Countries: []string{"IL"}, BotDetection: "off", BotScope: "all"})
	wantLive := readString(t, appsecRulePath)

	rows := []appseccfg.Exclusion{flarumRow(), vpatchRow()}
	res, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams(rows, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !res.Reloaded {
		t.Fatalf("want changed+reloaded, got %+v", res)
	}
	want := live
	want.RuleExclusions = rows
	if got := readString(t, appsecRulePath); got != appseccfg.Render(want) {
		t.Fatalf("AppSec config differs from render-config's for the same rows:\n%s\nwant:\n%s", got, appseccfg.Render(want))
	}
	if got := readString(t, before); strings.Contains(got, "crowdsecurity/") || !strings.Contains(got, "ruleRemoveById=920450") {
		t.Fatalf("before-plugin must hold the id row only:\n%s", got)
	}
	if strings.Join(*calls, ";") != cscliRulesList+";crowdsec -t;systemctl reload crowdsec" {
		t.Fatalf("want the rule check, crowdsec -t, then the reload; got %v", *calls)
	}

	// Same rows: no write, no check, no reload.
	*calls = nil
	if res, err = callOperatorApply(t, appseccfg.NewOperatorApplyParams(rows, nil)); err != nil || res.Changed {
		t.Fatalf("unchanged apply: %+v err=%v", res, err)
	}
	if strings.Join(*calls, ";") != cscliRulesList {
		t.Fatalf("unchanged apply ran %v", *calls)
	}

	// Removing it puts the file back as it was.
	if _, err = callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{flarumRow()}, nil)); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, appsecRulePath); got != wantLive {
		t.Fatalf("removal left:\n%s\nwant:\n%s", got, wantLive)
	}
}

// Operator apply keeps the panel allowlist's host scope as the live file has
// it.
func TestOperatorApply_KeepsLivePanelHostScope(t *testing.T) {
	operatorApplyEnvExec(t, nil, nil)
	live := liveAppsec(t, appseccfg.Opts{Mode: "off", PanelHost: "panel.example.com"})
	if _, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{vpatchRow()}, nil)); err != nil {
		t.Fatal(err)
	}
	want := live
	want.RuleExclusions = []appseccfg.Exclusion{vpatchRow()}
	if got := readString(t, appsecRulePath); got != appseccfg.Render(want) {
		t.Fatalf("panel host scope not kept:\n%s", got)
	}
}

// A live allowlist scoped to a host that does not read back (here: upper
// case) stays scoped. Read as path-only, it would let every tenant vhost skip
// AppSec on the panel paths.
func TestOperatorApply_UnreadablePanelHostStaysScoped(t *testing.T) {
	operatorApplyEnvExec(t, nil, nil)
	liveAppsec(t, appseccfg.Opts{Mode: "off", PanelHost: "Panel.Example.com"})
	if _, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{vpatchRow()}, nil)); err != nil {
		t.Fatal(err)
	}
	got := readString(t, appsecRulePath)
	if !strings.Contains(got, `/dav/") && req.Host == "`) {
		t.Fatalf("panel allowlist lost its host scope:\n%s", got)
	}
}

// If crowdsec -t refuses the new config, both files go back to what they were
// and nothing is reloaded: the running crowdsec keeps its config.
func TestOperatorApply_RefusedConfigRollsBack(t *testing.T) {
	before, calls := operatorApplyEnvExec(t, map[string]bool{"crowdsec -t": true}, nil)
	liveAppsec(t, appseccfg.Opts{Mode: "off"})
	wantLive := readString(t, appsecRulePath)

	_, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{flarumRow(), vpatchRow()}, nil))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInternal {
		t.Fatalf("want Internal, got %v", err)
	}
	if got := readString(t, appsecRulePath); got != wantLive {
		t.Fatalf("AppSec config not rolled back:\n%s", got)
	}
	if _, err := os.Stat(before); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("before-plugin not rolled back to absent (err=%v)", err)
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "systemctl") {
			t.Fatalf("reloaded a refused config: %v", *calls)
		}
	}
}

// Without jabali-appsec.yaml AppSec is not set up: a named row must not create
// the file.
func TestOperatorApply_NoAppsecConfigNotCreated(t *testing.T) {
	operatorApplyEnvExec(t, nil, nil)
	res, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{vpatchRow()}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("want no change, got %+v", res)
	}
	if _, err := os.Stat(appsecRulePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created %s (err=%v)", appsecRulePath, err)
	}
}

// CrowdSec drops a remove of a rule that is not loaded without a word, so the
// verb names the rules no installed rule matches.
func TestOperatorApply_ReportsUnknownRules(t *testing.T) {
	operatorApplyEnvExec(t, nil, map[string]string{
		cscliRulesList: `{"appsec-rules":[{"name":"crowdsecurity/vpatch-CVE-2025-29927"},{"name":"crowdsecurity/crs"}]}`,
	})
	liveAppsec(t, appseccfg.Opts{Mode: "off"})
	typo := vpatchRow()
	typo.RuleID = "crowdsecurity/vpatch-CVE-2025-2992"
	other := typo
	other.URIPrefix = "/other/"
	res, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{vpatchRow(), typo, other}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"crowdsecurity/vpatch-CVE-2025-2992"}; !reflect.DeepEqual(res.UnknownRules, want) {
		t.Fatalf("unknown rules = %v, want %v", res.UnknownRules, want)
	}
}

// A geoblock change after an apply keeps the named exclusions: they ride in
// the file's header.
func TestGeoblockRerenderKeepsRuleExclusions(t *testing.T) {
	operatorApplyEnvExec(t, nil, nil)
	liveAppsec(t, appseccfg.Opts{Mode: "off"})
	if _, err := callOperatorApply(t, appseccfg.NewOperatorApplyParams([]appseccfg.Exclusion{vpatchRow()}, nil)); err != nil {
		t.Fatal(err)
	}
	out := renderAppSecGeoblockRule("deny", []string{"RU"})
	if !strings.Contains(out, `RemoveInBandRuleByName("crowdsecurity/vpatch-CVE-2025-29927")`) {
		t.Fatalf("geoblock re-render dropped the rule exclusion:\n%s", out)
	}
	if !strings.Contains(out, `DropRequest("Forbidden Country (jabali deny-list)")`) {
		t.Fatalf("geoblock missing:\n%s", out)
	}
}
