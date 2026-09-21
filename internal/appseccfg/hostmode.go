package appseccfg

import (
	"fmt"
	"sort"
	"strings"
)

// hostmode.go — GH #1641. Per-host AppSec mode.
//
// The operator-managed exclusions in exclusions.go drop ONE CRS rule for ONE
// host+path. That is the right tool for a single false positive, but it is
// whack-a-mole for a host whose ordinary traffic trips a rotating set of rules —
// a code-discussion forum (Flarum) where users legitimately paste SQL and shell
// snippets into post bodies, so the SQLi (942xxx) and RCE (932xxx) detections
// fire on the content itself, a different rule every week.
//
// A host mode is the coarser, honest tool for that case: "detect" puts one host
// into DETECTION-ONLY. It is NOT "AppSec off" and is deliberately not named that:
//
//   - The jabali/native virtual-patch rules (>= 9,500,000, which `deny` directly
//     rather than via the anomaly score) still BLOCK, so a scanner hitting
//     `.env` / `.git` on that host is still stopped.
//   - The behavioural CrowdSec bouncer (IP reputation, brute-force scenarios) is
//     a separate layer and is untouched.
//   - The change is host-scoped and surgical, not an nginx-level WAF bypass, so a
//     later audit-logging feature would still see the host's traffic.
//
// The trade is honest and worth stating: CrowdSec only emits an AppSec event when
// a request is BLOCKED. Suppress the block and there is no event, so while a host
// is in detect it goes DARK in `jabali appsec explain` (which reads blocks) — you
// stop the 403s at the cost of that host's block visibility. This is why detect
// is the coarse last resort after per-path exclusions, not the first reach.
//
// Only the CRS anomaly-SCORE blocking is suppressed, by dropping exactly the
// three rules ValidateExclusion refuses for a per-path exclusion: 949110 (inbound
// anomaly-score threshold — the blocker), 949111 (inbound early-blocking
// threshold), and 980170 (anomaly-score reporting/correlation). Dropping those
// per PATH is a WAF hole wearing a rule id, which is why the exclusion tool
// refuses them; dropping them for a whole HOST is a deliberate, named,
// operator-only posture — a separate surface with its own validator, so the
// exclusion tool's refusal stays intact.
//
// Mechanism parity: ctl:ruleRemoveById is the only removal construct CrowdSec's
// Coraza engine honours (the target-scoped forms are silent no-ops, JAB-227),
// and a phase-1 rule dropping a phase-2 detection is exactly the shipped shape of
// the sprintdoc default (GH #1669, 932120). The host match is @streq (exact), so
// "forum.example.com" never matches "notforum.example.com".

// HostModeDetect is the only non-default host mode: detection-only. (The default,
// unlisted, is full blocking — a host with no row is unaffected.)
const HostModeDetect = "detect"

// hostModeBlockingRuleIDs are the CRS anomaly-score blockers. Removing them
// leaves every detection rule running (still scores, still logs) but stops the
// score from ever reaching a block. This is byte-for-byte the set
// ValidateExclusion refuses for a per-path exclusion — the whole point of the
// host mode is that dropping them is safe ONLY when it is a deliberate,
// host-scoped, operator-set posture, never a per-path exclusion.
var hostModeBlockingRuleIDs = []string{"949110", "949111", "980170"}

// OperatorHostModeIDBase is the first SecRule id for host-mode rules. Disjoint
// from operator exclusions (9,597,xxx) and built-ins (9,599,xxx) so the ranges
// never collide and each surface stays legible in the rendered file.
const OperatorHostModeIDBase = 9598000

// maxHostModes caps how many host-mode rules render. Each is a SecRule evaluated
// on every request; an unbounded list is a performance problem the operator
// cannot see. Exceeding it is reported, never silently truncated.
const maxHostModes = 900

// HostMode is one operator-set per-host AppSec mode.
type HostMode struct {
	Host string
	Mode string
	Note string
}

// ValidateHostMode rejects anything that would break the seclang literal or name
// an unsupported mode. Its own surface: it does NOT reuse ValidateExclusion,
// whose refuse-list (949110/949111/980170) is exactly what a host mode drops on
// purpose.
func ValidateHostMode(m HostMode) error {
	host := strings.ToLower(strings.TrimSpace(m.Host))
	if host == "" {
		return fmt.Errorf("host is required")
	}
	if len(host) > 253 {
		return fmt.Errorf("host too long")
	}
	for _, r := range host {
		if !(r == '.' || r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("host %q has invalid characters (want [a-z0-9.-])", m.Host)
		}
	}
	if strings.TrimSpace(m.Mode) != HostModeDetect {
		return fmt.Errorf("mode %q not supported — the only host mode is %q (detection-only)", m.Mode, HostModeDetect)
	}
	if strings.ContainsAny(m.Note, "\"\n\r") {
		return fmt.Errorf("note contains a quote or newline")
	}
	return nil
}

// RenderHostModes emits the operator-managed host-mode section of the
// before-plugin. Deterministic (sorted by host) so an unchanged set renders
// byte-identically and the write-on-diff reconcile stays quiet. Invalid entries
// render as SKIPPED comments — the breadcrumb an operator needs to see WHY a mode
// did not take effect, rather than debug the wrong thing.
func RenderHostModes(list []HostMode) string {
	if len(list) == 0 {
		return ""
	}
	sorted := make([]HostMode, len(list))
	copy(sorted, list)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Host < sorted[j].Host })

	var b strings.Builder
	b.WriteString("#\n# ---- operator-managed host modes (GH #1641) ----\n")
	b.WriteString("# Added via `jabali appsec host-mode set`. 'detect' puts a host into\n")
	b.WriteString("# detection-only: the CRS anomaly-score BLOCK is suppressed, so the host stops\n")
	b.WriteString("# 403-ing on CRS. Native virtual-patches (>=9.5M, direct deny) and the\n")
	b.WriteString("# behavioural IP bouncer are unaffected. With no block, CrowdSec logs no AppSec\n")
	b.WriteString("# event for the host, so `jabali appsec explain` shows nothing for it.\n")

	id := OperatorHostModeIDBase
	rendered := 0
	for _, m := range sorted {
		if rendered >= maxHostModes {
			fmt.Fprintf(&b, "# !! %d further host mode(s) NOT rendered — limit of %d reached.\n",
				len(sorted)-rendered, maxHostModes)
			break
		}
		if err := ValidateHostMode(m); err != nil {
			fmt.Fprintf(&b, "# SKIPPED (%s mode %s): %v\n", m.Host, m.Mode, err)
			continue
		}
		host := strings.ToLower(strings.TrimSpace(m.Host))
		if m.Note != "" {
			fmt.Fprintf(&b, "# %s\n", strings.TrimSpace(m.Note))
		}
		// One self-contained phase-1 rule per host: exact Host match, then drop
		// every anomaly-score blocker so the request can score but never block.
		var ctl strings.Builder
		for _, rid := range hostModeBlockingRuleIDs {
			fmt.Fprintf(&ctl, ",ctl:ruleRemoveById=%s", rid)
		}
		fmt.Fprintf(&b, "SecRule REQUEST_HEADERS:Host \"@streq %s\" \"id:%d,phase:1,pass,nolog%s\"\n",
			host, id, ctl.String())
		id++
		rendered++
	}
	return b.String()
}
