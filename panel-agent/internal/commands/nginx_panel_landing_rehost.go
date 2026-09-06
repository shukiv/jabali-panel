package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nginx.panel_landing_rehost re-points the server_name of the panel's :443
// landing vhost in /etc/nginx/sites-available/jabali-default.conf to the
// current panel hostname, then reloads nginx (JAB-389).
//
// Why it exists: changing the panel FQDN through Settings updates the OS
// hostname (system.set_hostname) and the panel_certificate rows, and the
// self-signed panel cert is re-issued by the ssl.panel.selfsign reconciler.
// But the GH#135 dedicated :443 landing vhost keeps its server_name on the OLD
// FQDN — it is rendered only by install.sh (install_nginx_default_vhost, at
// install / `jabali update`). So after a live rename, https://<new-fqdn>/ on
// :443 no longer matches the landing server{} and falls to the default
// return-444 block: the browser shows "Secure Connection Failed" even though
// :8443 and the cert are correct. That is the exact symptom GH#135 fixed for
// the original hostname, reintroduced by the rename.
//
// Scope is server_name ONLY. `root /var/www/<old>` is left as-is (the admin's
// landing content is preserved; no docroot migration), and the two
// `return 301 https://mail.<host>/` webmail redirects are left untouched: the
// panel mail hostname was deliberately decoupled from the FQDN (#1546,
// renameOnDrift=false), so re-pointing them would send webmail at a name with
// no vhost and no cert.
//
// Why change-time (dispatched by the Settings handler on a hostname change),
// not a stateless reconciler like kratos.config.rehost / ssl.panel.selfsign:
// the panel-api daemon's AppArmor profile grants /etc/jabali-panel/** r and
// /etc/jabali/** r (so those reconcilers can read kratos.yml and panel.crt on
// disk to detect drift) but NOT /etc/nginx — a reconciler in the confined
// daemon could not read this vhost. A self-healing reconciler would have to
// widen the hardened daemon's read surface, which isn't warranted for a
// landing-page server_name. Boxes already broken by a past rename converge
// when install.sh's install_nginx_default_vhost next re-renders under `jabali
// update` (it sources the name from `hostname -f`, which system.set_hostname
// already set to the new FQDN).
type nginxPanelLandingRehostParams struct {
	Hostname string `json:"hostname"`
}

type nginxPanelLandingRehostResponse struct {
	Rewritten    bool   `json:"rewritten"`
	Reason       string `json:"reason,omitempty"`
	Replacements int    `json:"replacements,omitempty"`
}

// panelLandingVhostPath is the on-disk default/landing vhost. Overridable in tests.
var panelLandingVhostPath = "/etc/nginx/sites-available/jabali-default.conf"

// panelLandingServerNameRE captures a server_name directive whose value is a
// real hostname. `server_name _;` (the two default catch-all blocks in the
// same file) is skipped because `_` is not in the value character class, so
// only the GH#135 landing block matches. RE2 has no lookahead — the class
// exclusion is what does the skipping.
var panelLandingServerNameRE = regexp.MustCompile(`(?m)^(\s*server_name\s+)([a-zA-Z0-9][a-zA-Z0-9.-]*)(\s*;)`)

// panelLandingServerName returns the value of the first real-hostname
// server_name directive in the file.
func panelLandingServerName(data []byte) (string, bool) {
	m := panelLandingServerNameRE.FindSubmatch(data)
	if m == nil {
		return "", false
	}
	return string(m[2]), true
}

func nginxPanelLandingRehostHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p nginxPanelLandingRehostParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	target := strings.ToLower(strings.TrimSpace(p.Hostname))
	if !sslDomainRegex.MatchString(target) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid hostname %q", p.Hostname),
		}
	}

	data, err := os.ReadFile(panelLandingVhostPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("read %s: %v", panelLandingVhostPath, err),
		}
	}
	current, ok := panelLandingServerName(data)
	if !ok {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("no landing server_name in %s", panelLandingVhostPath),
		}
	}
	if strings.EqualFold(current, target) {
		return nginxPanelLandingRehostResponse{Rewritten: false, Reason: "already current"}, nil
	}

	rewritten, n := rewritePanelLandingServerName(data, current, target)
	if n == 0 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("no server_name %q to rewrite", current),
		}
	}
	// Confirm the rewrite converged before touching a live nginx.
	if h, ok := panelLandingServerName(rewritten); !ok || !strings.EqualFold(h, target) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "refusing to write: landing server_name did not converge to target",
		}
	}

	if err := writeFilePreservingMeta(panelLandingVhostPath, rewritten); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write %s: %v", panelLandingVhostPath, err),
		}
	}
	// nginx cannot test one file in isolation, so validate the whole config
	// after the swap and roll this file back on failure — a broken
	// jabali-default.conf would take every :443 vhost down, not just the panel.
	if out, terr := execCommandContext(ctx, "nginx", "-t").CombinedOutput(); terr != nil {
		_ = writeFilePreservingMeta(panelLandingVhostPath, data)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("nginx -t failed after landing server_name rewrite (rolled back): %s", strings.TrimSpace(string(out))),
		}
	}
	if out, rerr := execCommandContext(ctx, "systemctl", "reload", "nginx").CombinedOutput(); rerr != nil {
		// The file is valid and converged on disk; the next nginx reload from
		// any domain operation applies it. Surface the reload failure so the
		// caller logs it.
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl reload nginx failed: %s", strings.TrimSpace(string(out))),
		}
	}

	return nginxPanelLandingRehostResponse{
		Rewritten:    true,
		Reason:       current + " -> " + target,
		Replacements: n,
	}, nil
}

// rewritePanelLandingServerName replaces the value of every server_name
// directive equal to oldHost with newHost, and returns the new bytes plus the
// replacement count. Anchoring on the whole `server_name <value>;` directive
// leaves `root /var/www/<oldHost>` and the `mail.<oldHost>` redirects
// untouched by construction — only server_name is rewritten.
func rewritePanelLandingServerName(data []byte, oldHost, newHost string) ([]byte, int) {
	re := regexp.MustCompile(`(?m)^(\s*server_name\s+)` + regexp.QuoteMeta(oldHost) + `(\s*;)`)
	count := len(re.FindAll(data, -1))
	if count == 0 {
		return data, 0
	}
	return re.ReplaceAll(data, []byte("${1}"+newHost+"${2}")), count
}

func init() {
	Default.Register("nginx.panel_landing_rehost", nginxPanelLandingRehostHandler)
}
