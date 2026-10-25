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

// hostname.free.apply (JAB-213) persists a claimed free-hostname credential
// and turns on its lifecycle, so an operator can activate a jabalihosted.com
// hostname from Server Settings — not just at install. panel-api does the
// register/claim HTTPS to the service (ADR-0050: the agent never talks to
// third parties); this command only does the root-owned local side:
//
//   1. write /etc/jabali-panel/hostname.env (0600 root:root) — the bearer
//      token that every later /v1/* call authenticates with;
//   2. enable the daily heartbeat timer;
//   3. send the first heartbeat right away. It marks the label in use at the
//      service, so a later claim from this server can never reissue it away
//      from this box.
//
// The certificate comes from the panel-cert reconciler (HTTP-01 for the new
// hostname). This command does not run jabali-hostname-cert.sh: its DNS-01
// wildcard run used the same lineage name as the panel cert, so the two
// overwrote each other, and each run spent a second certificate from the
// base domain's shared Let's Encrypt quota for a cert nothing served.
//
// The hostname itself is changed by the existing system.set_hostname path the
// settings handler already dispatches — this command does not touch it.

// hostnameEnvPathVar is the credential path; a var so tests can redirect it.
var hostnameEnvPathVar = "/etc/jabali-panel/hostname.env"

// hostnameChown is os.Chown in production (agent runs as root); tests override
// it — ownership is asserted on the real box, not in unprivileged unit tests.
var hostnameChown = os.Chown

var freeFQDNRegex = regexp.MustCompile(`^[a-z0-9-]+\.jabalihosted\.com$`)
var freeLabelRegex = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

// freeTokenRegex allowlists the bearer token's charset. hostname.env is read
// by root-run scripts, so the token must never be able to carry shell
// metacharacters ($(...), backticks, ${...}) or a newline that could forge an
// extra KEY=VALUE line.
var freeTokenRegex = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

type hostnameFreeApplyParams struct {
	FQDN  string `json:"fqdn"`
	Label string `json:"label"`
	Email string `json:"email"`
	Token string `json:"token"`
	API   string `json:"api"`
}

func hostnameFreeApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p hostnameFreeApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !freeFQDNRegex.MatchString(p.FQDN) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "fqdn must be <label>.jabalihosted.com"}
	}
	if !freeLabelRegex.MatchString(p.Label) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid label"}
	}
	if _, err := requireEmail(p.Email); err != nil {
		return nil, err
	}
	// Allowlist the token's charset rather than excluding whitespace. The
	// old "no \n\r or space" check still permitted $(...), backticks, and
	// ${...} — and hostname.env is read by root-run scripts (heartbeat timer,
	// certbot renew hooks). Those readers no longer `source` the file, but a
	// writer is the right place to reject remote data outright, and this keeps
	// the value safe for any future consumer.
	if !freeTokenRegex.MatchString(p.Token) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "token must be [A-Za-z0-9_-]+"}
	}
	if !strings.HasPrefix(p.API, "https://") || strings.ContainsAny(p.API, "\n\r ") {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "api must be an https URL"}
	}

	// Persist the credential 0600 root:root. The token must never be
	// world-readable; the parent dir /etc/jabali-panel stays 0755 (the
	// /etc/jabali 0755 SSH-lockout scar generalizes — only the file is tight).
	content := fmt.Sprintf("LABEL=%s\nFQDN=%s\nEMAIL=%s\nTOKEN=%s\nAPI=%s\n",
		p.Label, p.FQDN, p.Email, p.Token, p.API)
	if err := writeAtomic(hostnameEnvPathVar, []byte(content), 0o600); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write %s: %v", hostnameEnvPathVar, err)}
	}
	if err := hostnameChown(hostnameEnvPathVar, 0, 0); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown: %v", err)}
	}

	// Enable the heartbeat timer and send the first heartbeat now
	// (best-effort; skipped in test env). --no-block: the oneshot calls the
	// service over the network and this RPC must return promptly.
	if os.Getenv("JABALI_HOSTNAME_SKIP_SYSTEMD") == "" {
		_ = execCommandContext(ctx, "systemctl", "enable", "--now", "jabali-hostname-heartbeat.timer").Run()
		_ = execCommandContext(ctx, "systemctl", "start", "--no-block", "jabali-hostname-heartbeat.service").Run()
	}

	return okBody{Ok: true}, nil
}

func init() {
	Default.Register("hostname.free.apply", hostnameFreeApplyHandler)
}
