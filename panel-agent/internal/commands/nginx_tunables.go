package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nginx.tunables.apply — renders the server-wide nginx tunables managed by
// Server Settings → Nginx (M55).
//
// Two destinations, because nginx config has two kinds of directive here:
//
//   - DIRECTIVES DEBIAN/UBUNTU SHIP IN nginx.conf (server_tokens, gzip,
//     keepalive_timeout) + the worker knobs (worker_processes main-scope,
//     worker_connections events-scope): these are REPLACED IN PLACE in
//     /etc/nginx/nginx.conf. They can't go in a conf.d fragment because that
//     fragment is included inside http{} where nginx.conf already declares
//     them — a second declaration is a hard `nginx -t` "directive is
//     duplicate" error (caught live on a real box, GH#172 follow-up). We
//     replace the first matching line only (a stray copy inside a nested
//     server{} is left alone) and skip silently if the directive is absent.
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

const (
	nginxTunablesFragmentPath = "/etc/nginx/conf.d/05-jabali-tunables.conf"
	nginxMainConfPath         = "/etc/nginx/nginx.conf"
	nginxCustomHTTPMaxLen     = 4000
)

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

	fragment := BuildNginxTunablesFragment(&p)

	liveMain, _ := os.ReadFile(nginxMainConfPath)
	newMain, mainChanged := patchNginxMainConf(liveMain, &p)

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
// unusual nginx.conf could break the whole config. Debian/Ubuntu stock
// nginx.conf ships server_tokens/gzip/worker_* so those always apply;
// keepalive_timeout is occasionally absent, in which case it silently no-ops
// (nginx's built-in default stands). Returns new bytes + whether anything
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
	re := regexp.MustCompile(`(?m)^([ \t]*)` + regexp.QuoteMeta(name) + `[ \t][^;\n]*;`)
	loc := re.FindStringSubmatchIndex(conf)
	if loc == nil {
		return conf, false
	}
	newSeg := string(re.ExpandString(nil, "${1}"+name+" "+value+";", conf, loc))
	out := conf[:loc[0]] + newSeg + conf[loc[1]:]
	return out, out != conf
}

// BuildNginxTunablesFragment renders the http{}-scope fragment of
// genuinely-additive directives (those NOT in stock nginx.conf). Pure +
// deterministic so the idempotent read-compare in the handler works.
// server_tokens / gzip / keepalive_timeout are intentionally NOT here — they
// live in nginx.conf and are patched there (see patchNginxMainConf).
func BuildNginxTunablesFragment(p *nginxTunablesApplyParams) string {
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
