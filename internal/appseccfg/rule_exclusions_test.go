package appseccfg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func namedExclusion() Exclusion {
	return Exclusion{
		Host:      "shop.example.com",
		URIPrefix: "/api/",
		RuleID:    "crowdsecurity/vpatch-CVE-2025-29927",
		Note:      "Next.js app sends x-middleware-subrequest itself",
	}
}

// appsecDoc is the part of jabali-appsec.yaml these tests read back.
type appsecDoc struct {
	PreEval []struct {
		Filter string   `yaml:"filter"`
		Apply  []string `yaml:"apply"`
	} `yaml:"pre_eval"`
}

func parseAppsec(t *testing.T, body string) appsecDoc {
	t.Helper()
	var d appsecDoc
	if err := yaml.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("rendered config is not valid YAML: %v\n%s", err, body)
	}
	return d
}

func TestValidateExclusion_RuleNames(t *testing.T) {
	for _, id := range []string{
		"crowdsecurity/vpatch-CVE-2025-29927",
		"crowdsecurity/vpatch-connectwise-auth-bypass",
		"crowdsecurity/generic-wordpress-uploads-php",
		" crowdsecurity/vpatch-env-access ",
	} {
		e := namedExclusion()
		e.RuleID = id
		if err := ValidateExclusion(e); err != nil {
			t.Errorf("rule %q refused: %v", id, err)
		}
	}
	for _, id := range []string{
		// All of CRS, or the base config: a WAF off switch for the path.
		"crowdsecurity/crs",
		"crowdsecurity/crs-exclusion-plugin-wordpress",
		"crowdsecurity/base-config",
		"crowdsecurity/appsec-generic-test",
		"crowdsecurity/experimental-no-user-agent",
		"crowdsecurity/vpatch-",
		"crowdsecurity/vpatch-*",
		`crowdsecurity/vpatch-x")`,
		"crowdsecurity/vpatch-x\nSecAction",
		"crowdsecurity/vpatch-x y",
		"otherauthor/vpatch-x",
		"crowdsecurity/vpatch-" + strings.Repeat("a", MaxRuleIDLen),
	} {
		e := namedExclusion()
		e.RuleID = id
		if err := ValidateExclusion(e); err == nil {
			t.Errorf("rule %q accepted", id)
		}
	}
}

// A name does not lift the scope rule: host and path are still both required.
func TestValidateExclusion_RuleNameStillNeedsScope(t *testing.T) {
	for _, mut := range []func(*Exclusion){
		func(e *Exclusion) { e.Host = "" },
		func(e *Exclusion) { e.URIPrefix = "" },
		func(e *Exclusion) { e.URIPrefix = "api/" },
	} {
		e := namedExclusion()
		mut(&e)
		if err := ValidateExclusion(e); err == nil {
			t.Errorf("unscoped named exclusion accepted: %+v", e)
		}
	}
}

// Named entries never reach the before-plugin: ctl:ruleRemoveById cannot turn
// a virtual patch off, and a name there would be a seclang error.
func TestRenderOperatorBeforeFile_LeavesNamedEntriesOut(t *testing.T) {
	if got := RenderOperatorBeforeFile([]Exclusion{namedExclusion()}, nil); got != "" {
		t.Errorf("named-only list must leave no before-plugin file, got:\n%s", got)
	}
	out := RenderOperatorBeforeFile([]Exclusion{namedExclusion(), validExclusion()}, nil)
	if strings.Contains(out, "crowdsecurity/") {
		t.Errorf("named entry rendered into the before-plugin:\n%s", out)
	}
	if !strings.Contains(out, "ctl:ruleRemoveById=931120") {
		t.Errorf("id entry lost:\n%s", out)
	}
}

