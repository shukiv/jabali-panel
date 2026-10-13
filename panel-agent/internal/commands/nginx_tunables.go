package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nginx.tunables.apply — renders the server-wide nginx tunables managed by
// Server Settings → Nginx (M55).
//
// Two destinations, because nginx config has two kinds of directive here:
//
//   - DIRECTIVES nginx.conf MAY DECLARE (server_tokens, gzip,
//     keepalive_timeout) + the worker knobs (worker_processes main-scope,
//     worker_connections events-scope): where nginx.conf declares one, it is
//     REPLACED IN PLACE in /etc/nginx/nginx.conf. It can't also go in the
//     conf.d fragment, which is included inside the same http{} — a second
//     declaration is a hard `nginx -t` "directive is duplicate" error (caught
//     live on a real box, GH#172 follow-up). We replace the first matching
//     line only (a stray copy inside a nested server{} is left alone).
//
//   - Where nginx.conf does NOT declare one of the three http{}-scope ones,
//     it goes in the fragment instead (see httpDirectivesForFragment). Stock
//     Debian and Ubuntu ship `# server_tokens off;` commented out, and Ubuntu
//     24.04 has no keepalive_timeout line, so patching in place alone left
//     nginx on its built-in `server_tokens on` while the page said off. The
//     worker knobs have no such fallback: they belong outside http{}.
//
//   - GENUINELY-ADDITIVE DIRECTIVES (client_max_body_size, the client/send
//     timeouts, the proxy timeouts, plus the admin free-form block): these
//     are NOT in stock nginx.conf, so they render cleanly into the http-scope
//     fragment /etc/nginx/conf.d/05-jabali-tunables.conf.
//
// Atomicity: both files are staged with a backup, swapped in, then a SINGLE
// `nginx -t` validates the whole config. On failure BOTH roll back. So a bad
// value (or a bad admin custom-snippet) never leaves nginx unable to reload.
//
// Inputs are re-validated here (not just at the panel-api edge): the agent is
// the privileged boundary writing root-owned config and must not trust the
// caller's strings. Size/timeout values are regex-checked; the free-form
// CustomHTTP is length-capped + NUL-screened but otherwise gated only by
// nginx -t (the documented "advanced, can destabilize" contract).

// Variables so tests can point them at a temp dir.
var (
	nginxTunablesFragmentPath = "/etc/nginx/conf.d/05-jabali-tunables.conf"
	nginxMainConfPath         = "/etc/nginx/nginx.conf"
)

const nginxCustomHTTPMaxLen = 4000

var nginxSizeRe = regexp.MustCompile(`^[0-9]+[kKmMgG]?$`)
var nginxTimeRe = regexp.MustCompile(`^[0-9]+(ms|s|m|h)?$`)
var nginxWorkerProcessesRe = regexp.MustCompile(`^(auto|[1-9][0-9]?)$`)

type nginxTunablesApplyParams struct {
	ClientMaxBodySize   string `json:"client_max_body_size"`
	KeepaliveTimeout    string `json:"keepalive_timeout"`
	ServerTokens        bool   `json:"server_tokens"`
	Gzip                bool   `json:"gzip"`
	ClientBodyTimeout   string `json:"client_body_timeout"`
	ClientHeaderTimeout string `json:"client_header_timeout"`
	SendTimeout         string `json:"send_timeout"`
	ProxyConnectTimeout string `json:"proxy_connect_timeout"`
	ProxyReadTimeout    string `json:"proxy_read_timeout"`
	ProxySendTimeout    string `json:"proxy_send_timeout"`
	WorkerProcesses     string `json:"worker_processes"`
	WorkerConnections   uint32 `json:"worker_connections"`
	CustomHTTP          string `json:"custom_http"`
}

type nginxTunablesApplyResponse struct {
	FragmentPath    string `json:"fragment_path"`
	MainConfPatched bool   `json:"main_conf_patched"`
	NoChange        bool   `json:"no_change,omitempty"`
}

func nginxTunablesApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p nginxTunablesApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if err := validateNginxTunables(&p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}

	liveMain, _ := os.ReadFile(nginxMainConfPath)
	newMain, mainChanged := patchNginxMainConf(liveMain, &p)
	fragment := BuildNginxTunablesFragment(&p, httpDirectivesForFragment(liveMain, otherConfDFiles(), &p))

	liveFragment, _ := os.ReadFile(nginxTunablesFragmentPath)
	fragmentChanged := !bytes.Equal(liveFragment, []byte(fragment))

	if !fragmentChanged && !mainChanged {
		return &nginxTunablesApplyResponse{FragmentPath: nginxTunablesFragmentPath, NoChange: true}, nil
	}

	// JAB-71: serialize swap→test→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	var rollbacks []func()
	restore := func() {
		for i := len(rollbacks) - 1; i >= 0; i-- {
			rollbacks[i]()
		}
	}

	if fragmentChanged {
		if err := swapFile(nginxTunablesFragmentPath, []byte(fragment), &rollbacks); err != nil {
			restore()
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
		}
	}
	if mainChanged {
		if err := swapFile(nginxMainConfPath, newMain, &rollbacks); err != nil {
			restore()
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
		}
	}

	testCmd := execCommandContext(ctx, "nginx", "-t")
	var testOut bytes.Buffer
	testCmd.Stdout = &testOut
	testCmd.Stderr = &testOut
	if err := testCmd.Run(); err != nil {
		restore()
		return nil, nginxTestFailure("nginx.tunables (rolled back)", testOut.String())
	}

	reloadCmd := execCommandContext(ctx, "systemctl", "reload", "nginx")
	var reloadOut bytes.Buffer
	reloadCmd.Stdout = &reloadOut
	reloadCmd.Stderr = &reloadOut
	if err := reloadCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("nginx -t passed but reload failed: %s", reloadOut.String()),
		}
	}

	return &nginxTunablesApplyResponse{FragmentPath: nginxTunablesFragmentPath, MainConfPatched: mainChanged}, nil
}

