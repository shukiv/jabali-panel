package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// configField is one text input on the config screen, plus how it maps to an
// install.sh environment variable and whether it only applies with DNS on.
type configField struct {
	env      string // install.sh env var this sets
	label    string
	ph       string // placeholder
	dnsOnly  bool   // only shown/collected when the dns module is enabled
	required bool
	// derive computes a default from the hostname; applied to this field as
	// long as the user hasn't edited it (touched). nil = no derivation.
	derive  func(host string) string
	touched bool
	input   textinput.Model

	// PHP version multi-select (phpSelect=true): available versions + which are
	// checked + the horizontal cursor. Same list the panel offers.
	phpSelect  bool
	phpVers    []string
	phpChecked map[string]bool
	phpCursor  int
}

// availablePHPVersions mirrors panel-agent SupportedPHPVersions / panel-api
// supportedPHPVersions — the versions Sury/jabali can install. Kept in sync
// manually; the installer runs before any panel is up so it can't query the API.
var availablePHPVersions = []string{"7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.5"}

var hostnameRe = regexp.MustCompile(`^([a-zA-Z0-9]([-a-zA-Z0-9]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// newConfigFields builds the ordered config inputs. The panel hostname is
// pre-seedable from the environment (JABALI_HOSTNAME) so a half-headless run
// can still tune modules interactively.
func newConfigFields(seedHostname string) []configField {
	mk := func(env, label, ph, def string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = ph
		ti.CharLimit = 253
		ti.Prompt = ""
		if def != "" {
			ti.SetValue(def)
		}
		return ti
	}
	fields := []configField{
		{env: "JABALI_HOSTNAME", label: "Panel hostname (FQDN)", ph: "panel.example.com", required: true, input: mk("JABALI_HOSTNAME", "", "panel.example.com", seedHostname)},
		{env: "JABALI_ADMIN_EMAIL", label: "Admin login email", ph: "admin@example.com", required: true,
			derive: func(h string) string { return "admin@" + h }, input: mk("", "", "admin@example.com", "")},
		{env: "JABALI_NS1_NAME", label: "Primary nameserver (DNS)", ph: "ns1.example.com", dnsOnly: true,
			derive: func(h string) string { return "ns1." + h }, input: mk("", "", "ns1.example.com", "")},
		{env: "JABALI_NS2_NAME", label: "Secondary nameserver (DNS)", ph: "ns2.example.com", dnsOnly: true,
			derive: func(h string) string { return "ns2." + h }, input: mk("", "", "ns2.example.com", "")},
		{env: "JABALI_PHP_VERSIONS", label: "PHP versions", phpSelect: true,
			phpVers: availablePHPVersions, phpChecked: map[string]bool{"8.4": true}, phpCursor: 5, input: mk("", "", "", "")},
	}
	// Seed the derived fields from any pre-supplied hostname.
	applyDerived(fields, seedHostname)
	return fields
}

// applyDerived fills every derivable, untouched field from the hostname. Called
// on New (seed) and after each hostname keystroke, so admin/NS default to the
// hostname unless the operator overrode them.
func applyDerived(fields []configField, host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	for i := range fields {
		if fields[i].derive != nil && !fields[i].touched {
			fields[i].input.SetValue(fields[i].derive(host))
		}
	}
}

// visibleFields returns the field indexes shown given whether DNS is enabled.
func visibleFields(fields []configField, dnsOn bool) []int {
	var idx []int
	for i, f := range fields {
		if f.dnsOnly && !dnsOn {
			continue
		}
		idx = append(idx, i)
	}
	return idx
}

// validateConfig checks required + format for the visible fields; returns the
// first error message, or "" when valid.
func validateConfig(fields []configField, dnsOn bool) string {
	for _, i := range visibleFields(fields, dnsOn) {
		f := fields[i]
		if f.phpSelect {
			if f.phpSelectedValue() == "" {
				return "select at least one PHP version"
			}
			continue
		}
		v := strings.TrimSpace(f.input.Value())
		if f.required && v == "" {
			return f.label + " is required"
		}
		switch f.env {
		case "JABALI_HOSTNAME", "JABALI_NS1_NAME", "JABALI_NS2_NAME":
			if v != "" && !hostnameRe.MatchString(v) {
				return f.label + ": not a valid hostname"
			}
		case "JABALI_ADMIN_EMAIL":
			if v != "" && !emailRe.MatchString(v) {
				return f.label + ": not a valid email"
			}
		}
	}
	return ""
}

// phpSelectedValue returns the checked PHP versions in list order, space-joined.
func (f configField) phpSelectedValue() string {
	var out []string
	for _, v := range f.phpVers {
		if f.phpChecked[v] {
			out = append(out, v)
		}
	}
	return strings.Join(out, " ")
}

// configEnv builds the install.sh env assignments from the visible fields.
func configEnv(fields []configField, dnsOn bool) []string {
	var env []string
	for _, i := range visibleFields(fields, dnsOn) {
		f := fields[i]
		var v string
		if f.phpSelect {
			v = f.phpSelectedValue()
		} else {
			v = strings.TrimSpace(f.input.Value())
		}
		if v != "" {
			env = append(env, f.env+"="+v)
		}
	}
	return env
}

// updateFocusedField routes a key message to the focused input.
func updateFocusedField(fields []configField, focus int, msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	fields[focus].input, cmd = fields[focus].input.Update(msg)
	return cmd
}
