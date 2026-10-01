package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// php.ini_defaults (GH #1543, johnnyq) returns the effective baseline php.ini
// values a domain INHERITS on a given PHP version when it sets no per-domain
// override — so the panel's per-domain PHP Settings dropdowns can label the
// inherit option with the real value (e.g. "256M (Default)") instead of a
// generic "Pool default". The panel overlays the pool's own ini overrides
// (which it holds in the DB) on top of this baseline; here we report what the
// box's FPM master config + conf.d resolve to, read straight from PHP so a
// distro/operator-tuned php.ini is reflected truthfully rather than guessed,
// with the pool template's literal values over it (overlayPoolTemplateDefaults).
// The same baseline is what a domain's vhost pins for a setting it leaves unset
// (php_flag_pins.go, php_value_pins.go), so the label and the pin agree.

// phpIniDefaultDirectives is the fixed set of per-domain PHP directives whose
// box baseline the panel labels as the inherited "(Default)". Kept in lockstep
// with the panel's DomainPHPSettingsPanel selects — EXCEPT display_errors:
// buildPHPValueParam (domain_create.go) pins display_errors=Off on every PHP
// vhost, so a domain that "inherits" always runs with it Off regardless of the
// box php.ini. Reporting ini_get('display_errors') here would surface php.ini's
// value, which can differ from that effective Off and mislabel error exposure —
// so display_errors is deliberately omitted; its select keeps its own
// "Use pool default (off)" label (GH #1332, GH #1705).
var phpIniDefaultDirectives = []string{
	"memory_limit",
	"upload_max_filesize",
	"post_max_size",
	"max_input_vars",
	"max_execution_time",
	"max_input_time",
	"error_reporting", // GH #1332 bitmask; the panel maps it to a preset label
	"date.timezone",   // GH #1332; "" here is PHP's effective UTC fallback
	// GH #1701 Slice 2 flags ("1" on, "" or "0" off). Also the baseline
	// resolvePHPFlagPins pins on a domain that sets no value of its own.
	"log_errors",
	"file_uploads",
	"short_open_tag",
	// GH #1701 Slice 3. A pool cannot override it, so this read is both the
	// "(Default)" label and the value php_admin_pins.go pins on a domain that
	// sets none.
	"allow_url_fopen",
}

// phpVersionRE bounds the version to <major>.<minor> before it is spliced into a
// binary name / ini path — no shell is involved (exec.Command, not sh -c), but
// this keeps a mangled version from probing arbitrary paths.
var phpVersionRE = regexp.MustCompile(`^\d+\.\d+$`)

type phpIniDefaultsParams struct {
	PHPVersion string `json:"php_version"`
}

type phpIniDefaultsResponse struct {
	PHPVersion string            `json:"php_version"`
	Defaults   map[string]string `json:"defaults"`
}

func phpIniDefaultsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p phpIniDefaultsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	if !phpVersionRE.MatchString(p.PHPVersion) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "php_version must be <major>.<minor>"}
	}

	defaults, err := readPHPIniDefaults(ctx, p.PHPVersion)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	return phpIniDefaultsResponse{PHPVersion: p.PHPVersion, Defaults: defaults}, nil
}

// readPHPIniDefaults reads the values PHP itself resolves for the FPM SAPI's
// config: the master php.ini plus its conf.d scan dir — the same layering the
// FPM pool starts from before per-pool php_admin_value. Executed through the
// version's own CLI binary so the answer matches that version's build. version
// must already match phpVersionRE.
func readPHPIniDefaults(ctx context.Context, version string) (map[string]string, error) {
	bin := "php" + version // e.g. php8.3, resolved via PATH
	iniFile := "/etc/php/" + version + "/fpm/php.ini"
	scanDir := "/etc/php/" + version + "/fpm/conf.d"

	cmd := execCommandContext(ctx, bin, "-c", iniFile, "-r", phpIniReadScript())
	// Layer conf.d exactly as FPM does. -n would drop it; instead point the scan
	// dir at the FPM conf.d so extension/tuning .ini files are honoured.
	cmd.Env = append(cmd.Environ(), "PHP_INI_SCAN_DIR="+scanDir)

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("php %s ini read failed: %v", version, err)
	}
	var defaults map[string]string
	if uerr := json.Unmarshal(out, &defaults); uerr != nil {
		return nil, fmt.Errorf("php ini output parse failed: %v", uerr)
	}
	return overlayPoolTemplateDefaults(defaults, readPoolTemplateIniValues(poolTemplatePath())), nil
}

