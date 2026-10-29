package appseccfg

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// A path-scoped exclusion matches REQUEST_URI as the client wrote it, while
// nginx resolves ".." segments before it picks a location. Without the
// traversal guard, an exclusion on "/excluded/" applied to
// "/excluded/%2e%2e/other", which nginx serves as "/other".

// seclangChains splits a seclang body into rule chains: a SecRule line, plus
// the lines that follow while the previous one ends its actions with "chain".
func seclangChains(body string) [][]string {
	var chains [][]string
	open := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "SecRule") {
			continue
		}
		if open {
			chains[len(chains)-1] = append(chains[len(chains)-1], trimmed)
		} else {
			chains = append(chains, []string{trimmed})
		}
		open = strings.HasSuffix(trimmed, `,chain"`)
	}
	return chains
}

// Every chain that drops a rule and is scoped by a path carries the guard,
// and the drop sits on the guard or after it, so the guard gates it.
func TestTraversalGuard_EveryPathScopedExclusionHasIt(t *testing.T) {
	operator := RenderExclusions([]Exclusion{
		validExclusion(),
		{Host: "cloud.example.com", URIPrefix: "/remote.php/dav/", RuleID: "911100"},
	})
	for name, body := range map[string]string{"built-in": CRSPluginBefore(), "operator": operator} {
		checked := 0
		for _, chain := range seclangChains(body) {
			joined := strings.Join(chain, "\n")
			if !strings.Contains(joined, "ctl:ruleRemoveById=") {
				continue
			}
			// wc-ajax= is matched anywhere in the URI, query string included,
			// so it has no path prefix for ".." to climb out of.
			if strings.Contains(chain[0], `"@contains wc-ajax="`) {
				continue
			}
			checked++
			guard := -1
			for i, l := range chain {
				if strings.HasPrefix(l, TraversalGuard+",") {
					guard = i
				}
			}
			if guard < 0 {
				t.Errorf("%s: path-scoped exclusion without the traversal guard:\n%s", name, joined)
				continue
			}
			for i, l := range chain[:guard] {
				if strings.Contains(l, "ctl:ruleRemoveById=") {
					t.Errorf("%s: rule drop on link %d runs before the guard on link %d:\n%s", name, i, guard, joined)
				}
			}
		}
		if checked == 0 {
			t.Errorf("%s: no path-scoped exclusion found; the test checks nothing", name)
		}
	}
}

