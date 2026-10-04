package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// php.pool.effective (GH #1701) reports what one PHP pool really runs with,
// for the read-only part of a domain's PHP Settings page: the disabled
// functions, what PHP Defense blocks there, and include_path /
// session.save_path. It reads the files the pool's master loads (its pool
// conf, the FPM php.ini + conf.d + the pool's own scan dir, and the PHP Defense
// rules file it is pointed at), so it shows the configuration in force, not
// what the panel meant to write. It also reports which of those functions
// this PHP version's FPM build does not provide at all, so a function the
// package allows is not shown as callable when PHP-FPM has no such function.

type phpPoolEffectiveParams struct {
	PHPVersion string `json:"php_version"`
	Slug       string `json:"slug"`
}

// phpEffectiveFunction is one disabled function and where it is disabled:
// "pool" (the pool's disable_functions, from its hosting package) or
// "php.ini" (server-wide; a pool cannot enable it again).
type phpEffectiveFunction struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// phpDefenseFunction is one function PHP Defense bans for the pool:
// "blocked" (enforce) or "logged" (simulation: the call still runs).
type phpDefenseFunction struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type phpDefenseEffective struct {
	// Active: the PHP Defense module is installed and loaded for this PHP
	// version's FPM. When false nothing below applies.
	Active bool `json:"active"`
	// Mode of the rules the pool loads: enforce, simulation or off.
	Mode string `json:"mode"`
	// PoolRules: the pool loads its own copy (its package lifts some bans).
	PoolRules bool                 `json:"pool_rules"`
	Functions []phpDefenseFunction `json:"functions"`
}

// phpIniEffective is one ini value and where it comes from: "pool" (the pool
// conf sets it, from a pool ini override) or "php.ini".
type phpIniEffective struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

type phpPoolEffectiveResponse struct {
	PHPVersion        string                 `json:"php_version"`
	Slug              string                 `json:"slug"`
	PoolFound         bool                   `json:"pool_found"`
	DisabledFunctions []phpEffectiveFunction `json:"disabled_functions"`
	PHPDefense        phpDefenseEffective    `json:"php_defense"`
	IncludePath       phpIniEffective        `json:"include_path"`
	SessionSavePath   phpIniEffective        `json:"session_save_path"`
	// IniReadError is set when the php.ini read failed; the php.ini parts are
	// then missing rather than guessed.
	IniReadError string `json:"ini_read_error,omitempty"`
	// UnavailableFunctions are the reported functions (the lockdown list plus
	// every disabled or PHP Defense-banned name) that this PHP version's FPM
	// build does not provide, e.g. pcntl_* when FPM does not load pcntl, or
	// dl, which only PHP's command-line build registers.
	UnavailableFunctions []string `json:"unavailable_functions"`
	// AvailabilityError is set when that check could not run; the list is
	// then empty rather than guessed.
	AvailabilityError string `json:"availability_error,omitempty"`
	// ExecConfined (GH #2001) is true when the jabali-fpm-app AppArmor
	// profile, which every per-user PHP-FPM master runs under, is loaded in
	// enforce mode. PHP's program-starting functions (exec, shell_exec,
	// system, ...) can then start only the programs that profile allows (the
	// shell and cat), so `df`, `ls` or `grep` fail with "Permission denied"
	// even where the hosting package allows the function. False when the
	// profile is in complain mode, not loaded, or AppArmor is off.
	ExecConfined bool `json:"exec_confined"`
}

// fpmAppArmorProfile is the AppArmor profile the per-user PHP-FPM masters
// run under (install/apparmor/usr.local.libexec.jabali.fpm-exec).
const fpmAppArmorProfile = "jabali-fpm-app"

// fpmAppArmorMode returns the mode fpmAppArmorProfile is loaded in
// ("enforce", "complain", ...), or "" when AppArmor or the profile is not
// loaded or aa-status could not be read. A var so tests can stub it.
var fpmAppArmorMode = func(ctx context.Context) string {
	out, err := execCommandContext(ctx, "aa-status", "--json").Output()
	if err != nil {
		return ""
	}
	return aaStatusProfileMode(out, fpmAppArmorProfile)
}