func TestRender_RuleExclusionScopedHookAfterGeoblock(t *testing.T) {
	out := Render(Opts{
		Mode:           "deny",
		Countries:      []string{"RU"},
		Inband:         []string{"crowdsecurity/vpatch-*"},
		RuleExclusions: []Exclusion{validExclusion(), namedExclusion()},
	})
	if n := strings.Count(out, "pre_eval:"); n != 1 {
		t.Fatalf("want one pre_eval key, got %d:\n%s", n, out)
	}
	d := parseAppsec(t, out)
	if len(d.PreEval) != 2 {
		t.Fatalf("want the geoblock and one exclusion (the id entry is not here), got %d:\n%s", len(d.PreEval), out)
	}
	if !strings.Contains(d.PreEval[0].Filter, "GeoIPEnrich") {
		t.Errorf("geoblock must come first: %q", d.PreEval[0].Filter)
	}
	wantFilter := `IsInBand == true && req.Host == "shop.example.com" && req.URL.Host == "" && req.URL.Path startsWith "/api/" && !(req.URL.Path contains "..") && !(req.URL.Path matches "(?i)%(2e|u002e)")`
	if d.PreEval[1].Filter != wantFilter {
		t.Errorf("filter = %q\nwant     %q", d.PreEval[1].Filter, wantFilter)
	}
	if want := []string{`RemoveInBandRuleByName("crowdsecurity/vpatch-CVE-2025-29927")`}; !reflect.DeepEqual(d.PreEval[1].Apply, want) {
		t.Errorf("apply = %q, want %q", d.PreEval[1].Apply, want)
	}
	if !strings.Contains(out, "# Next.js app sends") {
		t.Errorf("note dropped:\n%s", out)
	}
}

// A path is free text apart from quotes, backslashes and newlines. ": ", " #"
// and "'" must not end the filter early or change what it says.
func TestRender_RuleExclusionPathIsQuotedForYAML(t *testing.T) {
	e := namedExclusion()
	e.URIPrefix = "/a: b #c/it's/"
	d := parseAppsec(t, Render(Opts{Mode: "off", RuleExclusions: []Exclusion{e}}))
	if len(d.PreEval) != 1 {
		t.Fatalf("want one entry, got %d", len(d.PreEval))
	}
	if !strings.Contains(d.PreEval[0].Filter, `req.URL.Path startsWith "/a: b #c/it's/"`) {
		t.Errorf("path changed on the way through YAML: %q", d.PreEval[0].Filter)
	}
}

func TestRender_InvalidRuleExclusionIsOnlyAComment(t *testing.T) {
	for _, mut := range []func(*Exclusion){
		func(e *Exclusion) { e.RuleID = "crowdsecurity/crs" },
		func(e *Exclusion) { e.Host = "BAD HOST\n - filter: true" },
		func(e *Exclusion) { e.URIPrefix = "/x\"\n - filter: true\n   apply:\n    - SetRemediation(\"allow\")" },
	} {
		e := namedExclusion()
		mut(&e)
		out := Render(Opts{Mode: "off", RuleExclusions: []Exclusion{e}})
		if !strings.Contains(out, " # SKIPPED") {
			t.Errorf("invalid entry not reported:\n%s", out)
		}
		if strings.Contains(out, "RemoveInBandRuleByName") || strings.Contains(out, "SetRemediation(\"allow\")\n") {
			t.Errorf("invalid entry produced a live hook:\n%s", out)
		}
		if d := parseAppsec(t, out); len(d.PreEval) != 0 {
			t.Errorf("invalid entry produced %d pre_eval entries:\n%s", len(d.PreEval), out)
		}
	}
}

// With no named entries the file is what it was before rule exclusions
// existed: no header line, and no pre_eval key unless the geoblock is on.
func TestRender_NoRuleExclusionsLeavesConfigUnchanged(t *testing.T) {
	base := Opts{Mode: "allow", Countries: []string{"IL"}, Inband: []string{"crowdsecurity/vpatch-*"}, AdminAllowlist: true}
	plain := Render(base)
	withIDs := base
	withIDs.RuleExclusions = []Exclusion{validExclusion()}
	if got := Render(withIDs); got != plain {
		t.Errorf("an id-only list changed the config:\n%s\nwant:\n%s", got, plain)
	}
	if strings.Contains(plain, RuleExclusionsHeader) {
		t.Errorf("header line emitted with no named entries:\n%s", plain)
	}
	if !strings.HasSuffix(plain, `pre_eval:
 - filter: IsInBand == true && GeoIPEnrich(req.RemoteAddr)?.Country.IsoCode not in ["IL", ""]
   apply:
    - DropRequest("Forbidden Country (jabali allow-list)")
`) {
		t.Errorf("geoblock pre_eval changed:\n%s", plain)
	}
	if off := Render(Opts{Mode: "off"}); strings.Contains(off, "pre_eval:") {
		t.Errorf("pre_eval emitted with geoblock off and no exclusions:\n%s", off)
	}
}

