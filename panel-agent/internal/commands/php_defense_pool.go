package commands

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Per-pool PHP Defense rules (GH #1701).
//
// PHP Defense (Snuffleupagus) loads /etc/jabali/snuffleupagus/active.rules in
// every PHP-FPM master, and 00-base.rules there bans system, exec, shell_exec,
// passthru, popen, proc_open and pcntl_exec outright. In enforce mode that ban
// silently overrode a hosting package that allows those functions (the GH #402
// php_exec opt-out): the pool's disable_functions line was gone, yet the call
// still aborted.
//
// So a pool whose disable_functions list leaves any of those functions enabled
// loads its own copy of active.rules without the bans on them. Every other
// rule stays, including the eval blacklist and the .php-write block. The copy
// is pointed at by an ini in the pool's own scan dir: fpm-exec adds
// /etc/php/<ver>/jabali-ext/<slug> to PHP_INI_SCAN_DIR after the FPM conf.d,
// so its sp.configuration_file wins over the server-wide one for that master
// only. Box-verified on PHP 8.4 in enforce: the copy is honoured after a USR2
// reload, and a ban kept in it still aborts. CLI PHP (cron, SSH) keeps
// active.rules through cli.ini.

// phpDefenseExecFunctions are the PHP Defense bans a hosting package can lift
// for its pools. Nothing else is ever removed from a pool's copy. Kept in step
// with models.PHPDefenseExecFunctions (TestPHPLockdownMatchesAgentDefault).
var phpDefenseExecFunctions = map[string]bool{
	"system": true, "exec": true, "shell_exec": true, "passthru": true,
	"popen": true, "proc_open": true, "pcntl_exec": true,
}

// Paths, vars so tests can point them at a temp dir.
var (
	phpDefenseActiveRulesPath = snuffleupagusActiveRulesPath
	phpDefensePoolRulesDir    = "/etc/jabali/snuffleupagus/pools"
	phpEtcRoot                = "/etc/php"
)

// phpDefensePoolIniName is the per-slug ini that points the master at its copy.
// 90- so it sorts after anything else in the slug's scan dir.
const phpDefensePoolIniName = "90-jabali-php-defense.ini"

// phpDefenseAllowHeader records, inside each copy, which bans it lifts, so a
// PHP Defense rules change can rebuild the copy without the panel.
const phpDefenseAllowHeader = "# jabali-php-defense-allow: "

// phpFunctionNameRE bounds one disable_functions entry. The panel normalizes
// to this form; the agent refuses anything else so a value can never carry
// more than function names into the pool conf.
var phpFunctionNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)

// validateDisableFunctions checks a comma-separated disable_functions value.
// "" (nothing disabled) is valid.
func validateDisableFunctions(v string) error {
	if v == "" {
		return nil
	}
	for _, f := range strings.Split(v, ",") {
		if !phpFunctionNameRE.MatchString(f) {
			return fmt.Errorf("disable_functions: %q is not a lowercase PHP function name", f)
		}
	}
	return nil
}

// phpDefenseAllowFromDisabled returns the PHP Defense bans a disable_functions
// value leaves enabled: the functions the pool's package allows. Sorted.
func phpDefenseAllowFromDisabled(disable string) []string {
	disabled := map[string]bool{}
	for _, f := range strings.Split(disable, ",") {
		disabled[f] = true
	}
	var allow []string
	for f := range phpDefenseExecFunctions {
		if !disabled[f] {
			allow = append(allow, f)
		}
	}
	sort.Strings(allow)
	return allow
}

// phpDefenseDropLineRE matches one unconditional PHP Defense ban, in enforce
// (.drop();) or simulation (.drop().simulation();) form. Rules with filters
// (.param, .filename, ...) never match, so only a flat ban is ever lifted.
var phpDefenseDropLineRE = regexp.MustCompile(`^sp\.disable_function\.function\("([a-z_][a-z0-9_]*)"\)\.drop\(\)(?:\.simulation\(\))?;\s*$`)

// renderPoolPHPDefenseRules is active.rules without the bans on allow, which
// are kept as comments so the copy shows what it lifted.
func renderPoolPHPDefenseRules(active []byte, allow []string) []byte {
	lift := map[string]bool{}
	for _, f := range allow {
		if phpDefenseExecFunctions[f] {
			lift[f] = true
		}
	}
	var b bytes.Buffer
	b.WriteString("# Jabali PHP Defense rules for one PHP pool -- RENDERED by the agent, do not edit.\n")
	b.WriteString(phpDefenseAllowHeader + strings.Join(allow, ",") + "\n")
	b.WriteString("# A copy of active.rules without the bans on the functions this pool's\n")
	b.WriteString("# hosting package allows (GH #1701). Rebuilt on every pool apply and on\n")
	b.WriteString("# every PHP Defense rules change.\n")
	for _, line := range strings.Split(strings.TrimSuffix(string(active), "\n"), "\n") {
		if m := phpDefenseDropLineRE.FindStringSubmatch(line); m != nil && lift[m[1]] {
			b.WriteString("# allowed by the hosting package: " + line + "\n")
			continue
		}
		b.WriteString(line + "\n")
	}
	return b.Bytes()
}

func phpDefensePoolRulesPath(slug string) string {
	return filepath.Join(phpDefensePoolRulesDir, slug+".rules")
}

func phpDefensePoolIniPath(version, slug string) string {
	return filepath.Join(phpEtcRoot, version, "jabali-ext", slug, phpDefensePoolIniName)
}

