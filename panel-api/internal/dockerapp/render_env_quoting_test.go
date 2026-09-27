package dockerapp

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// envHostile carries every byte that broke a raw {{ index .Env "X" }}
// substitution (GH #322, GH #1790 follow-up): '$' (docker compose
// interpolation: "p@ss$x" became "p@ss"), '"' and '\' (YAML double-quoted
// scalar), " #" (YAML comment), a single quote, a backtick and "$(" (shell
// quoting and command substitution in one-shot installer scripts) and '@' ':'
// '/' '%' (URL userinfo). A tenant can send any of these as an install env
// override, since validateEnvKV only rejects newlines and NUL.
const envHostile = `p@ss$x"y\z #'` + "`id`$(id):/%"

// envMarker tags a value with the env name it came from, so the render walk
// can tell which catalog variable reached which compose scalar.
func envMarker(name string) string { return "<<" + name + ">>" }

// composeInterpolate applies docker compose's interpolation to a rendered
// scalar with every variable unset, giving what the container receives: "$$"
// is a literal '$', "$NAME" and "${...}" become "", and any other '$' is a
// compose "invalid template" error.
func composeInterpolate(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		switch {
		case i+1 < len(s) && s[i+1] == '$':
			b.WriteByte('$')
			i++
		case i+1 < len(s) && s[i+1] == '{':
			j := strings.IndexByte(s[i:], '}')
			if j < 0 {
				return "", fmt.Errorf("unterminated ${ at byte %d", i)
			}
			i += j
		case i+1 < len(s) && (s[i+1] == '_' || isASCIILetter(s[i+1])):
			j := i + 1
			for j < len(s) && (s[j] == '_' || isASCIILetter(s[j]) || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			i = j - 1
		default:
			return "", fmt.Errorf("invalid template: bare '$' at byte %d", i)
		}
	}
	return b.String(), nil
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

// hostileOverrides sets every catalog env var of e to its marker + envHostile.
// SMTP_HOST / SMTP_PORT stay sane: they are endpoint coordinates built into
// URLs, not secrets, and a hostile host would make every URL unparseable.
func hostileOverrides(e Entry) map[string]string {
	out := make(map[string]string, len(e.Env)+2)
	for _, ev := range e.Env {
		out[ev.Name] = envMarker(ev.Name) + envHostile
	}
	out["SMTP_HOST"] = "smtp.example.com"
	out["SMTP_PORT"] = "587"
	out["SMTP_USER"] = envMarker("SMTP_USER") + envHostile
	out["SMTP_PASSWORD"] = envMarker("SMTP_PASSWORD") + envHostile
	return out
}

// collectScalars returns every scalar value in the parsed compose document.
func collectScalars(n *yaml.Node, out *[]string) {
	if n.Kind == yaml.ScalarNode {
		*out = append(*out, n.Value)
	}
	for _, c := range n.Content {
		collectScalars(c, out)
	}
}

// TestRender_AllApps_EnvValuesReachContainerVerbatim renders every catalog app
// with a hostile value for each of its env vars (admin door and tenant door)
// and asserts each value reaches the container exactly: the render stays valid
// YAML, every scalar that carries a value holds it verbatim after compose
// interpolation, and a value embedded in a URL decodes back to itself.
func TestRender_AllApps_EnvValuesReachContainerVerbatim(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	if cat.Len() == 0 {
		t.Fatal("catalog loaded zero entries")
	}
	for _, e := range cat.All() {
		env, err := MaterialiseEnv(e, hostileOverrides(e))
		if err != nil {
			t.Errorf("%s MaterialiseEnv: %v", e.Slug, err)
			continue
		}
		ports := make(map[string]RuntimePort, len(e.Ports))
		for i, p := range e.Ports {
			ports[p.Name] = RuntimePort{HostPort: 20000 + i, ContainerPort: p.ContainerPort, BindInterface: "127.0.0.1", Protocol: p.Protocol}
		}
		for _, door := range []struct {
			name string
			h    *TenantHardening
		}{
			{"admin", nil},
			{"tenant", &TenantHardening{CgroupParent: "jabali-user-t.slice", PIDsLimit: 200}},
		} {
			out, err := Render(e, RenderParams{
				Slug: e.Slug, Name: "t", Domain: e.Slug + ".example.com",
				ImageChannel: e.ImageChannel, DataRoot: "/var/lib/jabali/docker-apps/" + e.Slug,
				CPULimit: "1.0", MemoryLimit: "1g", PIDsLimit: 200, Ports: ports, Env: env,
				TenantHardening: door.h,
			})
			if err != nil {
				t.Errorf("%s (%s door) Render with hostile env: %v", e.Slug, door.name, err)
				continue
			}
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
				t.Errorf("%s (%s door): invalid YAML: %v", e.Slug, door.name, err)
				continue
			}
			var scalars []string
			collectScalars(&doc, &scalars)
			for _, raw := range scalars {
				got, ierr := composeInterpolate(raw)
				if ierr != nil {
					if strings.Contains(raw, "<<") {
						t.Errorf("%s (%s door): compose rejects a scalar carrying an env value: %v\n  %q", e.Slug, door.name, ierr, raw)
					}
					continue
				}
				for _, ev := range e.Env {
					m := envMarker(ev.Name)
					if strings.Contains(got, m) && !strings.Contains(got, m+envHostile) {
						t.Errorf("%s (%s door): %s reached the container altered:\n  got  %q\n  want it to contain %q", e.Slug, door.name, ev.Name, got, m+envHostile)
					}
				}
				checkURLUserinfo(t, e.Slug+" ("+door.name+" door)", got)
			}
		}
	}
}