// aaStatusProfileMode reads one profile's mode from `aa-status --json`
// output ({"profiles": {"<name>": "enforce|complain|..."}}).
func aaStatusProfileMode(aaStatusJSON []byte, profile string) string {
	var raw struct {
		Profiles map[string]string `json:"profiles"`
	}
	if json.Unmarshal(aaStatusJSON, &raw) != nil {
		return ""
	}
	return raw.Profiles[profile]
}

// appArmorModeConfines reports whether a profile mode blocks what the
// profile does not allow. complain only logs it.
func appArmorModeConfines(mode string) bool {
	return mode == "enforce" || mode == "kill"
}

// snuffleupagusLibRoot holds the per-minor snuffleupagus.so builds. A var so
// tests can point it at a temp dir.
var snuffleupagusLibRoot = "/usr/lib/php/jabali-snuffleupagus"

// phpEffectiveIniNames are the php.ini values the read reports.
var phpEffectiveIniNames = []string{"disable_functions", "include_path", "session.save_path"}

// phpEffectiveIniRead runs the version's PHP with the FPM php.ini, its conf.d
// and the pool's own scan dir (the layering fpm-exec gives the master) and
// returns the three ini values. A var so tests can stub it.
var phpEffectiveIniRead = func(ctx context.Context, version, slug string) (map[string]string, error) {
	namesJSON, _ := json.Marshal(phpEffectiveIniNames)
	script := "$d=[];foreach(json_decode('" + string(namesJSON) + "',true) as $k){$d[$k]=(string)ini_get($k);}echo json_encode($d);"
	cmd := execCommandContext(ctx, "php"+version, "-c", filepath.Join("/etc/php", version, "fpm", "php.ini"), "-r", script)
	cmd.Env = append(cmd.Environ(), phpEffectiveEnv(version, slug))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("php %s ini read failed: %v", version, err)
	}
	var vals map[string]string
	if err := json.Unmarshal(out, &vals); err != nil {
		return nil, fmt.Errorf("php ini output parse failed: %v", err)
	}
	return vals, nil
}

// phpEffectiveEnv is the ini layering fpm-exec gives the pool's master: the
// FPM conf.d plus the pool's own scan dir.
func phpEffectiveEnv(version, slug string) string {
	return "PHP_INI_SCAN_DIR=" + filepath.Join("/etc/php", version, "fpm", "conf.d") + ":" + filepath.Join("/etc/php", version, "jabali-ext", slug)
}

// phpFPMModules lists the modules the version's PHP-FPM loads with the pool's
// ini layering (`php-fpm -m`), lowercased. A var so tests can stub it.
var phpFPMModules = func(ctx context.Context, version, slug string) (map[string]bool, error) {
	cmd := execCommandContext(ctx, "/usr/sbin/php-fpm"+version, "-c", filepath.Join("/etc/php", version, "fpm", "php.ini"), "-m")
	cmd.Env = append(cmd.Environ(), phpEffectiveEnv(version, slug))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("php-fpm %s module list failed: %v", version, err)
	}
	mods := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		mods[strings.ToLower(line)] = true
	}
	if len(mods) == 0 {
		return nil, fmt.Errorf("php-fpm %s listed no modules", version)
	}
	return mods, nil
}

// phpFunctionExtensions maps each name to the extension that provides it in
// the version's PHP build ("" when the build has no such function), read with
// the CLI and the pool's ini layering. Names go in as arguments, never into
// the script. A var so tests can stub it.
var phpFunctionExtensions = func(ctx context.Context, version, slug string, names []string) (map[string]string, error) {
	script := "$o=[];foreach(array_slice($argv,1) as $n){$o[$n]=function_exists($n)?(string)(new ReflectionFunction($n))->getExtensionName():'';}echo json_encode((object)$o);"
	args := append([]string{"-c", filepath.Join("/etc/php", version, "fpm", "php.ini"), "-r", script, "--"}, names...)
	cmd := execCommandContext(ctx, "php"+version, args...)
	cmd.Env = append(cmd.Environ(), phpEffectiveEnv(version, slug))
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("php %s function read failed: %v", version, err)
	}
	exts := map[string]string{}
	if err := json.Unmarshal(out, &exts); err != nil {
		return nil, fmt.Errorf("php function read output parse failed: %v", err)
	}
	return exts, nil
}

// cliSAPIFunctions are registered by PHP's command-line SAPI itself, under the
// "standard" module (php-src PHP-8.3 sapi/cli/php_cli.c additional_functions;
// main/main.c registers them into "standard"). The CLI read reports them, but
// PHP-FPM never has them.
var cliSAPIFunctions = map[string]bool{"dl": true, "cli_set_process_title": true, "cli_get_process_title": true}

