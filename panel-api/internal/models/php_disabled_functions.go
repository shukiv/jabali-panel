package models

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Disabled PHP functions per hosting package (GH #1701, GH #402).
//
// A package's php_disabled_functions column is the php_admin_value
// [disable_functions] list its pools run with. NULL means the default: the
// GH #401 command-exec lockdown below. An admin can allow any lockdown
// function back or disable more functions. A tenant never edits it.

// PHPLockdownFunctions is the GH #401 command-exec lockdown, in the order the
// agent renders it. It must equal the agent's defaultDisableFunctions
// (TestPHPLockdownMatchesAgentDefault). panel-ui mirrors it in
// phpDisabledFunctions.ts (TestPHPLockdownTSInSync).
var PHPLockdownFunctions = []string{
	"exec", "passthru", "shell_exec", "system", "proc_open", "popen",
	"pcntl_exec", "pcntl_fork", "proc_nice", "dl",
}

// PHPDefenseExecFunctions are the functions PHP Defense (Snuffleupagus)
// 00-base.rules bans outright. When a package allows one of them, the agent
// gives its pools a copy of the PHP Defense rules without that ban, so the
// package decision is not silently overridden in enforce mode. Every other
// PHP Defense rule stays. The agent's phpDefenseExecFunctions is the
// authoritative copy (TestPHPLockdownMatchesAgentDefault).
var PHPDefenseExecFunctions = []string{
	"system", "exec", "shell_exec", "passthru", "popen", "proc_open", "pcntl_exec",
}

// maxPHPDisabledFunctions bounds the list so the pool-conf line stays sane.
const maxPHPDisabledFunctions = 200

// phpFunctionNameRE is a PHP function identifier. Names are lowercased first:
// PHP and Snuffleupagus both match builtin names case-insensitively, and one
// canonical form keeps comparisons and the agent's rule filter exact.
var phpFunctionNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)

// NormalizePHPDisabledFunctions validates a comma- or whitespace-separated
// list of PHP function names and returns its canonical form: lowercased,
// de-duplicated, lockdown functions first in lockdown order, then the rest
// sorted. An empty input returns "" (nothing disabled).
func NormalizePHPDisabledFunctions(raw string) (string, error) {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	set := map[string]bool{}
	for _, f := range fields {
		name := strings.ToLower(f)
		if !phpFunctionNameRE.MatchString(name) {
			return "", fmt.Errorf("%q is not a PHP function name", f)
		}
		set[name] = true
	}
	if len(set) > maxPHPDisabledFunctions {
		return "", fmt.Errorf("at most %d functions can be disabled", maxPHPDisabledFunctions)
	}
	return strings.Join(canonicalFunctionOrder(set), ","), nil
}

// canonicalFunctionOrder lists set's members with the lockdown functions
// first, in lockdown order, then the others alphabetically. A set equal to the
// lockdown therefore renders exactly as the agent's default line.
func canonicalFunctionOrder(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	lock := map[string]bool{}
	for _, f := range PHPLockdownFunctions {
		lock[f] = true
		if set[f] {
			out = append(out, f)
		}
	}
	var rest []string
	for f := range set {
		if !lock[f] {
			rest = append(rest, f)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// EffectivePHPDisabledFunctions is the disabled-functions list the package's
// pools run with, in canonical order. A nil package gets the lockdown. A
// package with no list of its own follows php_exec_enabled: rows written
// before the list existed (or by an older binary) keep their opt-out.
func EffectivePHPDisabledFunctions(p *HostingPackage) []string {
	if p == nil {
		return append([]string(nil), PHPLockdownFunctions...)
	}
	if p.PHPDisabledFunctions == nil {
		if p.PHPExecEnabled {
			return []string{}
		}
		return append([]string(nil), PHPLockdownFunctions...)
	}
	norm, err := NormalizePHPDisabledFunctions(*p.PHPDisabledFunctions)
	if err != nil {
		// A stored value is normalized on write, so this is a hand edit. Fail
		// closed to the lockdown rather than guess which names were meant.
		return append([]string(nil), PHPLockdownFunctions...)
	}
	if norm == "" {
		return []string{}
	}
	return strings.Split(norm, ",")
}

// PHPExecAllowed reports whether a disabled-functions list leaves every
// lockdown function enabled: the meaning php_exec_enabled has always had. It
// is stored alongside the list so an older binary reading the row still sees
// the right opt-out.
func PHPExecAllowed(disabled []string) bool {
	set := map[string]bool{}
	for _, f := range disabled {
		set[f] = true
	}
	for _, f := range PHPLockdownFunctions {
		if set[f] {
			return false
		}
	}
	return true
}

// SetPHPExecOnList applies a php_exec_enabled toggle to a disabled list:
// allowing removes every lockdown function, forbidding adds them back. Other
// functions on the list stay. It returns nil (follow the default) when the
// result is exactly the lockdown.
func SetPHPExecOnList(current []string, allow bool) *string {
	set := map[string]bool{}
	for _, f := range current {
		set[f] = true
	}
	for _, f := range PHPLockdownFunctions {
		if allow {
			delete(set, f)
		} else {
			set[f] = true
		}
	}
	if isLockdownSet(set) {
		return nil
	}
	s := strings.Join(canonicalFunctionOrder(set), ",")
	return &s
}

func isLockdownSet(set map[string]bool) bool {
	if len(set) != len(PHPLockdownFunctions) {
		return false
	}
	for _, f := range PHPLockdownFunctions {
		if !set[f] {
			return false
		}
	}
	return true
}

// PoolDisableFunctions is the disable_functions value a pool apply sends the
// agent for a package: nil = omit the key, so the agent applies its own
// lockdown default. The agent derives the PHP Defense lift from the same list
// (the PHP Defense exec functions it leaves enabled), so the two never
// disagree. Every php.pool.apply caller goes through here so no path
// re-applies the lockdown to an opted-out package (GH #1422).
func PoolDisableFunctions(p *HostingPackage) *string {
	eff := EffectivePHPDisabledFunctions(p)
	set := map[string]bool{}
	for _, f := range eff {
		set[f] = true
	}
	if isLockdownSet(set) {
		return nil
	}
	s := strings.Join(eff, ",")
	return &s
}

// PHPDisabledFunctionsField turns a normalized list into the stored column
// value: nil when it is exactly the lockdown, so the package keeps following
// the default.
func PHPDisabledFunctionsField(norm string) *string {
	set := map[string]bool{}
	if norm != "" {
		for _, f := range strings.Split(norm, ",") {
			set[f] = true
		}
	}
	if isLockdownSet(set) {
		return nil
	}
	return &norm
}

// SetPHPDisabledFunctions stores a disabled-functions list (nil = the
// lockdown default) together with the php_exec_enabled it implies. Every
// writer goes through here so the two columns never disagree.
func (p *HostingPackage) SetPHPDisabledFunctions(list *string) {
	p.PHPDisabledFunctions = list
	p.PHPExecEnabled = false // a nil list now means the lockdown
	p.PHPExecEnabled = PHPExecAllowed(EffectivePHPDisabledFunctions(p))
}