// swapFile backs up path → .jabali-bak (if present), writes content, and
// pushes a rollback closure restoring the previous state. The caller runs the
// rollbacks in reverse on any later failure.
func swapFile(path string, content []byte, rollbacks *[]func()) error {
	backup := path + ".jabali-bak"
	orig, statErr := os.ReadFile(path)
	had := statErr == nil
	if had {
		if err := os.WriteFile(backup, orig, 0644); err != nil {
			return fmt.Errorf("backup %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, content, 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	*rollbacks = append(*rollbacks, func() {
		if had {
			_ = os.WriteFile(path, orig, 0644)
		} else {
			_ = os.Remove(path)
		}
	})
	return nil
}

// patchNginxMainConf applies the directives that live in nginx.conf (rather
// than the http-scope fragment, where they would duplicate Debian's stock
// declarations and fail nginx -t). Every directive is REPLACE-IN-PLACE,
// first-match-only, skip-if-absent — never inserted, because getting scope
// placement wrong (worker_connections must be inside events{}, etc.) on an
// unusual nginx.conf could break the whole config. Stock nginx.conf ships
// the worker lines and gzip; server_tokens is commented out and
// keepalive_timeout is sometimes absent, and those two then go in the
// fragment (httpDirectivesForFragment). Returns new bytes + whether anything
// changed.
func patchNginxMainConf(conf []byte, p *nginxTunablesApplyParams) ([]byte, bool) {
	if len(conf) == 0 {
		return conf, false
	}
	s := string(conf)
	changed := false
	set := func(name, value string) {
		if value == "" {
			return
		}
		if ns, c := setDirectiveFirst(s, name, value); c {
			s = ns
			changed = true
		}
	}
	set("worker_processes", p.WorkerProcesses)
	if p.WorkerConnections > 0 {
		set("worker_connections", fmt.Sprintf("%d", p.WorkerConnections))
	}
	set("server_tokens", boolOnOff(p.ServerTokens))
	set("gzip", boolOnOff(p.Gzip))
	set("keepalive_timeout", p.KeepaliveTimeout)
	return []byte(s), changed
}

func boolOnOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// setDirectiveFirst replaces the value of the first line declaring `name`,
// preserving its leading indentation (capture ${1}) and stopping the match at
// the first `;` so any trailing comment (e.g. "server_tokens off; # ...") is
// left intact — which makes a default-value save a true no-op. Returns the
// new string + whether it changed. A directive that isn't present is left
// untouched (changed=false). Go's regexp has no ReplaceFirst, so we splice by
// submatch index.
func setDirectiveFirst(conf, name, value string) (string, bool) {
	re := directiveLineRE(name)
	loc := re.FindStringSubmatchIndex(conf)
	if loc == nil {
		return conf, false
	}
	newSeg := string(re.ExpandString(nil, "${1}"+name+" "+value+";", conf, loc))
	out := conf[:loc[0]] + newSeg + conf[loc[1]:]
	return out, out != conf
}

// directiveLineRE matches an active (uncommented) `name value;` line.
func directiveLineRE(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^([ \t]*)` + regexp.QuoteMeta(name) + `[ \t][^;\n]*;`)
}

// httpDirectivesForFragment returns the server_tokens / gzip /
// keepalive_timeout lines the fragment must carry because nothing else in the
// http block declares them: not nginx.conf, not another conf.d file, not the
// admin's custom directives. A second declaration in the same block fails
// nginx -t and rolls the whole apply back, so when nginx.conf could not be
// read nothing is added.
func httpDirectivesForFragment(liveMain []byte, confD [][]byte, p *nginxTunablesApplyParams) []string {
	if len(liveMain) == 0 {
		return nil
	}
	declared := func(name string) bool {
		re := directiveLineRE(name)
		if re.Match(liveMain) || re.MatchString(p.CustomHTTP) {
			return true
		}
		for _, conf := range confD {
			if re.Match(conf) {
				return true
			}
		}
		return false
	}
	var lines []string
	for _, d := range []struct{ name, value string }{
		{"server_tokens", boolOnOff(p.ServerTokens)},
		{"gzip", boolOnOff(p.Gzip)},
		{"keepalive_timeout", p.KeepaliveTimeout},
	} {
		if d.value != "" && !declared(d.name) {
			lines = append(lines, d.name+" "+d.value+";")
		}
	}
	return lines
}

// otherConfDFiles reads the other *.conf files in the fragment's directory;
// nginx includes them into the same http block.
func otherConfDFiles() [][]byte {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(nginxTunablesFragmentPath), "*.conf"))
	var out [][]byte
	for _, m := range matches {
		if m == nginxTunablesFragmentPath {
			continue
		}
		if b, err := os.ReadFile(m); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// BuildNginxTunablesFragment renders the http{}-scope fragment of
// genuinely-additive directives (those NOT in stock nginx.conf). Pure +
// deterministic so the idempotent read-compare in the handler works.
// server_tokens / gzip / keepalive_timeout appear only as httpLines, the ones
// nothing else in the http block declares (httpDirectivesForFragment).
func BuildNginxTunablesFragment(p *nginxTunablesApplyParams, httpLines []string) string {
	var b strings.Builder
	b.WriteString("# Auto-generated by jabali — do not edit.\n")
	b.WriteString("# Server-wide nginx tunables (Server Settings → Nginx, M55).\n")
	b.WriteString("# http{}-scope defaults; individual vhosts may override per-directive.\n\n")

	if p.ClientMaxBodySize != "" {
		fmt.Fprintf(&b, "client_max_body_size %s;\n", p.ClientMaxBodySize)
	}
	if p.ClientBodyTimeout != "" {
		fmt.Fprintf(&b, "client_body_timeout %s;\n", p.ClientBodyTimeout)
	}
	if p.ClientHeaderTimeout != "" {
		fmt.Fprintf(&b, "client_header_timeout %s;\n", p.ClientHeaderTimeout)
	}
	if p.SendTimeout != "" {
		fmt.Fprintf(&b, "send_timeout %s;\n", p.SendTimeout)
	}
	if p.ProxyConnectTimeout != "" {
		fmt.Fprintf(&b, "proxy_connect_timeout %s;\n", p.ProxyConnectTimeout)
	}
	if p.ProxyReadTimeout != "" {
		fmt.Fprintf(&b, "proxy_read_timeout %s;\n", p.ProxyReadTimeout)
	}
	if p.ProxySendTimeout != "" {
		fmt.Fprintf(&b, "proxy_send_timeout %s;\n", p.ProxySendTimeout)
	}

	if len(httpLines) > 0 {
		b.WriteString("\n# Not set in nginx.conf, so set here:\n")
		for _, l := range httpLines {
			b.WriteString(l + "\n")
		}
	}

	custom := strings.TrimRight(p.CustomHTTP, " \t\r\n")
	if custom != "" {
		b.WriteString("\n# --- Admin custom directives (Server Settings → Nginx → Advanced) ---\n")
		b.WriteString(custom)
		b.WriteString("\n")
	}
	return b.String()
}

func validateNginxTunables(p *nginxTunablesApplyParams) error {
	if p.ClientMaxBodySize != "" && !nginxSizeRe.MatchString(p.ClientMaxBodySize) {
		return fmt.Errorf("client_max_body_size: invalid nginx size %q", p.ClientMaxBodySize)
	}
	times := map[string]string{
		"keepalive_timeout":     p.KeepaliveTimeout,
		"client_body_timeout":   p.ClientBodyTimeout,
		"client_header_timeout": p.ClientHeaderTimeout,
		"send_timeout":          p.SendTimeout,
		"proxy_connect_timeout": p.ProxyConnectTimeout,
		"proxy_read_timeout":    p.ProxyReadTimeout,
		"proxy_send_timeout":    p.ProxySendTimeout,
	}
	for name, v := range times {
		if v != "" && !nginxTimeRe.MatchString(v) {
			return fmt.Errorf("%s: invalid nginx time %q", name, v)
		}
	}
	if p.WorkerProcesses != "" && !nginxWorkerProcessesRe.MatchString(p.WorkerProcesses) {
		return fmt.Errorf("worker_processes: must be 'auto' or 1-99")
	}
	if p.WorkerConnections > 1048576 {
		return fmt.Errorf("worker_connections: must be <= 1048576")
	}
	if len(p.CustomHTTP) > nginxCustomHTTPMaxLen {
		return fmt.Errorf("custom_http: must be <= %d chars", nginxCustomHTTPMaxLen)
	}
	if strings.ContainsRune(p.CustomHTTP, 0) {
		return fmt.Errorf("custom_http: must not contain NUL bytes")
	}
	return nil
}

func init() {
	Default.Register("nginx.tunables.apply", nginxTunablesApplyHandler)
}
