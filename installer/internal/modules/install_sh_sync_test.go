package modules

// install.sh is the engine behind this registry. Every key the TUI offers must
// install something there, and the Requires rules must hold for a headless
// JABALI_MODULES as well: install.sh applies those itself, because the TUI's
// Resolve never sees a preset selection.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func readInstallSh(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	return string(b)
}

// shellFunc returns the body of the top-level shell function name.
func shellFunc(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "\n"+name+"() {\n")
	if start < 0 {
		t.Fatalf("install.sh has no %s()", name)
	}
	body := src[start+1:]
	end := strings.Index(body, "\n}\n")
	if end < 0 {
		t.Fatalf("install.sh: %s() is not closed", name)
	}
	return body[:end]
}

// GH #2056: mail's install stops without the DNS module.
func TestRegistry_MailRequiresDNS(t *testing.T) {
	m, ok := byKey("mail")
	if !ok {
		t.Fatal("no mail module")
	}
	for _, r := range m.Requires {
		if r == "dns" {
			return
		}
	}
	t.Errorf("mail Requires %v, want dns", m.Requires)
}

func TestResolve_MailPullsDNS(t *testing.T) {
	got := keysOf(Resolve(map[string]bool{"mail": true}, "mail"))
	if !eq(got, []string{"dns", "mail"}) {
		t.Errorf("mail should pull dns, got %v", got)
	}
}

// A module the TUI offers but install.sh never acts on is a checkbox that does
// nothing: Docker, Docker apps and PostgreSQL were that, Full profile included.
// Each key is either installed by main() (run_if_module, or run_if_mail for
// mail) or is a panel switch only, seeded by seed_module_flags (api).
func TestRegistry_EveryModuleInstallsSomething(t *testing.T) {
	src := readInstallSh(t)
	main := shellFunc(t, src, "main")
	seeded := map[string]bool{}
	for _, m := range regexp.MustCompile(`"[a-z_]+_enabled:([a-z_]+)"`).FindAllStringSubmatch(shellFunc(t, src, "seed_module_flags"), -1) {
		seeded[m[1]] = true
	}
	for _, m := range Registry {
		installs := regexp.MustCompile(`(?m)^\s*run_if_module ` + regexp.QuoteMeta(m.Key) + ` `).MatchString(main)
		if m.Key == "mail" {
			installs = installs || regexp.MustCompile(`(?m)^\s*run_if_mail `).MatchString(main)
		}
		if !installs && !seeded[m.Key] {
			t.Errorf("module %q is offered by the installer, but install.sh's main() never installs it and seed_module_flags doesn't seed it", m.Key)
		}
	}
}

// install.sh's MODULE_REQUIRES is the same rule set as the registry's Requires.
func TestInstallSh_RequirementsMatchRegistry(t *testing.T) {
	src := readInstallSh(t)
	line := regexp.MustCompile(`(?m)^MODULE_REQUIRES=\(([^)]*)\)$`).FindStringSubmatch(src)
	if line == nil {
		t.Fatal("install.sh has no MODULE_REQUIRES=(…) line")
	}
	var got []string
	for _, tok := range strings.Fields(line[1]) {
		got = append(got, strings.Trim(tok, `"`))
	}
	var want []string
	for _, m := range Registry {
		for _, r := range m.Requires {
			want = append(want, m.Key+":"+r)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !eq(got, want) {
		t.Errorf("install.sh MODULE_REQUIRES %v, registry Requires %v", got, want)
	}
}
