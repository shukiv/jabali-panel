package models

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// PHP settings policy (GH #1701 slice 1). Each hosting package says, per
// php.ini directive, who may set that directive on a domain's PHP Settings
// page: only an admin, or the tenant too. The admin always gets the whole
// surface; a tenant gets the subset their package permits. Enforced in the
// shared GET/PATCH /domains/:id/php-settings handler, which serves both.

// PHPSettingLevel is who may set one PHP directive on a domain.
type PHPSettingLevel string

const (
	// PHPSettingAdminOnly: only an admin may set the directive.
	PHPSettingAdminOnly PHPSettingLevel = "admin_only"
	// PHPSettingTenantAllowed: the domain owner may set it too. Valid for
	// standard directives only.
	PHPSettingTenantAllowed PHPSettingLevel = "tenant_allowed"
	// PHPSettingTenantPrivileged: the explicit opt-in that lets a tenant set a
	// security-sensitive directive, still server-validated. Valid for
	// sensitive directives only.
	PHPSettingTenantPrivileged PHPSettingLevel = "tenant_privileged"
)

// PHPSettingClass separates directives a tenant may be allowed to set from
// the security-sensitive ones pinned admin-only unless privileged.
type PHPSettingClass string

const (
	PHPSettingStandard  PHPSettingClass = "standard"
	PHPSettingSensitive PHPSettingClass = "sensitive"
)

// PHPSettingDef is one directive a package policy governs.
type PHPSettingDef struct {
	Directive string
	Class     PHPSettingClass
	// Default applies when the package sets no level for the directive. The
	// standard directives default to tenant_allowed because tenants could
	// already set every one of them before policies existed: a policy only
	// ever restricts that.
	Default PHPSettingLevel
}

// PHPSettingCatalog lists the per-domain PHP settings a package policy
// governs, in display order. These are the directives the per-domain PHP
// Settings page writes today. The PHP version is not here: a tenant also sets
// it through the per-domain pool binding, so gating it on this page alone
// would not hold. panel-ui mirrors this list in phpSettingsPolicy.ts (kept in
// step by TestPHPSettingCatalogTSInSync).
var PHPSettingCatalog = []PHPSettingDef{
	{"memory_limit", PHPSettingStandard, PHPSettingTenantAllowed},
	{"upload_max_filesize", PHPSettingStandard, PHPSettingTenantAllowed},
	{"post_max_size", PHPSettingStandard, PHPSettingTenantAllowed},
	{"max_input_vars", PHPSettingStandard, PHPSettingTenantAllowed},
	{"max_execution_time", PHPSettingStandard, PHPSettingTenantAllowed},
	{"max_input_time", PHPSettingStandard, PHPSettingTenantAllowed},
	{"display_errors", PHPSettingStandard, PHPSettingTenantAllowed},
	{"error_reporting", PHPSettingStandard, PHPSettingTenantAllowed},
	{"date.timezone", PHPSettingStandard, PHPSettingTenantAllowed},
}

// PHPSensitiveDirectives are pinned admin-only whatever a policy says. A
// tenant can set one only when the package grants tenant_privileged, and
// only through a server-validated value (GH #1701 slice 3). Setting any of
// these through PHP_VALUE is a sandbox escape or an injection vector
// (feedback: fastcgi_param PHP_ADMIN_VALUE jailbreak; sendmail argument
// injection through mail.force_extra_parameters).
var PHPSensitiveDirectives = []string{
	"open_basedir",
	"disable_functions",
	"mail.force_extra_parameters",
	"allow_url_fopen",
	"include_path",
	"session.save_path",
}

func phpSettingClass(directive string) (PHPSettingClass, PHPSettingLevel, bool) {
	for _, d := range PHPSettingCatalog {
		if d.Directive == directive {
			return d.Class, d.Default, true
		}
	}
	for _, d := range PHPSensitiveDirectives {
		if d == directive {
			return PHPSettingSensitive, PHPSettingAdminOnly, true
		}
	}
	return "", "", false
}

// validPHPSettingLevel reports whether a level may be stored for a class.
func validPHPSettingLevel(class PHPSettingClass, level PHPSettingLevel) bool {
	switch class {
	case PHPSettingStandard:
		return level == PHPSettingAdminOnly || level == PHPSettingTenantAllowed
	case PHPSettingSensitive:
		return level == PHPSettingAdminOnly || level == PHPSettingTenantPrivileged
	}
	return false
}

// NormalizePHPSettingsPolicy validates an admin-supplied policy (a JSON object
// of directive -> level) and returns its canonical form: keys sorted, "" when
// empty. Unknown directives, and a level that does not fit the directive's
// class (tenant_allowed on a sensitive directive, tenant_privileged on a
// standard one), are refused with the directive named.
func NormalizePHPSettingsPolicy(in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", nil
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(in), &raw); err != nil {
		return "", fmt.Errorf("php_settings_policy must be a JSON object of directive to level: %w", err)
	}
	if len(raw) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		class, _, ok := phpSettingClass(k)
		if !ok {
			return "", fmt.Errorf("php_settings_policy: unknown PHP directive %q", k)
		}
		level := PHPSettingLevel(raw[k])
		if !validPHPSettingLevel(class, level) {
			if class == PHPSettingSensitive {
				return "", fmt.Errorf("php_settings_policy: %q is security-sensitive; its level must be admin_only or tenant_privileged, not %q", k, raw[k])
			}
			return "", fmt.Errorf("php_settings_policy: %q level must be admin_only or tenant_allowed, not %q", k, raw[k])
		}
	}
	b, err := json.Marshal(raw) // encoding/json writes map keys sorted
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// PHPSettingLevelFor returns who may set a directive on domains of a package.
// It fails closed: a directive outside the catalog and the sensitive set, a
// stored level that is not valid for the directive's class, and a policy that
// does not parse all give admin_only. A nil package (an account with no
// package, GH #282) gets the catalog defaults for standard directives and
// admin_only for sensitive ones.
func (p *HostingPackage) PHPSettingLevelFor(directive string) PHPSettingLevel {
	class, def, ok := phpSettingClass(directive)
	if !ok {
		return PHPSettingAdminOnly
	}
	if p == nil {
		return def
	}
	policy := strings.TrimSpace(p.PHPSettingsPolicy)
	if policy == "" {
		return def
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(policy), &raw); err != nil {
		return PHPSettingAdminOnly
	}
	stored, set := raw[directive]
	if !set {
		return def
	}
	level := PHPSettingLevel(stored)
	if !validPHPSettingLevel(class, level) {
		return PHPSettingAdminOnly
	}
	return level
}

// TenantMaySet reports whether a tenant may set a directive at this level.
func (l PHPSettingLevel) TenantMaySet() bool {
	return l == PHPSettingTenantAllowed || l == PHPSettingTenantPrivileged
}

// ResolvedPHPSettingsPolicy returns the effective level of every catalog
// directive for a package (nil = no package).
func (p *HostingPackage) ResolvedPHPSettingsPolicy() map[string]PHPSettingLevel {
	out := make(map[string]PHPSettingLevel, len(PHPSettingCatalog))
	for _, d := range PHPSettingCatalog {
		out[d.Directive] = p.PHPSettingLevelFor(d.Directive)
	}
	return out
}
