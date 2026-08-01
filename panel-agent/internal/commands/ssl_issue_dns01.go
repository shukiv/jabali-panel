package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/certbot"
)

// acmeDNSHookAuth / acmeDNSHookCleanup are the commands certbot runs to
// place and remove the _acme-challenge TXT record for each name. They
// call back into the panel binary, which writes the record through the
// panel's own DNS tables and pushes the zone to PowerDNS — the panel DB
// stays authoritative even for ephemeral challenge rows, and certbot
// persists these commands in the renewal conf so `certbot renew` keeps
// working unattended.
const (
	acmeDNSHookAuth    = "/usr/local/bin/jabali-panel dns acme-hook auth"
	acmeDNSHookCleanup = "/usr/local/bin/jabali-panel dns acme-hook cleanup"
)

// sslIssueDNS01Params is the input shape for ssl.issue_dns01.
//
// CertName pins the certbot lineage (must be a plain FQDN-ish label —
// no `*`). SANs is the full name list; each entry may carry a single
// leading "*." (that is the point of DNS-01: LE only grants wildcards
// over it). The DNS zones for every SAN must be served by this panel's
// PowerDNS — the hook refuses foreign zones, which fails the challenge
// with a clear error instead of a timeout.
type sslIssueDNS01Params struct {
	CertName string   `json:"cert_name"`
	SANs     []string `json:"sans"`
	Email    string   `json:"email"`
	Staging  bool     `json:"staging"`
}

type sslIssueDNS01Response struct {
	CertPath  string `json:"cert_path"`
	KeyPath   string `json:"key_path"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	Staging   bool   `json:"staging"`
	Skipped   bool   `json:"skipped"`
}

func sslIssueDNS01Handler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p sslIssueDNS01Params
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse params: %v", err),
		}
	}

	if !sslDomainRegex.MatchString(p.CertName) || strings.Contains(p.CertName, "*") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid cert_name %q: must be a plain name with no wildcard", p.CertName),
		}
	}
	if len(p.SANs) == 0 {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "sans[] must not be empty",
		}
	}
	for _, san := range p.SANs {
		bare := strings.TrimPrefix(san, "*.")
		// After stripping ONE wildcard label the rest must be a plain
		// FQDN — "*.*.x", "*x" and a bare "*" are all rejected.
		if bare == "" || strings.Contains(bare, "*") || !sslDomainRegex.MatchString(bare) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("invalid SAN %q: must be a FQDN with at most one leading '*.' label", san),
			}
		}
	}
	if !emailRegex.MatchString(p.Email) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid email %q", p.Email),
		}
	}

	runner := certbot.NewRunner()
	result, err := runner.IssueDNS01(p.CertName, p.SANs, p.Email, p.Staging, acmeDNSHookAuth, acmeDNSHookCleanup)
	if err != nil {
		msg := fmt.Sprintf("dns-01 issue failed: %v", err)
		if result != nil && result.Reason != "" {
			msg = fmt.Sprintf("dns-01 issue failed (%s): %v", result.Reason, err)
		}
		if result != nil && result.Stderr != "" {
			if detail := certbot.ExtractActionableDetail(result.Stderr); detail != "" {
				msg = fmt.Sprintf("%s — %s", msg, detail)
			}
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: msg}
	}

	return sslIssueDNS01Response{
		CertPath:  result.CertPath,
		KeyPath:   result.KeyPath,
		IssuedAt:  result.IssuedAt.Format("2006-01-02T15:04:05Z07:00"),
		ExpiresAt: result.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		Staging:   p.Staging,
		Skipped:   result.Skipped,
	}, nil
}

func init() {
	Default.Register("ssl.issue_dns01", sslIssueDNS01Handler)
}
