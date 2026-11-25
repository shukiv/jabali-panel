package appseccfg

import (
	"encoding/json"
	"strings"
	"testing"
)

func gatedExclusion() Exclusion {
	return Exclusion{
		Host: "www.example.co.il", URIPrefix: "/wp-admin/admin-ajax.php", RuleID: "941100",
		Note: "ACF options save", WPLoginOnly: true,
	}
}

// A gated row renders as host → path → traversal guard → POST → login cookie,
// with the rule drop on the last link only, so every link must match.
func TestRenderExclusions_LoginGateChain(t *testing.T) {
	out := RenderExclusions([]Exclusion{gatedExclusion()})
	var chain []string
	for _, c := range seclangChains(out) {
		if strings.Contains(strings.Join(c, "\n"), "ctl:ruleRemoveById=941100") {
			chain = c
		}
	}
	want := []string{
		`SecRule REQUEST_HEADERS:Host "@streq www.example.co.il" "id:9597000,phase:1,pass,nolog,chain"`,
		`SecRule REQUEST_URI "@beginsWith /wp-admin/admin-ajax.php" "t:none,chain"`,
		TraversalGuard + `,chain"`,
		`SecRule REQUEST_METHOD "@streq POST" "t:none,chain"`,
		`SecRule REQUEST_COOKIES_NAMES "@rx ^wordpress_logged_in_" "t:none,ctl:ruleRemoveById=941100"`,
	}
	if strings.Join(chain, "\n") != strings.Join(want, "\n") {
		t.Fatalf("gated chain:\n%s\nwant:\n%s", strings.Join(chain, "\n"), strings.Join(want, "\n"))
	}

	// An ungated row keeps its old shape: the drop sits on the guard.
	plain := gatedExclusion()
	plain.WPLoginOnly = false
	if o := RenderExclusions([]Exclusion{plain}); strings.Contains(o, "REQUEST_COOKIES_NAMES") ||
		!strings.Contains(o, TraversalGuard+`,ctl:ruleRemoveById=941100"`) {
		t.Fatalf("ungated row changed shape:\n%s", o)
	}
}

// The gate is a seclang chain; a rule removed by name renders as YAML with no
// request condition, so a gated name is refused and never rendered ungated.
func TestLoginGate_RuleNameRefused(t *testing.T) {
	e := gatedExclusion()
	e.RuleID = "crowdsecurity/vpatch-CVE-2026-0001"
	if err := ValidateExclusion(e); err == nil {
		t.Fatal("a login-gated AppSec rule name was accepted")
	}
	if got := RuleNameExclusions([]Exclusion{e}); len(got) != 0 {
		t.Fatalf("gated name reached the YAML renderer: %+v", got)
	}
	if out := RenderRuleExclusions([]Exclusion{e}); out != "" {
		t.Fatalf("gated name rendered into the AppSec YAML:\n%s", out)
	}
	out := RenderExclusions([]Exclusion{e})
	if !strings.Contains(out, "# SKIPPED") {
		t.Fatalf("gated name must be reported as SKIPPED:\n%s", out)
	}
	assertOnlyComments(t, out)
}

// Gated rows ride their own key. An agent built before the gate decodes the
// params into a struct without it, so it sees only the ungated rows and leaves
// the gated rule ON, rather than dropping it for every request on the path.
func TestOperatorApplyParams_GatedRowsInvisibleToOldAgent(t *testing.T) {
	plain := validExclusion()
	raw, err := json.Marshal(NewOperatorApplyParams([]Exclusion{plain, gatedExclusion()}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "wp_login_only") {
		t.Fatalf("the gate flag must not be on the wire (an old agent would drop it): %s", raw)
	}

	var old struct {
		Exclusions []Exclusion `json:"exclusions"`
		HostModes  []HostMode  `json:"host_modes"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if len(old.Exclusions) != 1 || old.Exclusions[0].RuleID != plain.RuleID {
		t.Fatalf("old agent sees %+v, want only the ungated row", old.Exclusions)
	}

	var p OperatorApplyParams
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
	all := p.AllExclusions()
	if len(all) != 2 || all[0].WPLoginOnly || !all[1].WPLoginOnly || all[1].RuleID != "941100" {
		t.Fatalf("new agent decoded %+v", all)
	}
	if !strings.Contains(RenderOperatorBeforeFile(all, nil), "REQUEST_COOKIES_NAMES") {
		t.Fatal("new agent rendered the gated row without its gate")
	}
}

func TestNormalizeExclusion_KeepsLoginGate(t *testing.T) {
	if !NormalizeExclusion(gatedExclusion()).WPLoginOnly {
		t.Fatal("normalizing dropped the login gate: the row would be stored ungated")
	}
}
