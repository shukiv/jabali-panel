package commands

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestApplyPlanSieveInterpreterParity pins the GH #1795 x:SieveUserInterpreter
// limits identical in the two places they must agree:
//
//   - install/stalwart/apply-plan.json.tmpl — applied by `stalwart-cli apply`
//     on a FRESH install only (skipped when :8446 already serves).
//   - install.sh — the unconditional `stalwart-cli update x:SieveUserInterpreter`
//     converger that reaches EXISTING boxes on every `jabali update`.
//
// This is the [[feedback_applyplan_converger_drift]] trap (#1818/#371): an edit
// to one file that never reaches a real box because the other is the path that
// actually runs there. If the two ever disagree, multi-target forwards behave
// differently on a fresh box than on an updated one — so this guard fails the
// build the moment they drift.
func TestApplyPlanSieveInterpreterParity(t *testing.T) {
	root := repoRootT(t)

	planRaw, err := os.ReadFile(filepath.Join(root, "install", "stalwart", "apply-plan.json.tmpl"))
	if err != nil {
		t.Fatalf("read apply-plan.json.tmpl: %v", err)
	}
	// The template carries a {{.MariaDBPassword}} placeholder elsewhere, but the
	// x:SieveUserInterpreter block is pure JSON — extract that block, then the
	// two ints from it, so an unrelated occurrence cannot shadow the values.
	planBlock := sieveInterpBlockRe.Find(planRaw)
	if planBlock == nil {
		t.Fatal("apply-plan.json.tmpl: no x:SieveUserInterpreter block found")
	}
	planRedir := mustSieveInt(t, planBlock, sieveMaxRedirectsRe, "apply-plan maxRedirects")
	planOut := mustSieveInt(t, planBlock, sieveMaxOutMsgsRe, "apply-plan maxOutMessages")

	shRaw, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	// install.sh mentions the Stalwart DEFAULTS (maxRedirects=1 / maxOutMessages=3)
	// in a comment, so parse ONLY the sieve_interp_patch='{...}' payload, never
	// the whole file.
	patch := sieveInterpPatchRe.FindSubmatch(shRaw)
	if patch == nil {
		t.Fatal("install.sh: no sieve_interp_patch='{...}' assignment found")
	}
	shRedir := mustSieveInt(t, patch[1], sieveMaxRedirectsRe, "install.sh maxRedirects")
	shOut := mustSieveInt(t, patch[1], sieveMaxOutMsgsRe, "install.sh maxOutMessages")

	if planRedir != shRedir {
		t.Errorf("maxRedirects drift: apply-plan.json.tmpl=%d, install.sh=%d — edit BOTH", planRedir, shRedir)
	}
	if planOut != shOut {
		t.Errorf("maxOutMessages drift: apply-plan.json.tmpl=%d, install.sh=%d — edit BOTH", planOut, shOut)
	}
	// Guard against a matched-but-nonsensical zero from a regex that slid onto a
	// comment: the whole point of raising the limits is that they exceed the
	// defaults (1 / 3).
	if shRedir <= 1 || shOut <= 3 {
		t.Errorf("sieve interpreter limits not raised above Stalwart defaults: maxRedirects=%d maxOutMessages=%d", shRedir, shOut)
	}
}

var (
	// sieveInterpBlockRe isolates the x:SieveUserInterpreter object's "value"
	// block in the apply plan (non-greedy up to the closing brace of value).
	sieveInterpBlockRe = regexp.MustCompile(`(?s)"object":\s*"x:SieveUserInterpreter".*?"value":\s*\{[^}]*\}`)
	// sieve_interp_patch='{...}' — the single-quoted JSON payload in install.sh.
	sieveInterpPatchRe  = regexp.MustCompile(`sieve_interp_patch='([^']*)'`)
	sieveMaxRedirectsRe = regexp.MustCompile(`"maxRedirects"\s*:\s*(\d+)`)
	sieveMaxOutMsgsRe   = regexp.MustCompile(`"maxOutMessages"\s*:\s*(\d+)`)
)

func mustSieveInt(t *testing.T, in []byte, re *regexp.Regexp, what string) int {
	t.Helper()
	m := re.FindSubmatch(in)
	if m == nil {
		t.Fatalf("%s: pattern %q not found in %q", what, re.String(), string(in))
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("%s: parse int %q: %v", what, string(m[1]), err)
	}
	return n
}
