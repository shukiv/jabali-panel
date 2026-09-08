package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// php.ini_defaults (GH #1543, johnnyq) returns the effective baseline php.ini
// values a domain INHERITS on a given PHP version when it sets no per-domain
// override — so the panel's per-domain PHP Settings dropdowns can label the
// inherit option with the real value (e.g. "256M (Default)") instead of a
// generic "Pool default". The panel overlays the pool's own ini overrides
// (which it holds in the DB) on top of this baseline; here we report only what
// the box's FPM master config + conf.d resolve to, read straight from PHP so a
// distro/operator-tuned php.ini is reflected truthfully rather than guessed.

// phpIniDefaultDirectives is the fixed set the per-domain panel exposes. Kept in
// lockstep with the panel's DomainPHPSettingsPanel selects.
var phpIniDefaultDirectives = []string{
	"memory_limit",
	"upload_max_filesize",
	"post_max_size",
	"max_input_vars",
	"max_execution_time",
	"max_input_time",
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

	// Read the values PHP itself resolves for the FPM SAPI's config: the master
	// php.ini plus its conf.d scan dir — the same layering the FPM pool starts
	// from before per-pool php_admin_value. Executed through the version's own
	// CLI binary so the answer matches that version's build.
	bin := "php" + p.PHPVersion // e.g. php8.3, resolved via PATH
	iniFile := "/etc/php/" + p.PHPVersion + "/fpm/php.ini"
	scanDir := "/etc/php/" + p.PHPVersion + "/fpm/conf.d"

	script := "$d=[];foreach(['" +
		joinQuoted(phpIniDefaultDirectives) +
		"] as $k){$d[$k]=(string)ini_get($k);}echo json_encode($d);"

	cmd := execCommandContext(ctx, bin, "-c", iniFile, "-r", script)
	// Layer conf.d exactly as FPM does. -n would drop it; instead point the scan
	// dir at the FPM conf.d so extension/tuning .ini files are honoured.
	cmd.Env = append(cmd.Environ(), "PHP_INI_SCAN_DIR="+scanDir)

	out, err := cmd.Output()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("php %s ini read failed: %v", p.PHPVersion, err)}
	}
	var defaults map[string]string
	if uerr := json.Unmarshal(out, &defaults); uerr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("php ini output parse failed: %v", uerr)}
	}

	return phpIniDefaultsResponse{PHPVersion: p.PHPVersion, Defaults: defaults}, nil
}

// joinQuoted renders directive names as a PHP single-quoted, comma-separated
// list body. The names are a fixed internal allowlist (phpIniDefaultDirectives),
// never caller input, so there is nothing to escape.
func joinQuoted(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += "','"
		}
		out += n
	}
	return out
}

func init() {
	Default.Register("php.ini_defaults", phpIniDefaultsHandler)
}
