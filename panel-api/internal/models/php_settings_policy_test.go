package models

import (
	"os"
	"strings"
	"testing"
)

func TestNormalizePHPSettingsPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, in, want, errHas string
	}{
		{name: "empty", in: "", want: ""},
		{name: "empty object", in: " {} ", want: ""},
		{name: "canonical sorted", in: `{"post_max_size":"tenant_allowed","memory_limit":"admin_only"}`,
			want: `{"memory_limit":"admin_only","post_max_size":"tenant_allowed"}`},
		{name: "sensitive privileged opt-in", in: `{"open_basedir":"tenant_privileged"}`, want: `{"open_basedir":"tenant_privileged"}`},
		{name: "not an object", in: `["memory_limit"]`, errHas: "JSON object"},
		{name: "unknown directive", in: `{"extension":"tenant_allowed"}`, errHas: `unknown PHP directive "extension"`},
		{name: "sensitive cannot be tenant_allowed", in: `{"disable_functions":"tenant_allowed"}`, errHas: `"disable_functions" is security-sensitive`},
		{name: "standard cannot be privileged", in: `{"memory_limit":"tenant_privileged"}`, errHas: `"memory_limit" level must be admin_only or tenant_allowed`},
		{name: "unknown level", in: `{"memory_limit":"everyone"}`, errHas: `"memory_limit"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePHPSettingsPolicy(tc.in)
			if tc.errHas != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errHas) {
					t.Fatalf("err = %v, want one containing %q", err, tc.errHas)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// The read side fails closed, so a row edited by hand (or written before a
// rule tightened) can never widen what a tenant may set.
func TestPHPSettingLevelFor_FailsClosed(t *testing.T) {
	pkg := &HostingPackage{PHPSettingsPolicy: `{"memory_limit":"admin_only","disable_functions":"tenant_allowed","open_basedir":"tenant_privileged","post_max_size":"everyone","max_input_vars":"tenant_privileged"}`}
	for directive, want := range map[string]PHPSettingLevel{
		"memory_limit":        PHPSettingAdminOnly,
		"upload_max_filesize": PHPSettingTenantAllowed, // missing key: default
		"disable_functions":   PHPSettingAdminOnly,     // floor: sensitive never tenant_allowed
		"open_basedir":        PHPSettingTenantPrivileged,
		"include_path":        PHPSettingAdminOnly, // sensitive, not set
		"post_max_size":       PHPSettingAdminOnly, // unknown level
		"max_input_vars":      PHPSettingAdminOnly, // privileged is not a standard level
		"extension":           PHPSettingAdminOnly, // outside the catalog
	} {
		if got := pkg.PHPSettingLevelFor(directive); got != want {
			t.Errorf("%s = %q, want %q", directive, got, want)
		}
	}
	corrupt := &HostingPackage{PHPSettingsPolicy: `{broken`}
	if got := corrupt.PHPSettingLevelFor("memory_limit"); got != PHPSettingAdminOnly {
		t.Errorf("corrupt policy: memory_limit = %q, want admin_only", got)
	}
}

// An account with no package: standard directives keep today's tenant access,
// sensitive ones stay admin-only (GH #282: privileged features deny).
func TestPHPSettingLevelFor_NoPackage(t *testing.T) {
	var pkg *HostingPackage
	for _, d := range PHPSettingCatalog {
		if got := pkg.PHPSettingLevelFor(d.Directive); got != PHPSettingTenantAllowed {
			t.Errorf("no package: %s = %q, want tenant_allowed", d.Directive, got)
		}
	}
	for _, d := range PHPSensitiveDirectives {
		if got := pkg.PHPSettingLevelFor(d); got != PHPSettingAdminOnly {
			t.Errorf("no package: %s = %q, want admin_only", d, got)
		}
	}
}

// No catalog directive may be one of the sensitive ones: the catalog's
// tenant_allowed default would otherwise bypass the floor.
func TestPHPSettingCatalog_HoldsNoSensitiveDirective(t *testing.T) {
	for _, d := range PHPSettingCatalog {
		for _, s := range PHPSensitiveDirectives {
			if d.Directive == s {
				t.Fatalf("%s is security-sensitive and must not be in the standard catalog", s)
			}
		}
		if d.Class != PHPSettingStandard || !validPHPSettingLevel(d.Class, d.Default) {
			t.Fatalf("%s: class %q default %q is not a valid standard entry", d.Directive, d.Class, d.Default)
		}
	}
}

// Cross-boundary contract test: panel-ui's package editor and domain panel
// render one row per catalog directive from PHP_SETTING_DIRECTIVES. A directive
// added here but not there would be governed by a policy the admin cannot see.
func TestPHPSettingCatalogTSInSync(t *testing.T) {
	const tsPath = "../../../panel-ui/src/components/packages/phpSettingsPolicy.ts"
	data, err := os.ReadFile(tsPath)
	if err != nil {
		t.Skipf("panel-ui catalog not readable (%v) — skipping cross-boundary check", err)
	}
	content := string(data)
	var catalog []string
	for _, d := range PHPSettingCatalog {
		catalog = append(catalog, d.Directive)
	}
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"PHP_SETTING_DIRECTIVES", catalog},
		{"PHP_SENSITIVE_DOMAIN_DIRECTIVES", PHPDomainSensitiveDirectives},
	} {
		start := strings.Index(content, "export const "+tc.name+" =")
		if start < 0 {
			t.Fatalf("phpSettingsPolicy.ts does not define %s", tc.name)
		}
		end := strings.Index(content[start:], "]")
		if end < 0 {
			t.Fatalf("%s is not an array literal", tc.name)
		}
		body := content[start : start+end]
		var got []string
		for _, f := range strings.FieldsFunc(body, func(r rune) bool { return r == ',' || r == '\n' || r == '[' }) {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, `"`) {
				got = append(got, strings.Trim(f, `"`))
			}
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("panel-ui %s = %v, want the Go list in order %v", tc.name, got, tc.want)
		}
	}
}

// The sensitive directives a domain can set are a subset of the sensitive
// set, so they keep its admin_only floor, and a package that says nothing
// leaves them admin-only (GH #1701 slice 3).
func TestPHPDomainSensitiveDirectives(t *testing.T) {
	sensitive := map[string]bool{}
	for _, d := range PHPSensitiveDirectives {
		sensitive[d] = true
	}
	var pkg *HostingPackage
	resolved := (&HostingPackage{}).ResolvedPHPSettingsPolicy()
	for _, d := range PHPDomainSensitiveDirectives {
		if !sensitive[d] {
			t.Fatalf("%s is not in PHPSensitiveDirectives", d)
		}
		if got := pkg.PHPSettingLevelFor(d); got != PHPSettingAdminOnly {
			t.Errorf("no package: %s = %q, want admin_only", d, got)
		}
		if got, ok := resolved[d]; !ok || got != PHPSettingAdminOnly {
			t.Errorf("resolved policy %s = %q (present %v), want admin_only", d, got, ok)
		}
	}
	if got := len(PHPPolicyDirectives()); got != len(PHPSettingCatalog)+len(PHPDomainSensitiveDirectives) {
		t.Fatalf("PHPPolicyDirectives has %d entries", got)
	}
}
