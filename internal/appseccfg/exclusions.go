package appseccfg

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// exclusions.go — JAB-227. Operator-managed CRS false-positive exclusions,
// rendered into the same before-plugin as the built-in ones.
//
// The built-in exclusions cover false positives jabali ships with. This covers
// the ones only the operator can see: a customer's WhatsApp webhook whose
// message bodies read as RFI, a plugin's REST route that trips libinjection on
// ordinary content. Before this existed the only options were editing a file on
// the box by hand — and knowing that only ctl:ruleRemoveById has any effect,
// since the target-scoped forms are silent no-ops — or leaving the customer
// blocked.
//
// Two invariants make this safe enough to expose:
//
//   - Scope is MANDATORY. ruleRemoveById drops the rule for the entire matched
//     request, so an exclusion must name both a host and a URI prefix. There is
//     deliberately no "whole host" or "whole server" form.
//   - IDs live in 9,597,000-9,597,999, kept clear of the built-in 9,599,xxx
//     range, so the WAF-hole guard can tell operator entries from shipped ones
//     and hold them to their own rule.

// OperatorExclusionIDBase is the first SecRule id used for operator-managed
// exclusions. Distinct from the built-in 9,599,xxx range on purpose.
const OperatorExclusionIDBase = 9597000

// maxOperatorExclusions caps how many entries render. Each one is a SecRule
// evaluated on every request, and an unbounded list is a performance problem
// the operator cannot see. Exceeding it is reported, never silently truncated.
const maxOperatorExclusions = 900

// Exclusion is one operator-managed rule exclusion.
type Exclusion struct {
	Host      string
	URIPrefix string
	RuleID    string
	Note      string
}

// ValidateExclusion rejects anything that would break the seclang literal or
// widen the exclusion beyond its intended scope.
//
// The quoting checks are not cosmetic: these strings are interpolated into a
// SecRule, so a stray double-quote would end the directive early and could turn
// a scoped exclusion into something else entirely.
func ValidateExclusion(e Exclusion) error {
	host := strings.ToLower(strings.TrimSpace(e.Host))
	if host == "" {
		return fmt.Errorf("host is required — an unscoped exclusion would disable the rule server-wide")
	}
	if len(host) > 253 {
		return fmt.Errorf("host too long")
	}
	for _, r := range host {
		if !(r == '.' || r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return fmt.Errorf("host %q has invalid characters (want [a-z0-9.-])", e.Host)
		}
	}

	uri := strings.TrimSpace(e.URIPrefix)
	if uri == "" {
		return fmt.Errorf("uri-prefix is required — an exclusion covering the whole host is a WAF hole")
	}
	if !strings.HasPrefix(uri, "/") {
		return fmt.Errorf("uri-prefix %q must start with /", uri)
	}
	if len(uri) > 512 {
		return fmt.Errorf("uri-prefix too long")
	}
	if strings.ContainsAny(uri, "\"\\\n\r") {
		return fmt.Errorf("uri-prefix %q contains a quote, backslash or newline", uri)
	}

	id := strings.TrimSpace(e.RuleID)
	n, err := strconv.Atoi(id)
	if err != nil || n <= 0 {
		return fmt.Errorf("rule-id %q must be a positive integer", e.RuleID)
	}
	// Refuse to drop the anomaly-scoring machinery itself: removing 949110 or
	// 980170 disables blocking for the path wholesale, which is a path-allow
	// wearing a rule ID.
	switch n {
	case 949110, 949111, 980170:
		return fmt.Errorf("rule %d is the anomaly-score blocker — excluding it disables the WAF for that path, which is not what an exclusion is for", n)
	}
	if n >= 9500000 {
		return fmt.Errorf("rule %d is in the jabali/plugin reserved range, not a CRS detection rule", n)
	}

	if strings.ContainsAny(e.Note, "\"\n\r") {
		return fmt.Errorf("note contains a quote or newline")
	}
	return nil
}