// fpmSAPIFunctions are PHP-FPM's own (php-src PHP-8.3
// sapi/fpm/fpm/fpm_main_arginfo.h, module cgi-fcgi). The CLI read does not
// know them, but every PHP-FPM has them.
var fpmSAPIFunctions = map[string]bool{"fastcgi_finish_request": true, "apache_request_headers": true, "getallheaders": true, "fpm_get_status": true}

// phpFunctionIdentRE is a lowercase PHP function name. Only names that match
// are checked; anything else from a config file is left out of the read.
var phpFunctionIdentRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,99}$`)

// unavailableInFPM decides which names PHP-FPM does not provide. exts comes
// from the CLI read, modules from `php-fpm -m`, iniDisabled from php.ini's
// disable_functions.
func unavailableInFPM(names []string, exts map[string]string, modules, iniDisabled map[string]bool) []string {
	out := []string{}
	for _, n := range names {
		switch {
		case cliSAPIFunctions[n]:
			out = append(out, n)
		case fpmSAPIFunctions[n]:
		case iniDisabled[n]:
			// PHP drops a disabled function from its function table, so the
			// CLI read cannot see it. It is disabled either way; unknown
			// here, not unavailable.
		case exts[n] == "":
			// No extension of this PHP build provides it.
			out = append(out, n)
		case !modules[strings.ToLower(exts[n])]:
			// Its extension is in the CLI build but PHP-FPM does not load it
			// (Debian builds pcntl into the CLI only).
			out = append(out, n)
		}
	}
	return out
}

func phpPoolEffectiveHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p phpPoolEffectiveParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if !phpVersionRE.MatchString(p.PHPVersion) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "php_version must be <major>.<minor>"}
	}
	if !phpPoolSlugRegex.MatchString(p.Slug) || strings.Contains(p.Slug, "..") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid slug format"}
	}
	return readPHPPoolEffective(ctx, p.PHPVersion, p.Slug), nil
}

func readPHPPoolEffective(ctx context.Context, version, slug string) phpPoolEffectiveResponse {
	resp := phpPoolEffectiveResponse{
		PHPVersion:           version,
		Slug:                 slug,
		DisabledFunctions:    []phpEffectiveFunction{},
		PHPDefense:           phpDefenseEffective{Functions: []phpDefenseFunction{}},
		UnavailableFunctions: []string{},
	}

	// The pool conf's own ini lines; php_admin_value beats php_value.
	poolVals := map[string]string{}
	if data, err := os.ReadFile(filepath.Join(phpEtcRoot, version, "fpm", "pool.d", "jabali-"+slug+".conf")); err == nil {
		resp.PoolFound = true
		admin := map[string]string{}
		sc := bufio.NewScanner(bytes.NewReader(data))
		for sc.Scan() {
			m := poolTemplateIniRE.FindStringSubmatch(strings.TrimSpace(sc.Text()))
			if m == nil {
				continue
			}
			if m[1] == "_admin" {
				admin[m[2]] = m[3]
			} else {
				poolVals[m[2]] = m[3]
			}
		}
		for k, v := range admin {
			poolVals[k] = v
		}
	}

	ini, err := phpEffectiveIniRead(ctx, version, slug)
	if err != nil {
		resp.IniReadError = err.Error()
		ini = map[string]string{}
	}

	// FPM adds the pool's disable_functions to php.ini's; a function php.ini
	// disables stays disabled whatever the pool says.
	seen := map[string]bool{}
	for _, f := range splitFunctionList(ini["disable_functions"]) {
		if !seen[f] {
			seen[f] = true
			resp.DisabledFunctions = append(resp.DisabledFunctions, phpEffectiveFunction{Name: f, Source: "php.ini"})
		}
	}
	for _, f := range splitFunctionList(poolVals["disable_functions"]) {
		if !seen[f] {
			seen[f] = true
			resp.DisabledFunctions = append(resp.DisabledFunctions, phpEffectiveFunction{Name: f, Source: "pool"})
		}
	}
	sort.Slice(resp.DisabledFunctions, func(i, j int) bool { return resp.DisabledFunctions[i].Name < resp.DisabledFunctions[j].Name })

	resp.IncludePath = effectiveIni(poolVals, ini, "include_path")
	resp.SessionSavePath = effectiveIni(poolVals, ini, "session.save_path")
	resp.PHPDefense = readPoolPHPDefense(version, slug)

	// Which of the reported functions this PHP-FPM build has at all.
	names := map[string]bool{}
	for _, f := range splitFunctionList(defaultDisableFunctions) {
		names[f] = true
	}
	for _, f := range resp.DisabledFunctions {
		names[f.Name] = true
	}
	for _, f := range resp.PHPDefense.Functions {
		names[f.Name] = true
	}
	check := make([]string, 0, len(names))
	for n := range names {
		if phpFunctionIdentRE.MatchString(n) {
			check = append(check, n)
		}
	}
	sort.Strings(check)
	iniDisabled := map[string]bool{}
	for _, f := range splitFunctionList(ini["disable_functions"]) {
		iniDisabled[f] = true
	}
	modules, merr := phpFPMModules(ctx, version, slug)
	exts, eerr := phpFunctionExtensions(ctx, version, slug, check)
	switch {
	case merr != nil:
		resp.AvailabilityError = merr.Error()
	case eerr != nil:
		resp.AvailabilityError = eerr.Error()
	case err != nil:
		// Without php.ini's disable list a server-disabled function would
		// read as missing from the build.
		resp.AvailabilityError = "php.ini could not be read"
	default:
		resp.UnavailableFunctions = unavailableInFPM(check, exts, modules, iniDisabled)
	}
	resp.ExecConfined = appArmorModeConfines(fpmAppArmorMode(ctx))
	return resp
}

func effectiveIni(pool, ini map[string]string, name string) phpIniEffective {
	if v, ok := pool[name]; ok {
		return phpIniEffective{Value: strings.Trim(v, `"`), Source: "pool"}
	}
	return phpIniEffective{Value: ini[name], Source: "php.ini"}
}