// writePHPDefenseFile writes content atomically unless the file already holds it,
// reporting whether it wrote.
func writePHPDefenseFile(path string, content []byte) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := writeFileAtomic(path, content, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// removePHPDefenseFile removes path, reporting whether it was there.
func removePHPDefenseFile(path string) (bool, error) {
	err := os.Remove(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// applyPoolPHPDefense gives the slug's master its own PHP Defense rules when
// allow is non-empty, and removes them otherwise. Call it before the pool's
// reload so the master picks it up. It returns whether anything changed.
func applyPoolPHPDefense(version, slug string, allow []string) (bool, error) {
	changed := false
	// An ini left in another version's scan dir (the pool moved versions)
	// would apply again if the slug ever moved back with a different list.
	others, _ := filepath.Glob(filepath.Join(phpEtcRoot, "*", "jabali-ext", slug, phpDefensePoolIniName))
	for _, p := range others {
		if p == phpDefensePoolIniPath(version, slug) {
			continue
		}
		if ok, err := removePHPDefenseFile(p); err != nil {
			return changed, fmt.Errorf("remove stale PHP Defense ini: %w", err)
		} else if ok {
			changed = true
		}
	}

	active, readErr := os.ReadFile(phpDefenseActiveRulesPath)
	if len(allow) == 0 || os.IsNotExist(readErr) {
		// Nothing to lift, or PHP Defense is not installed: the master loads
		// the server-wide rules (or none).
		for _, p := range []string{phpDefensePoolIniPath(version, slug), phpDefensePoolRulesPath(slug)} {
			if ok, err := removePHPDefenseFile(p); err != nil {
				return changed, fmt.Errorf("remove pool PHP Defense rules: %w", err)
			} else if ok {
				changed = true
			}
		}
		return changed, nil
	}
	if readErr != nil {
		return changed, fmt.Errorf("read PHP Defense rules: %w", readErr)
	}

	rulesPath := phpDefensePoolRulesPath(slug)
	wrote, err := writePHPDefenseFile(rulesPath, renderPoolPHPDefenseRules(active, allow))
	if err != nil {
		return changed, fmt.Errorf("write pool PHP Defense rules: %w", err)
	}
	changed = changed || wrote
	ini := "; Jabali PHP Defense: this pool's hosting package allows " + strings.Join(allow, ", ") + ",\n" +
		"; so it loads its own copy of the rules without those bans (GH #1701).\n" +
		"; RENDERED by the agent, do not edit.\n" +
		"sp.configuration_file=" + rulesPath + "\n"
	wrote, err = writePHPDefenseFile(phpDefensePoolIniPath(version, slug), []byte(ini))
	if err != nil {
		return changed, fmt.Errorf("write pool PHP Defense ini: %w", err)
	}
	return changed || wrote, nil
}

// removePoolPHPDefense deletes a slug's PHP Defense copy and its ini in every
// version. Best-effort, for pool removal and the orphan reaper.
func removePoolPHPDefense(slug string) {
	if !phpPoolSlugRegex.MatchString(slug) || strings.Contains(slug, "..") {
		return
	}
	_ = os.Remove(phpDefensePoolRulesPath(slug))
	inis, _ := filepath.Glob(filepath.Join(phpEtcRoot, "*", "jabali-ext", slug, phpDefensePoolIniName))
	for _, p := range inis {
		_ = os.Remove(p)
	}
}

// regeneratePoolPHPDefenseRules rebuilds every pool's copy from the current
// active.rules, so a PHP Defense mode or rule change reaches pools with their
// own copy too. A copy whose pool no longer exists is removed instead of being
// rewritten forever. Call it after writing active.rules and before the reload.
func regeneratePoolPHPDefenseRules() error {
	active, err := os.ReadFile(phpDefenseActiveRulesPath)
	if err != nil {
		return fmt.Errorf("read PHP Defense rules: %w", err)
	}
	copies, _ := filepath.Glob(filepath.Join(phpDefensePoolRulesDir, "*.rules"))
	var failed []string
	for _, path := range copies {
		slug := strings.TrimSuffix(filepath.Base(path), ".rules")
		if !phpPoolSlugRegex.MatchString(slug) || strings.Contains(slug, "..") {
			continue
		}
		if confs, _ := filepath.Glob(filepath.Join(phpEtcRoot, "*", "fpm", "pool.d", "jabali-"+slug+".conf")); len(confs) == 0 {
			removePoolPHPDefense(slug)
			continue
		}
		cur, err := os.ReadFile(path)
		if err != nil {
			failed = append(failed, slug)
			continue
		}
		allow := readPHPDefenseAllowHeader(cur)
		if len(allow) == 0 {
			// Not a copy this agent wrote: leave it alone rather than guess.
			continue
		}
		if _, err := writePHPDefenseFile(path, renderPoolPHPDefenseRules(active, allow)); err != nil {
			slog.Warn("php defense: rebuild pool rules", "slug", slug, "err", err)
			failed = append(failed, slug)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("PHP Defense rules not rebuilt for pool(s): %s", strings.Join(failed, ", "))
	}
	return nil
}

// readPHPDefenseAllowHeader returns the bans a copy lifts, from its header,
// keeping only functions a package may lift.
func readPHPDefenseAllowHeader(rules []byte) []string {
	for _, line := range strings.Split(string(rules), "\n") {
		if !strings.HasPrefix(line, phpDefenseAllowHeader) {
			continue
		}
		var allow []string
		for _, f := range strings.Split(strings.TrimPrefix(line, phpDefenseAllowHeader), ",") {
			if phpDefenseExecFunctions[f] {
				allow = append(allow, f)
			}
		}
		return allow
	}
	return nil
}
