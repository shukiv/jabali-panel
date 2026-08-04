package appseccfg

import (
	"strings"
	"testing"
)

func validExclusion() Exclusion {
	return Exclusion{
		Host:      "arizot-e.com",
		URIPrefix: "/wp-json/aramapp-leads/v1/inbound",
		RuleID:    "931120",
		Note:      "WhatsApp webhook: message text contains URLs",
	}
}

// Scope is the whole safety argument. ruleRemoveById drops the rule for the
// entire matched request, so an exclusion missing either half of its scope
// would be a WAF hole rather than a false-positive fix.
func TestValidateExclusion_ScopeIsMandatory(t *testing.T) {
	for _, tc := range []struct {
		name, wantErr string
		mut           func(*Exclusion)
	}{
		{"no host", "host is required", func(e *Exclusion) { e.Host = "" }},
		{"no uri", "uri-prefix is required", func(e *Exclusion) { e.URIPrefix = "" }},
		{"relative uri", "must start with /", func(e *Exclusion) { e.URIPrefix = "wp-json/x" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := validExclusion()
			tc.mut(&e)
			err := ValidateExclusion(e)
			if err == nil {
				t.Fatalf("want an error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// The strings are interpolated into a SecRule. A stray quote would terminate
// the directive early and could change what the rule does.
func TestValidateExclusion_RejectsSeclangBreakingInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Exclusion)
	}{
		{"quote in uri", func(e *Exclusion) { e.URIPrefix = `/x" "@rx .*` }},
		{"newline in uri", func(e *Exclusion) { e.URIPrefix = "/x\nSecAction" }},
		{"backslash in uri", func(e *Exclusion) { e.URIPrefix = `/x\\` }},
		{"quote in note", func(e *Exclusion) { e.Note = `oops" ` }},
		{"newline in note", func(e *Exclusion) { e.Note = "a\nb" }},
		{"bad host chars", func(e *Exclusion) { e.Host = "evil host!" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := validExclusion()
			tc.mut(&e)
			if err := ValidateExclusion(e); err == nil {
				t.Error("seclang-breaking input was accepted")
			}
		})
	}
}

// Excluding the anomaly blocker is a path-allow wearing a rule ID — it turns
// the WAF off for that path rather than silencing one false positive.
func TestValidateExclusion_RefusesAnomalyBlockers(t *testing.T) {
	for _, id := range []string{"949110", "949111", "980170"} {
		e := validExclusion()
		e.RuleID = id
		err := ValidateExclusion(e)
		if err == nil {
			t.Errorf("rule %s accepted — that disables blocking for the path", id)
			continue
		}
		if !strings.Contains(err.Error(), "disables the WAF") {
			t.Errorf("rule %s rejected for the wrong reason: %v", id, err)
		}
	}
}

func TestValidateExclusion_RejectsNonCRSIDs(t *testing.T) {
	for _, id := range []string{"0", "-1", "abc", "9599100", "9597000"} {
		e := validExclusion()
		e.RuleID = id
		if err := ValidateExclusion(e); err == nil {
			t.Errorf("rule id %q accepted", id)
		}
	}
}

func TestRenderExclusions_HostChainedAndScoped(t *testing.T) {
	out := RenderExclusions([]Exclusion{validExclusion()})

	// Host first, URI second — a URI-only match would hit every tenant using
	// that path.
	if !strings.Contains(out, `SecRule REQUEST_HEADERS:Host "@streq arizot-e.com"`) {
		t.Errorf("host not matched first:\n%s", out)
	}
	if !strings.Contains(out, `SecRule REQUEST_URI "@beginsWith /wp-json/aramapp-leads/v1/inbound"`) {
		t.Errorf("uri prefix missing:\n%s", out)
	}
	if !strings.Contains(out, "ctl:ruleRemoveById=931120") {
		t.Errorf("wrong removal construct:\n%s", out)
	}
	if !strings.Contains(out, ",chain") {
		t.Errorf("not chained — host and uri must BOTH be required:\n%s", out)
	}
	// Only the working construct may ever be emitted.
	for _, dead := range []string{"ruleRemoveTargetById", "ruleRemoveTargetByTag", "SecRuleUpdateTarget"} {
		if strings.Contains(out, dead) {
			t.Errorf("emitted %s, which is a silent no-op", dead)
		}
	}
	// The operator's note survives, so the file explains itself.
	if !strings.Contains(out, "WhatsApp webhook") {
		t.Errorf("note dropped:\n%s", out)
	}
}

func TestRenderExclusions_EmptyIsEmpty(t *testing.T) {
	if got := RenderExclusions(nil); got != "" {
		t.Errorf("nil list rendered %q", got)
	}
}

// Deterministic output keeps write-on-diff quiet: an unchanged set must render
// byte-identically regardless of the order the rows come back from the DB.
func TestRenderExclusions_Deterministic(t *testing.T) {
	a := Exclusion{Host: "b.com", URIPrefix: "/z", RuleID: "942100"}
	b := Exclusion{Host: "a.com", URIPrefix: "/y", RuleID: "931120"}
	c := Exclusion{Host: "a.com", URIPrefix: "/y", RuleID: "930120"}

	first := RenderExclusions([]Exclusion{a, b, c})
	second := RenderExclusions([]Exclusion{c, a, b})
	if first != second {
		t.Errorf("output depends on input order:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	// a.com sorts before b.com; within a.com, 930120 before 931120.
	if strings.Index(first, "a.com") > strings.Index(first, "b.com") {
		t.Error("hosts not sorted")
	}
	if strings.Index(first, "ruleRemoveById=930120") > strings.Index(first, "ruleRemoveById=931120") {
		t.Error("rule ids not sorted within a host")
	}
}

// Ids must not collide with the built-in 9,599,xxx range, or the two sets would
// fight and CrowdSec would refuse to load with "duplicated rule id".
func TestRenderExclusions_IDRangeDoesNotCollide(t *testing.T) {
	var list []Exclusion
	for i := 0; i < 5; i++ {
		e := validExclusion()
		e.URIPrefix = "/p" + string(rune('a'+i))
		list = append(list, e)
	}
	out := RenderExclusions(list)
	for i := 0; i < 5; i++ {
		want := "id:" + itoa(OperatorExclusionIDBase+i)
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "id:9599") {
		t.Error("operator ids collided with the built-in 9,599,xxx range")
	}
}

// A malformed entry must be reported in the output, not silently dropped — a
// rule that vanished without explanation is how an operator debugs the wrong
// thing for an hour.
func TestRenderExclusions_InvalidEntryIsReportedNotSwallowed(t *testing.T) {
	bad := validExclusion()
	bad.RuleID = "949110" // anomaly blocker
	out := RenderExclusions([]Exclusion{validExclusion(), bad})

	if !strings.Contains(out, "# SKIPPED") {
		t.Errorf("invalid entry silently dropped:\n%s", out)
	}
	if strings.Contains(out, "ctl:ruleRemoveById=949110") {
		t.Errorf("anomaly blocker rendered anyway:\n%s", out)
	}
	// The valid one still renders.
	if !strings.Contains(out, "ctl:ruleRemoveById=931120") {
		t.Errorf("valid entry lost:\n%s", out)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