// splitFunctionList splits a disable_functions value (commas, optional
// spaces) into lowercase names.
func splitFunctionList(v string) []string {
	var out []string
	for _, f := range strings.Split(strings.Trim(v, `"`), ",") {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out
}

var phpDefenseModeRE = regexp.MustCompile(`(?m)^# mode=(off|simulation|enforce)\b`)

// readPoolPHPDefense reports the PHP Defense bans of the rules file the pool's
// master loads: its own copy when the pool's scan dir points at one, else the
// server-wide active.rules.
func readPoolPHPDefense(version, slug string) phpDefenseEffective {
	out := phpDefenseEffective{Functions: []phpDefenseFunction{}}
	if _, err := os.Stat(filepath.Join(snuffleupagusLibRoot, version, "snuffleupagus.so")); err != nil {
		return out
	}
	if _, err := os.Stat(filepath.Join(phpEtcRoot, version, "fpm", "conf.d", "30-jabali-snuffleupagus.ini")); err != nil {
		return out
	}
	out.Active = true

	rulesPath := phpDefenseActiveRulesPath
	if ini, err := os.ReadFile(phpDefensePoolIniPath(version, slug)); err == nil {
		for _, line := range strings.Split(string(ini), "\n") {
			v, ok := strings.CutPrefix(strings.TrimSpace(line), "sp.configuration_file=")
			// Only follow a pointer into the agent's own pool-copy dir.
			if ok && filepath.Dir(v) == filepath.Clean(phpDefensePoolRulesDir) {
				rulesPath = v
				out.PoolRules = true
			}
		}
	}
	rules, err := os.ReadFile(rulesPath)
	if err != nil {
		out.Active = false
		return out
	}
	switch m := phpDefenseModeRE.FindSubmatch(rules); {
	case m != nil:
		out.Mode = string(m[1])
	case bytes.Contains(rules, []byte(".simulation();")):
		out.Mode = "simulation"
	default:
		out.Mode = "enforce"
	}
	for _, line := range strings.Split(string(rules), "\n") {
		m := phpDefenseDropLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		state := "blocked"
		if strings.Contains(line, ".simulation()") {
			state = "logged"
		}
		out.Functions = append(out.Functions, phpDefenseFunction{Name: m[1], State: state})
	}
	sort.Slice(out.Functions, func(i, j int) bool { return out.Functions[i].Name < out.Functions[j].Name })
	return out
}

func init() {
	Default.Register("php.pool.effective", phpPoolEffectiveHandler)
}