// operatorBeforeFileHeader labels the standalone operator-managed before-plugin
// file (CRSPluginOperatorBeforePath). Split out of jabali-before.conf (GH #1655)
// so the agent's boot-time re-render of the built-in file — which has no
// database access — can no longer overwrite operator exclusions.
const operatorBeforeFileHeader = "# Managed by jabali — operator CRS \"before\" exclusions.\n" +
	"# DO NOT hand-edit. Written by `jabali appsec render-config` from the panel\n" +
	"# database (`jabali appsec exclusion add`/`rm`). Kept SEPARATE from\n" +
	"# jabali-before.conf so the agent's boot re-render of the built-in file cannot\n" +
	"# clobber these (GH #1655). Loaded before the CRS detection rules by the same\n" +
	"# crs-plugins/*/*-before.conf glob; order relative to jabali-before.conf does\n" +
	"# not matter (these are self-contained ctl:ruleRemoveById rules in phase 1).\n"

// RenderOperatorBeforeFile returns the full body of the standalone
// operator-managed before-plugin file, or "" when there are no operator
// exclusions at all — the caller removes the file in that case so a
// since-removed exclusion cannot linger live. A non-empty list always yields a
// file, even if every entry fails validation: those render as SKIPPED comments
// (see RenderExclusions), the breadcrumb an operator needs to see WHY a rule
// vanished rather than debug the wrong thing.
func RenderOperatorBeforeFile(list []Exclusion) string {
	if len(list) == 0 {
		return ""
	}
	return operatorBeforeFileHeader + RenderExclusions(list)
}

// RenderExclusions emits the operator-managed section of the before-plugin.
//
// Deterministic: sorted by (host, uri, rule) so an unchanged set renders
// byte-identically and the write-on-diff reconcile stays quiet. Invalid entries
// are skipped with a comment rather than dropped silently — a rule that vanished
// without explanation is how an operator ends up debugging the wrong thing.
func RenderExclusions(list []Exclusion) string {
	if len(list) == 0 {
		return ""
	}
	sorted := make([]Exclusion, len(list))
	copy(sorted, list)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Host != sorted[j].Host {
			return sorted[i].Host < sorted[j].Host
		}
		if sorted[i].URIPrefix != sorted[j].URIPrefix {
			return sorted[i].URIPrefix < sorted[j].URIPrefix
		}
		return sorted[i].RuleID < sorted[j].RuleID
	})

	var b strings.Builder
	b.WriteString("#\n# ---- operator-managed exclusions (JAB-227) ----\n")
	b.WriteString("# Added via `jabali appsec exclusion add`. Each entry is scoped to ONE host and\n")
	b.WriteString("# ONE URI prefix, because ctl:ruleRemoveById drops the rule for the whole\n")
	b.WriteString("# matched request. Chained so the host is checked first: a URI prefix alone\n")
	b.WriteString("# would apply the exclusion to every tenant that happens to use that path.\n")

	id := OperatorExclusionIDBase
	rendered := 0
	for _, e := range sorted {
		if rendered >= maxOperatorExclusions {
			fmt.Fprintf(&b, "# !! %d further exclusion(s) NOT rendered — limit of %d reached.\n",
				len(sorted)-rendered, maxOperatorExclusions)
			break
		}
		if err := ValidateExclusion(e); err != nil {
			fmt.Fprintf(&b, "# SKIPPED (%s %s rule %s): %v\n", e.Host, e.URIPrefix, e.RuleID, err)
			continue
		}
		host := strings.ToLower(strings.TrimSpace(e.Host))
		uri := strings.TrimSpace(e.URIPrefix)
		if e.Note != "" {
			fmt.Fprintf(&b, "# %s\n", strings.TrimSpace(e.Note))
		}
		fmt.Fprintf(&b, "SecRule REQUEST_HEADERS:Host \"@streq %s\" \"id:%d,phase:1,pass,nolog,chain\"\n", host, id)
		fmt.Fprintf(&b, "    SecRule REQUEST_URI \"@beginsWith %s\" \"t:none,ctl:ruleRemoveById=%s\"\n",
			uri, strings.TrimSpace(e.RuleID))
		id++
		rendered++
	}
	return b.String()
}
