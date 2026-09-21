package appseccfg

import (
	"strings"
	"testing"
)

func TestValidateHostMode(t *testing.T) {
	ok := HostMode{Host: "forum.example.com", Mode: HostModeDetect, Note: "flarum body FP"}
	if err := ValidateHostMode(ok); err != nil {
		t.Fatalf("valid host mode rejected: %v", err)
	}
	bad := []struct {
		name string
		m    HostMode
	}{
		{"empty host", HostMode{Host: "", Mode: HostModeDetect}},
		{"host with quote", HostMode{Host: "a\"b.com", Mode: HostModeDetect}},
		{"host with slash", HostMode{Host: "a/b.com", Mode: HostModeDetect}},
		{"unsupported mode", HostMode{Host: "forum.example.com", Mode: "off"}},
		{"empty mode", HostMode{Host: "forum.example.com", Mode: ""}},
		{"note with newline", HostMode{Host: "forum.example.com", Mode: HostModeDetect, Note: "a\nb"}},
	}
	for _, tc := range bad {
		if err := ValidateHostMode(tc.m); err == nil {
			t.Errorf("%s: expected rejection, got nil", tc.name)
		}
	}
}

// RenderHostModes must scope the detection-only rule to the EXACT host (@streq,
// never @contains/@rx — a wildcard would put unrelated hosts into detect) and
// must drop every anomaly-score blocker (the whole point is that the request can
// still score but never reach a block).
func TestRenderHostModes(t *testing.T) {
	out := RenderHostModes([]HostMode{{Host: "forum.example.com", Mode: HostModeDetect, Note: "flarum"}})

	mustContain := func(needle string) {
		t.Helper()
		if !strings.Contains(out, needle) {
			t.Errorf("rendered host mode missing %q. full output:\n%s", needle, out)
		}
	}
	// Exact host match — not a substring/regex operator that could catch
	// notforum.example.com.
	mustContain(`SecRule REQUEST_HEADERS:Host "@streq forum.example.com"`)
	if strings.Contains(out, "@contains") || strings.Contains(out, "@rx") {
		t.Errorf("host mode must use @streq (exact), not @contains/@rx:\n%s", out)
	}
	// phase-1, self-contained, and every anomaly-score blocker dropped.
	mustContain("phase:1")
	for _, id := range []string{"949110", "949111", "980170"} {
		mustContain("ctl:ruleRemoveById=" + id)
	}
	// The note surfaces as a comment.
	mustContain("# flarum")

	// Empty in → empty out (caller removes the file).
	if RenderHostModes(nil) != "" {
		t.Error("RenderHostModes(nil) must be empty")
	}
	// Invalid entries render as a SKIPPED breadcrumb, never a live rule.
	skipped := RenderHostModes([]HostMode{{Host: "bad host", Mode: HostModeDetect}})
	if !strings.Contains(skipped, "SKIPPED") {
		t.Errorf("invalid host mode must render a SKIPPED comment, got:\n%s", skipped)
	}
	if strings.Contains(skipped, "@streq") {
		t.Errorf("invalid host mode must NOT render a live rule, got:\n%s", skipped)
	}
}

// The operator file carries exclusions (JAB-227) and host modes (GH #1641)
// together; it is empty only when BOTH are empty (so the reconcile removes it),
// and non-empty when either is present.
func TestRenderOperatorBeforeFile_ExclusionsAndModes(t *testing.T) {
	if RenderOperatorBeforeFile(nil, nil) != "" {
		t.Error("both empty must render empty (file removed)")
	}
	modeOnly := RenderOperatorBeforeFile(nil, []HostMode{{Host: "forum.example.com", Mode: HostModeDetect}})
	if !strings.Contains(modeOnly, "@streq forum.example.com") {
		t.Errorf("host mode alone must render into the operator file:\n%s", modeOnly)
	}
	both := RenderOperatorBeforeFile([]Exclusion{validExclusion()}, []HostMode{{Host: "forum.example.com", Mode: HostModeDetect}})
	if !strings.Contains(both, "operator-managed exclusions") || !strings.Contains(both, "operator-managed host modes") {
		t.Errorf("both sections must render together:\n%s", both)
	}
}
