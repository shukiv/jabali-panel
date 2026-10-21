package appseccfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// rule_exclusions.go — operator exclusions that name a CrowdSec AppSec rule
// (crowdsecurity/vpatch-…, crowdsecurity/generic-…) instead of a CRS rule id.
//
// A virtual patch is not a CRS rule: it runs under a 9- or 10-digit id derived
// from its name, outside the range a ctl:ruleRemoveById exclusion may target,
// so the before-plugin cannot turn it off. CrowdSec turns one off by name, in a
// pre_eval hook of the AppSec config:
//
//	pre_eval:
//	 - filter: IsInBand == true && req.Host == "…" && req.URL.Path startsWith "…"
//	   apply:
//	    - RemoveInBandRuleByName("crowdsecurity/vpatch-…")
//
// These entries therefore render into jabali-appsec.yaml (Render, via
// Opts.RuleExclusions), not into the operator before-plugin. The same scope
// rules hold: one host, one path prefix, one rule.
//
// jabali-appsec.yaml has more than one writer: `jabali appsec render-config`
// (from the database) and the agent (operator apply, geoblock, bot detection;
// no database). The list rides in the file's header as JSON
// (RuleExclusionsHeader), the way the geoblock and bot-detection settings do,
// so an agent re-render keeps the entries the last apply wrote.

// MaxRuleIDLen is the longest rule-id an exclusion can store: the
// crs_rule_exclusions.rule_id column (varchar(128)).
const MaxRuleIDLen = 128

// RuleExclusionsHeader starts the header line of jabali-appsec.yaml that holds
// the named rule exclusions as JSON. Absent when there are none.
const RuleExclusionsHeader = "# jabali-rule-exclusions:"

// AppsecRulesDir is where the hub installs AppSec rules, one YAML per rule.
const AppsecRulesDir = "/etc/crowdsec/appsec-rules"

// ruleNameRE is the AppSec rules an exclusion may name: CrowdSec's virtual
// patches and generic rules, which jabali-appsec loads in band by wildcard.
// crowdsecurity/crs (all of CRS as one rule) and crowdsecurity/base-config are
// deliberately not here: removing either turns the WAF off for the path, which
// is a path-allow, not an exclusion.
var ruleNameRE = regexp.MustCompile(`^crowdsecurity/(vpatch|generic)-[A-Za-z0-9._-]+$`)

// IsRuleName reports whether rule-id is meant as an AppSec rule name rather
// than a CRS rule id. Anything else, malformed or not, is treated as an id, so
// it is validated, and reported if invalid, where ids render.
func IsRuleName(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), "crowdsecurity/")
}

// validateRuleName accepts the rule names ruleNameRE allows. The character set
// is also what keeps the name safe inside the RemoveInBandRuleByName("…")
// literal: no quote, no backslash.
func validateRuleName(id string) error {
	if len(id) > MaxRuleIDLen || !ruleNameRE.MatchString(id) {
		return fmt.Errorf("rule %q is not a rule an exclusion can name: want crowdsecurity/vpatch-… or crowdsecurity/generic-…, at most %d characters", id, MaxRuleIDLen)
	}
	return nil
}

// crsExclusions returns the entries that target a CRS rule id.
func crsExclusions(list []Exclusion) []Exclusion {
	var out []Exclusion
	for _, e := range list {
		if !IsRuleName(e.RuleID) {
			out = append(out, e)
		}
	}
	return out
}

// RuleNameExclusions returns the entries that name an AppSec rule.
func RuleNameExclusions(list []Exclusion) []Exclusion {
	var out []Exclusion
	for _, e := range list {
		if IsRuleName(e.RuleID) {
			out = append(out, e)
		}
	}
	return out
}

// sortedExclusions returns a copy of list sorted by (host, uri, rule), so an
// unchanged set renders byte-identically whatever order the rows came in.
func sortedExclusions(list []Exclusion) []Exclusion {
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
	return sorted
}

// RenderRuleExclusions returns the pre_eval entries for the exclusions in list
// that name an AppSec rule, without the `pre_eval:` key (Render owns it, since
// the geoblock entry shares the list). "" when there are none.
//
// Each filter is a single-quoted YAML scalar: a path may hold ": " or " #",
// which would end a plain scalar early. Inside it, the expr string literals use
// double quotes, which ValidateExclusion keeps out of every field. The path is
// matched against the decoded path without the query string. The bouncer sends
// the raw request URI, and CrowdSec parses it as a URL, so two forms would
// carry the exclusion to a path nginx serves elsewhere: a path with ".."
// ("/excluded/../other"), and one that parses with an authority
// ("//other/excluded/" has path "/excluded/"). Neither matches.
func RenderRuleExclusions(list []Exclusion) string {
	sorted := sortedExclusions(RuleNameExclusions(list))
	if len(sorted) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" # ---- operator rule exclusions by name ----\n")
	b.WriteString(" # Added via `jabali appsec exclusion add`. Each turns ONE AppSec rule off for\n")
	b.WriteString(" # ONE host and ONE path prefix.\n")
	rendered := 0
	for _, e := range sorted {
		if rendered >= maxOperatorExclusions {
			fmt.Fprintf(&b, " # !! %d further rule exclusion(s) NOT rendered — limit of %d reached.\n",
				len(sorted)-rendered, maxOperatorExclusions)
			break
		}
		if err := ValidateExclusion(e); err != nil {
			// %q, never %s: an invalid entry is untrusted, and a raw newline
			// would end this comment and load the rest as live YAML.
			fmt.Fprintf(&b, " # SKIPPED (%q %q rule %q): %v\n", e.Host, e.URIPrefix, e.RuleID, err)
			continue
		}
		host := strings.ToLower(strings.TrimSpace(e.Host))
		uri := strings.TrimSpace(e.URIPrefix)
		if note := strings.TrimSpace(e.Note); note != "" {
			fmt.Fprintf(&b, " # %s\n", note)
		}
		filter := `IsInBand == true && req.Host == "` + host + `" && req.URL.Host == "" && req.URL.Path startsWith "` + uri +
			`" && !(req.URL.Path contains "..")`
		b.WriteString(" - filter: '" + strings.ReplaceAll(filter, "'", "''") + "'\n")
		b.WriteString("   apply:\n")
		b.WriteString(`    - RemoveInBandRuleByName("` + strings.TrimSpace(e.RuleID) + `")` + "\n")
		rendered++
	}
	return b.String()
}

