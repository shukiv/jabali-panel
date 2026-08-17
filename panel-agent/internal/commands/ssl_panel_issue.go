package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/certbot"
)

// sslPanelIssueParams is the input shape for ssl.panel.issue.
//
// Hostname is the panel's canonical FQDN (server_settings.hostname);
// ExtraHostnames typically carries `mail.<hostname>` so Bulwark's
// vhost gets the same cert via SAN. Email is the LE registration
// address (server_settings.admin_email). Staging gates --staging on
// the certbot invocation so admins can rehearse without burning the
// 50-cert/week LE rate limit.
type sslPanelIssueParams struct {
	Hostname       string   `json:"hostname"`
	ExtraHostnames []string `json:"extra_hostnames,omitempty"`
	Email          string   `json:"email"`
	Staging        bool     `json:"staging,omitempty"`
	// Kind (ADR-0105) routes the deploy-hook to the right target:
	// "" / "hostname" → panel.{crt,key} (+ reload panel/bulwark);
	// "mail" → panel-mail.{crt,key} (+ restart stalwart).
	Kind        string `json:"kind,omitempty"`
	CertPEMPath string `json:"cert_pem_path,omitempty"`
}

// sslPanelIssueResponse mirrors the agent's ssl.issue shape so callers
// can treat both response types uniformly.
type sslPanelIssueResponse struct {
	CertPath  string `json:"cert_path"`
	KeyPath   string `json:"key_path"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	Staging   bool   `json:"staging"`
}

// panelACMEWebroot is the directory nginx serves /.well-known/acme-challenge/
// from for the panel hostname's HTTP-01 challenges. install.sh's
// install_jabali_panel_cert_hook step creates this with mode 0750
// owned by root:www-data so certbot (root) writes and nginx
// (www-data) reads.
const panelACMEWebroot = "/var/www/jabali-panel-acme"

// panelDeployHook is the certbot deploy-hook script install.sh
// drops at /etc/letsencrypt/renewal-hooks/deploy/. The agent
// invokes it directly after a first-issue so the cert lands at
// /etc/jabali/tls/panel.{crt,key} and the consuming services
// (nginx, jabali-panel, jabali-bulwark) reload — same path certbot
// renewals will run through unattended.
const panelDeployHook = "/etc/letsencrypt/renewal-hooks/deploy/jabali-panel-cert.sh"

// runDeployHookFn is a package-level seam so unit tests can stub
// out the actual exec.Command without spawning processes. Production
// wiring is exec.Command + cmd.Run().
var runDeployHookFn = func(ctx context.Context, hostname, kind string) error {
	if kind == "" {
		kind = "hostname"
	}
	cmd := execCommandContext(ctx, panelDeployHook)
	// certbot sets RENEWED_LINEAGE on real renewals; for first-issue
	// we replicate the contract + pass the kind so the hook routes
	// to the right deploy target (ADR-0105).
	cmd.Env = append(os.Environ(),
		"RENEWED_LINEAGE=/etc/letsencrypt/live/"+hostname,
		"JABALI_PANEL_CERT_KIND="+kind,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deploy-hook failed: %v: %s", err, string(out))
	}
	return nil
}

func sslPanelIssueHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sslPanelIssueParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	if !sslDomainRegex.MatchString(p.Hostname) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid hostname %q", p.Hostname),
		}
	}
	for _, h := range p.ExtraHostnames {
		if !sslDomainRegex.MatchString(h) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("invalid hostname %q in extra_hostnames[]", h),
			}
		}
	}
	if !emailRegex.MatchString(p.Email) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid email %q", p.Email),
		}
	}

	// The panel-acme webroot must exist with the right perms before
	// certbot writes its challenge file. install.sh provisions this;
	// we tolerate a missing directory gracefully (return a clear
	// error) rather than chmod-ing on the agent's behalf — that's an
	// install-time concern, not a runtime one.
	if info, err := os.Stat(panelACMEWebroot); err != nil || !info.IsDir() {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("panel acme webroot missing at %s — re-run install.sh", panelACMEWebroot),
		}
	}

	runner := certbot.NewRunner()
	result, err := runner.Issue(p.Hostname, panelACMEWebroot, p.Email, p.Staging, p.ExtraHostnames)
	if err != nil {
		details, _ := json.Marshal(map[string]any{
			"reason": result.Reason,
			"stderr": result.Stderr,
		})
		actionable := certbot.ExtractActionableDetail(result.Stderr)
		msg := fmt.Sprintf("certbot panel issue failed: %v", err)
		if actionable != "" {
			msg = fmt.Sprintf("certbot panel issue failed (%s): %s", result.Reason, actionable)
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: msg,
			Details: json.RawMessage(details),
		}
	}

	// Run the deploy-hook directly. Certbot only fires deploy-hooks
	// on renewals, NOT on first-issue, so the agent triggers it once
	// the cert lineage exists.
	//
	// EXCEPT when certbot kept an existing still-valid cert
	// (result.Skipped): nothing changed on disk, so re-running the
	// hook only causes a needless service churn — and for the
	// hostname kind that churn is `systemctl restart jabali-panel`,
	// i.e. the panel-cert self-restart deadlock (the reconciler that
	// called us IS jabali-panel; the restart SIGTERMs it mid-RPC so
	// it never records status=issued, and re-dispatches forever).
	// The reply below still carries the existing cert's dates, so the
	// reconciler converges to issued without any restart.
	if !result.Skipped {
		if err := runDeployHookFn(ctx, p.Hostname, p.Kind); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: err.Error(),
			}
		}
	}

	return sslPanelIssueResponse{
		CertPath:  result.CertPath,
		KeyPath:   result.KeyPath,
		IssuedAt:  result.IssuedAt.UTC().Format("2006-01-02T15:04:05Z"),
		ExpiresAt: result.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		Staging:   p.Staging,
	}, nil
}

func init() {
	Default.Register("ssl.panel.issue", sslPanelIssueHandler)
}