// checkURLUserinfo asserts that a value embedded in a URL's userinfo decodes
// back to exactly the marker + envHostile it was built from.
func checkURLUserinfo(t *testing.T, where, s string) {
	t.Helper()
	if !strings.Contains(s, "://") || !(strings.Contains(s, "<<") || strings.Contains(s, "%3C%3C")) {
		return
	}
	u, err := url.Parse(s)
	if err != nil {
		t.Errorf("%s: URL carrying an env value no longer parses: %v\n  %q", where, err, s)
		return
	}
	if u.User == nil {
		t.Errorf("%s: env value landed outside the URL userinfo: %q", where, s)
		return
	}
	parts := []string{u.User.Username()}
	if pw, ok := u.User.Password(); ok {
		parts = append(parts, pw)
	}
	for _, p := range parts {
		if strings.Contains(p, "<<") && !strings.HasSuffix(p, ">>"+envHostile) {
			t.Errorf("%s: URL userinfo decodes to %q, want marker + %q", where, p, envHostile)
		}
	}
}

var (
	// tmplCommentRe matches a template comment, which may span lines.
	tmplCommentRe = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	// silentActionRe matches actions that emit nothing themselves: control
	// flow and variable assignment. An .Env read there only picks a branch.
	silentActionRe = regexp.MustCompile(`\{\{-?\s*(?:if\b[^}]*|else(?:\s+if\b[^}]*)?|end|range\b[^}]*|\$\w+\s*:?=[^}]*)\s*-?\}\}`)
	// envQuotedLineRe is the one shape allowed to emit an env value: the q
	// action IS the whole scalar of a mapping entry.
	envQuotedLineRe = regexp.MustCompile(`^\s*(?:[A-Za-z0-9_.\-]+|\{\{ \$name \}\}): \{\{ q .*\}\}\s*$`)
)

// TestCatalogTemplates_EnvOnlyThroughQ is the guard that keeps the class from
// coming back: in every compose template, an env value (.Env or the range
// variable $val) is emitted only as `KEY: {{ q ... }}`, where q JSON-quotes it
// and doubles '$'. A raw substitution inside a quoted scalar, a shell script
// or a URL fails here with the file and line.
func TestCatalogTemplates_EnvOnlyThroughQ(t *testing.T) {
	cat, _ := LoadDir(repoCatalogDir(t))
	if cat.Len() == 0 {
		t.Fatal("catalog loaded zero entries")
	}
	for _, e := range cat.All() {
		src := tmplCommentRe.ReplaceAllStringFunc(e.ComposeTemplate(), func(c string) string {
			return strings.Repeat("\n", strings.Count(c, "\n"))
		})
		for i, line := range strings.Split(src, "\n") {
			rest := silentActionRe.ReplaceAllString(line, "")
			if !strings.Contains(rest, ".Env") && !strings.Contains(rest, "$val") {
				continue
			}
			value := rest
			if j := strings.Index(rest, ": {{ q "); j >= 0 {
				value = rest[j+2:]
			}
			if !envQuotedLineRe.MatchString(rest) || strings.Count(value, "{{") != 1 {
				t.Errorf("%s/compose.yml.tmpl:%d emits an env value outside `KEY: {{ q ... }}`:\n  %s", e.Slug, i+1, strings.TrimSpace(line))
			}
		}
	}
}