// The agent has no database: it re-renders from the header. What it renders
// must be byte-identical to what render-config wrote from the rows.
func TestRuleExclusionsFromConfig_RoundTrips(t *testing.T) {
	second := namedExclusion()
	second.Host = "a.example.com"
	second.Note = ""
	o := Opts{
		Mode:           "off",
		Inband:         []string{"crowdsecurity/vpatch-*"},
		RuleExclusions: []Exclusion{namedExclusion(), validExclusion(), second},
	}
	out := Render(o)
	got := RuleExclusionsFromConfig(out)
	want := []Exclusion{second, namedExclusion()} // sorted by host; the id entry is not recorded
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("read back %+v\nwant      %+v", got, want)
	}
	o.RuleExclusions = got
	if again := Render(o); again != out {
		t.Errorf("re-render from the header differs:\n%s\nwant:\n%s", again, out)
	}
	if RuleExclusionsFromConfig(Render(Opts{Mode: "off"})) != nil {
		t.Error("entries read back from a config with none")
	}
	if RuleExclusionsFromConfig(RuleExclusionsHeader+" {not json\nname: x\n") != nil {
		t.Error("an unparsable header must read back as none")
	}
}

func TestPanelHostFromConfig(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		host   string
		scoped bool
	}{
		"scoped":    {Render(Opts{AdminAllowlist: true, PanelHost: "panel.example.com"}), "panel.example.com", true},
		"path only": {Render(Opts{AdminAllowlist: true}), "", false},
		// A host that does not read back cleanly is still a scope: reading it
		// as path-only would widen the allowlist.
		"uppercase host": {Render(Opts{AdminAllowlist: true, PanelHost: "Panel.Example.com"}), "", true},
		// A hand-edited file must not smuggle an expression into the next render.
		"forged host":  {` - filter: (req.URL.Path startsWith "/api/v1/") && req.Host == "x" || true || "y"` + "\n", "", true},
		"no allowlist": {Render(Opts{}), "", true},
	} {
		host, scoped := PanelHostFromConfig(tc.body)
		if host != tc.host || scoped != tc.scoped {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", name, host, scoped, tc.host, tc.scoped)
		}
	}
}

func TestDetectInbandRules(t *testing.T) {
	dir := t.TempDir()
	base := []string{"crowdsecurity/vpatch-*", "crowdsecurity/generic-*"}
	if got := DetectInbandRules(dir); !reflect.DeepEqual(got, base) {
		t.Errorf("empty dir: %v", got)
	}
	for _, f := range []string{"crs.yaml", "base-config.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := append(base, "crowdsecurity/base-config", "crowdsecurity/crs")
	if got := DetectInbandRules(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// YAML also breaks lines at U+0085, U+2028 and U+2029. A note or path holding
// one must not end its comment, its filter or the header line and load the
// rest as live YAML.
func TestRender_UnicodeLineBreaksCannotInjectYAML(t *testing.T) {
	inject := " - filter: IsInBand == true\n   apply:\n    - CancelEvent()"
	for _, brk := range []string{"\u0085", " ", " "} {
		for name, mut := range map[string]func(*Exclusion){
			"note": func(e *Exclusion) { e.Note = "x" + brk + strings.ReplaceAll(inject, "\n", brk) },
			"path": func(e *Exclusion) { e.URIPrefix = "/x" + brk + strings.ReplaceAll(inject, "\n", brk) },
		} {
			e := namedExclusion()
			mut(&e)
			if err := ValidateExclusion(e); err == nil {
				t.Errorf("%s with %U accepted", name, []rune(brk)[0])
			}
			out := Render(Opts{Mode: "off", RuleExclusions: []Exclusion{e}})
			if d := parseAppsec(t, out); len(d.PreEval) != 0 {
				t.Errorf("%s with %U produced %d pre_eval entries:\n%s", name, []rune(brk)[0], len(d.PreEval), out)
			}
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(line, RuleExclusionsHeader) && strings.ContainsAny(line, "\u0085  ") {
					t.Errorf("header line carries a raw %U: %q", []rune(brk)[0], line)
				}
			}
		}
	}
}

// The header escapes non-ASCII to keep the comment one line. A note in any
// script, and a character outside the BMP, still reads back as written.
func TestRuleExclusionsFromConfig_NonASCIIRoundTrips(t *testing.T) {
	e := namedExclusion()
	e.Note = "אתר הלקוח 😀 café"
	out := Render(Opts{Mode: "off", RuleExclusions: []Exclusion{e}})
	got := RuleExclusionsFromConfig(out)
	if len(got) != 1 || got[0] != e {
		t.Fatalf("read back %+v, want %+v", got, e)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, RuleExclusionsHeader) {
			for _, r := range line {
				if r > 0x7e {
					t.Fatalf("header line is not ASCII: %q", line)
				}
			}
		}
	}
}