// ruleExclusionsHeaderLine returns the header line that carries the named
// entries of list, or "" when there are none.
func ruleExclusionsHeaderLine(list []Exclusion) string {
	named := sortedExclusions(RuleNameExclusions(list))
	if len(named) == 0 {
		return ""
	}
	raw, err := json.Marshal(named)
	if err != nil {
		return "" // a []Exclusion of strings always marshals
	}
	return RuleExclusionsHeader + " " + asciiJSON(raw) + "\n"
}

// asciiJSON escapes every non-ASCII character of a JSON text as \uXXXX. The
// header line is a YAML comment, and YAML also ends a line at U+0085, U+2028
// and U+2029, which json.Marshal leaves as they are (U+0085) or which an
// entry that failed validation may carry. Non-ASCII only occurs inside JSON
// strings, where the escape means the same character.
func asciiJSON(raw []byte) string {
	var b strings.Builder
	for _, r := range string(raw) {
		switch {
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		case r > 0xFFFF:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&b, `\u%04x\u%04x`, hi, lo)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

// RuleExclusionsFromConfig returns the named rule exclusions recorded in the
// header of a rendered jabali-appsec.yaml, so a writer without the database
// re-renders them unchanged. nil when the header has none or does not parse.
// Entries are not validated here: Render validates each one as it renders.
func RuleExclusionsFromConfig(body string) []Exclusion {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			break // the header ends at the first non-comment line
		}
		if !strings.HasPrefix(line, RuleExclusionsHeader) {
			continue
		}
		var list []Exclusion
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, RuleExclusionsHeader))), &list); err != nil {
			return nil
		}
		return RuleNameExclusions(list)
	}
	return nil
}

// PanelHostFromConfig reads back the scope of the panel-path allowlist of a
// rendered jabali-appsec.yaml, so a writer that re-renders only to change
// something else (operator apply) keeps it. scoped is false only when the file
// holds the allowlist by path alone (Opts.PanelHost ""). Anything else counts
// as scoped, so a read-back never widens the allowlist; host is "" when the
// scoped host does not read back as a clean host name.
func PanelHostFromConfig(body string) (host string, scoped bool) {
	const pathOnly = `- filter: req.URL.Path startsWith "/api/v1/"`
	const prefix = `- filter: (req.URL.Path startsWith "/api/v1/"`
	const hostExpr = `) && req.Host == "`
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, pathOnly) {
			return "", false
		}
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		i := strings.LastIndex(line, hostExpr)
		if i < 0 || !strings.HasSuffix(line, `"`) {
			return "", true
		}
		h := strings.TrimSuffix(line[i+len(hostExpr):], `"`)
		if clean := sanitizeWebmailHosts([]string{h}); len(clean) == 1 && clean[0] == h {
			return h, true
		}
		return "", true
	}
	return "", true
}

// DetectInbandRules returns the in-band rules jabali-appsec should load, in a
// stable order. vpatch-* and generic-* are always included (the pre-flight hub
// installs guarantee them). base-config / crs / crs-exclusion-plugin-wordpress
// are presence-gated by their file in dir: a hub regression that removes one of
// them drops it from the config, so crowdsec never references a missing rule
// (it refuses to start otherwise). Every writer of jabali-appsec.yaml uses
// this, so a re-render by one never drops a rule set another loaded.
func DetectInbandRules(dir string) []string {
	out := []string{
		"crowdsecurity/vpatch-*",
		"crowdsecurity/generic-*",
	}
	for _, gated := range []struct{ file, rule string }{
		{"base-config.yaml", "crowdsecurity/base-config"},
		{"crs.yaml", "crowdsecurity/crs"},
		{"crs-exclusion-plugin-wordpress.yaml", "crowdsecurity/crs-exclusion-plugin-wordpress"},
	} {
		if _, err := os.Stat(filepath.Join(dir, gated.file)); err == nil {
			out = append(out, gated.rule)
		}
	}
	return out
}