// urlDecodeUni emulates Coraza's t:urlDecodeUni: %XX, %uXXXX and "+" decoded,
// an invalid escape left as it is.
func urlDecodeUni(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '+':
			b.WriteByte(' ')
		case s[i] == '%' && i+5 < len(s) && (s[i+1] == 'u' || s[i+1] == 'U'):
			if n, err := strconv.ParseUint(s[i+2:i+6], 16, 16); err == nil {
				b.WriteRune(rune(n))
				i += 5
				continue
			}
			b.WriteByte(s[i])
		case s[i] == '%' && i+2 < len(s):
			if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i += 2
				continue
			}
			b.WriteByte(s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func TestTraversalGuard_RefusesDotDotSegmentsOnly(t *testing.T) {
	if !strings.Contains(TraversalGuard, `"t:none,t:urlDecodeUni,t:urlDecodeUni`) {
		t.Fatalf("the guard must decode the URI twice: %s", TraversalGuard)
	}
	m := regexp.MustCompile(`"!@rx (.*?)" "`).FindStringSubmatch(TraversalGuard)
	if m == nil {
		t.Fatalf("no negated @rx in the guard: %s", TraversalGuard)
	}
	re := regexp.MustCompile(m[1]) // Coraza's @rx is Go's regexp
	refuse := func(uri string) bool { return re.MatchString(urlDecodeUni(urlDecodeUni(uri))) }

	for _, uri := range []string{
		"/excluded/../other",
		"/excluded/..",
		"/excluded/..?x=1",
		"/excluded/..;/other", // a Java servlet backend resolves "..;"
		"/excluded/..#x",      // nginx ends the path at a raw "#"
		"/excluded/../..#x",
		"/excluded/..%23x",
		"/excluded/%2e%2e/other",
		"/excluded/%2E%2e/other",
		"/excluded/.%2e/other",
		"/excluded%2f..%2fother",
		"/excluded/%252e%252e/other", // a proxied app may decode again
		"/excluded/%u002e%u002e/other",
		"/wp-json/elementor/../../wp-login.php",
		"/excluded/?next=/../other", // query too: refused, the rule stays on
	} {
		if !refuse(uri) {
			t.Errorf("guard lets the exclusion follow %q out of its prefix", uri)
		}
	}
	for _, uri := range []string{
		"/excluded/report..final.pdf",
		"/remote.php/dav/files/u/a..b",
		"/excluded/...",
		"/excluded/.../other",
		"/excluded/./other",
		"/excluded/.hidden",
		"/excluded/?q=../other",
		"/wp-json/elementor/v1/globals",
	} {
		if refuse(uri) {
			t.Errorf("guard refuses %q, which stays inside the prefix", uri)
		}
	}
}

// ruleFilterApplies evaluates a by-name exclusion's pre_eval filter for an
// in-band request to host with the raw request URI uri. CrowdSec builds
// req.URL by parsing the URI the bouncer sends, so req.URL.Path is decoded
// once. Only the terms RenderRuleExclusions writes are known; any other term
// fails the test, so a new term needs a case here.
func ruleFilterApplies(t *testing.T, filter, host, uri string) bool {
	t.Helper()
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("test URI %q does not parse: %v", uri, err)
	}
	term := regexp.MustCompile(`^(!\()?(\S+) (==|startsWith|contains|matches) "([^"]*)"\)?$`)
	for _, part := range strings.Split(filter, " && ") {
		if part == "IsInBand == true" {
			continue
		}
		m := term.FindStringSubmatch(part)
		if m == nil {
			t.Fatalf("unknown filter term %q in %q", part, filter)
		}
		var field string
		switch m[2] {
		case "req.Host":
			field = host
		case "req.URL.Host":
			field = u.Host
		case "req.URL.Path":
			field = u.Path
		default:
			t.Fatalf("unknown field in filter term %q", part)
		}
		var ok bool
		switch m[3] {
		case "==":
			ok = field == m[4]
		case "startsWith":
			ok = strings.HasPrefix(field, m[4])
		case "contains":
			ok = strings.Contains(field, m[4])
		case "matches":
			ok = regexp.MustCompile(m[4]).MatchString(field)
		}
		if m[1] != "" {
			ok = !ok
		}
		if !ok {
			return false
		}
	}
	return true
}

// A by-name exclusion matches req.URL.Path, decoded once. A path that reaches
// ".." only after a second decode, by a proxied app, must keep the rule on,
// as TraversalGuard does for the CRS-id exclusions.
func TestRuleExclusionFilter_StaysInsideItsPrefix(t *testing.T) {
	e := namedExclusion()
	e.Host, e.URIPrefix = "cloud.example.com", "/excluded/"
	d := parseAppsec(t, Render(Opts{Inband: []string{"crowdsecurity/vpatch-*"}, RuleExclusions: []Exclusion{e}}))
	if len(d.PreEval) != 1 {
		t.Fatalf("want one pre_eval entry, got %d", len(d.PreEval))
	}
	filter := d.PreEval[0].Filter

	for _, uri := range []string{
		"/excluded/../other",
		"/excluded/%2e%2e/other",
		"/excluded/%2E%2e/other",
		"/excluded/.%2e/other",
		"/excluded%2f..%2fother",
		"/excluded/%252e%252e/other", // a proxied app may decode again
		"/excluded/%252E%252E/other",
		"/excluded/.%252e/other",
		"/excluded/%252e./other",
		"/excluded/%25u002e%25u002e/other",
		"//other/excluded/",
	} {
		if ruleFilterApplies(t, filter, "cloud.example.com", uri) {
			t.Errorf("exclusion follows %q out of its prefix", uri)
		}
	}
	for _, uri := range []string{
		"/excluded/file.php",
		"/excluded/a/b?x=1",
		"/excluded/50%25off.pdf",
		"/excluded/%41bc",
		"/excluded/.hidden",
		"/excluded/?q=../other",
	} {
		if !ruleFilterApplies(t, filter, "cloud.example.com", uri) {
			t.Errorf("exclusion no longer applies to %q, which stays inside the prefix", uri)
		}
	}
	if ruleFilterApplies(t, filter, "other.example.com", "/excluded/file.php") {
		t.Errorf("exclusion applies on another host")
	}
}
