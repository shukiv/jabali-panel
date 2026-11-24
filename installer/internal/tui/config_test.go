package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestValidateConfig(t *testing.T) {
	f := newConfigFields("")
	// all required empty except seeded php → hostname missing
	if e := validateConfig(f, false); e == "" {
		t.Error("expected error for empty required hostname")
	}
	f[0].input.SetValue("panel.example.com")
	f[1].input.SetValue("admin@example.com")
	if e := validateConfig(f, false); e != "" {
		t.Errorf("valid config rejected: %s", e)
	}
	f[0].input.SetValue("not a hostname")
	if e := validateConfig(f, false); e == "" {
		t.Error("bad hostname accepted")
	}
}

func TestPHPSelectDefault(t *testing.T) {
	f := newConfigFields("h.example.com")
	f[1].input.SetValue("a@b.com")
	env := configEnv(f, false)
	var got string
	for _, e := range env {
		if len(e) >= 20 && e[:20] == "JABALI_PHP_VERSIONS=" {
			got = e[20:]
		}
	}
	if got != "8.4" {
		t.Errorf("default PHP env = %q, want 8.4", got)
	}
}

func TestConfigEnv_NSGatedOnDNSModule(t *testing.T) {
	f := newConfigFields("h.example.com")
	f[1].input.SetValue("a@b.com")
	f[3].input.SetValue("ns1.example.com")
	f[4].input.SetValue("ns2.example.com")

	// dns module off: the NS fields are hidden and never emitted —
	// install.sh falls back to its silent ns1/ns2.<hostname> placeholders
	// (GH #353 tester feedback: external-DNS installs must not ask for ns1/ns2).
	for _, e := range configEnv(f, false) {
		if strings.HasPrefix(e, "JABALI_NS1_NAME=") || strings.HasPrefix(e, "JABALI_NS2_NAME=") {
			t.Errorf("NS env emitted with dns module off: %v", e)
		}
	}

	// dns module on: both emitted.
	env := configEnv(f, true)
	var ns1, ns2 bool
	for _, e := range env {
		if e == "JABALI_NS1_NAME=ns1.example.com" {
			ns1 = true
		}
		if e == "JABALI_NS2_NAME=ns2.example.com" {
			ns2 = true
		}
	}
	if !ns1 || !ns2 {
		t.Errorf("NS env missing when dns on: %v", env)
	}
}

func TestVisibleFields_NSHiddenWhenDNSOff(t *testing.T) {
	f := newConfigFields("h.example.com")
	if got, want := len(visibleFields(f, false)), len(f)-2; got != want {
		t.Errorf("visible fields with dns off = %d, want %d (NS1/NS2 hidden)", got, want)
	}
	if got, want := len(visibleFields(f, true)), len(f); got != want {
		t.Errorf("visible fields with dns on = %d, want %d", got, want)
	}
}

// A panel hostname under an IDN TLD (punycode, e.g. .рф = xn--p1ai) was
// refused by a letters-only TLD rule; install.sh itself accepts it.
func TestHostnameRe_IDNTLD(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"panel.example.com", true},
		{"panel.example.xn--p1ai", true},
		{"panel.xn----7sbbhn.xn--4dbrk0ce", true},
		{"panel.example.xn--", false},
		{"panel.example.123", false},
	} {
		if got := hostnameRe.MatchString(tc.in); got != tc.want {
			t.Errorf("hostnameRe(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestAdminUsername: the admin login username defaults to the one the panel
// derives from the admin email, follows the email until the operator edits it,
// is validated with the panel's username rule, and reaches install.sh.
func TestAdminUsername(t *testing.T) {
	f := newConfigFields("panel.example.com")
	const email, user = 1, 2
	if f[user].env != "JABALI_ADMIN_USERNAME" || f[user].input.Value() != "admin" {
		t.Fatalf("username field %q = %q, want JABALI_ADMIN_USERNAME = admin", f[user].env, f[user].input.Value())
	}

	// Typing the email (through the config screen's key handling) moves it.
	m := Model{fields: f, focus: email, selected: map[string]bool{}}
	m.fields[email].input.SetValue("")
	m.fields[email].input.Focus()
	for _, r := range "Shuki.V@example.com" {
		u, _ := m.handleConfigKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = u.(Model)
	}
	if got := m.fields[user].input.Value(); got != "shuki_v" {
		t.Fatalf("username after typing the email = %q, want shuki_v", got)
	}

	// Once edited it stays.
	m.focus = user
	m.fields[user].input.SetValue("")
	m.fields[user].input.Focus()
	for _, r := range "ops" {
		u, _ := m.handleConfigKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = u.(Model)
	}
	m.focus = email
	u, _ := m.handleConfigKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = u.(Model)
	if got := m.fields[user].input.Value(); got != "ops" {
		t.Fatalf("an edited username changed with the email: %q", got)
	}

	found := false
	for _, e := range configEnv(m.fields, false) {
		found = found || e == "JABALI_ADMIN_USERNAME=ops"
	}
	if !found {
		t.Errorf("configEnv = %v, want JABALI_ADMIN_USERNAME=ops", configEnv(m.fields, false))
	}

	for v, ok := range map[string]bool{"ops": true, "_x-1": true, "Ops": false, "9lives": false, "a b": false, strings.Repeat("a", 33): false, "": false} {
		m.fields[user].input.SetValue(v)
		m.fields[email].input.SetValue("a@example.com")
		if got := validateConfig(m.fields, false) == ""; got != ok {
			t.Errorf("username %q valid = %v, want %v (%s)", v, got, ok, validateConfig(m.fields, false))
		}
	}

	for in, want := range map[string]string{"admin@x.com": "admin", "9a@x.com": "u9a", "-x-@x.com": "x", "Ü@x.com": "_"} {
		if got := deriveAdminUsername(in); got != want {
			t.Errorf("deriveAdminUsername(%q) = %q, want %q", in, got, want)
		}
	}
}