// The php CLI read above is not the whole FPM baseline (GH #1701). Every jabali
// pool conf is rendered from the pool template, whose literal php_value lines
// (GH #253: memory_limit=512M, max_execution_time=300, ...) are what a domain
// with no value of its own actually runs with; php.ini's 128M never applies.
// And the CLI SAPI forces max_execution_time=0 and max_input_time=-1 whatever
// php.ini says, so a CLI read of those two is never the FPM value. A pool's
// own ini overrides (php_admin_value, from the panel DB) sit above both; the
// panel overlays those itself.

// phpCLIHardcodedDirectives are the tracked directives the php CLI SAPI forces.
var phpCLIHardcodedDirectives = map[string]bool{"max_execution_time": true, "max_input_time": true}

// poolTemplatePath is the pool template the agent renders pool confs from.
// JABALI_PHP_POOL_TEMPLATE_PATH overrides it for tests.
func poolTemplatePath() string {
	if p := os.Getenv("JABALI_PHP_POOL_TEMPLATE_PATH"); p != "" {
		return p
	}
	return "/etc/jabali-panel/php-pool.conf.tmpl"
}

// poolTemplateIniRE matches a literal php_value / php_admin_value line.
var poolTemplateIniRE = regexp.MustCompile(`^php(_admin)?_value\[([A-Za-z0-9_.]+)\]\s*=\s*(.*?)\s*$`)

// readPoolTemplateIniValues returns the directives the pool template sets with
// a literal value. Templated lines ({{ ... }}) are skipped; php_admin_value
// beats php_value for the same directive, as in FPM. An unreadable template
// yields nil (logged): the CLI read then stands alone, minus the CLI-hardcoded
// directives.
func readPoolTemplateIniValues(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("php.ini_defaults: pool template %s unreadable: %v", path, err)
		return nil
	}
	vals := map[string]string{}
	admin := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "{{") {
			continue
		}
		m := poolTemplateIniRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		isAdmin := m[1] != ""
		if admin[m[2]] && !isAdmin {
			continue
		}
		vals[m[2]] = m[3]
		if isAdmin {
			admin[m[2]] = true
		}
	}
	return vals
}

// overlayPoolTemplateDefaults layers the template's literal values over the CLI
// read for the tracked directives, and drops a CLI-hardcoded directive the
// template does not set (absent means unknown, never the CLI's 0 or -1).
func overlayPoolTemplateDefaults(cli, tmpl map[string]string) map[string]string {
	out := make(map[string]string, len(cli))
	for k, v := range cli {
		if phpCLIHardcodedDirectives[k] {
			continue
		}
		out[k] = v
	}
	for _, d := range phpIniDefaultDirectives {
		if v, ok := tmpl[d]; ok {
			out[d] = v
		}
	}
	return out
}

// phpIniReadScript builds the PHP -r program that echoes json_encode of ini_get
// for each exposed directive. The directive list is handed to PHP as a JSON
// array literal that json_decode parses, rather than hand-glued single quotes —
// the earlier hand-glued form dropped the final closing quote (a malformed
// foreach list PHP rejects with a fatal parse error / exit 255) and the unit
// test didn't catch it. The names are a fixed internal allowlist
// (phpIniDefaultDirectives) with no quotes or backslashes, so the JSON literal
// sits safely inside a single-quoted PHP string with nothing to escape.
func phpIniReadScript() string {
	namesJSON, _ := json.Marshal(phpIniDefaultDirectives) // ["memory_limit",...]
	return "$d=[];foreach(json_decode('" + string(namesJSON) + "',true) as $k){$d[$k]=(string)ini_get($k);}echo json_encode($d);"
}

func init() {
	Default.Register("php.ini_defaults", phpIniDefaultsHandler)
}
