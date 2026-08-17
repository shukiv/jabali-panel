package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/certbot"
)

// M6.6 ssl.mail.issue — per-domain mail TLS.
//
// Issues an LE cert covering the four mail SAN hostnames for one
// tenant domain, then hands off to the deploy-hook so Stalwart picks
// up the new cert via its x:Certificate registry. The hard work
// (certbot certonly) lands in step 3; this skeleton focuses on the
// DNS pre-check that every issuance attempt MUST pass first.
//
// Why pre-check: HTTP-01 needs every SAN hostname resolvable to the
// panel's public IP. If any of the four don't, certbot will fail
// noisily and consume an LE rate-limit slot for nothing. Cheap
// pre-check turns a hard fail into a soft dns_missing state that
// just retries on the next reconciler tick.

// mailSANHostnames returns the 4 hostnames the per-domain mail cert
// must cover, in stable order. Centralised so step 3 (certbot) +
// step 4 (deploy-hook) + step 6 (UI) all agree on the set.
func mailSANHostnames(domain string) []string {
	domain = strings.TrimSpace(strings.ToLower(domain))
	return []string{
		"mail." + domain,
		"autoconfig." + domain,
		"autodiscover." + domain,
		"mta-sts." + domain,
	}
}

type sslMailIssueParams struct {
	DomainID string `json:"domain_id"`
	Domain   string `json:"domain"`
	Email    string `json:"email"`
	Webroot  string `json:"webroot"`
	Staging  bool   `json:"staging"`
	PublicIP string `json:"public_ip"`          // required for DNS check; reconciler resolves this from server_settings
	SkipDNS  bool   `json:"skip_dns,omitempty"` // operator override; never set in production
}

// sslMailIssueOutcome is one of:
//
//	dns_missing — one or more SANs do not resolve to PublicIP. Body
//	              carries the per-hostname result map.
//	issued      — certbot returned a fresh lineage (step 3 work).
//	failed      — certbot returned a non-DNS error (rate-limit,
//	              HTTP-01 401, etc).
type sslMailIssueResponse struct {
	Outcome     string             `json:"outcome"`
	Domain      string             `json:"domain"`
	SANs        []string           `json:"sans"`
	DNS         map[string]dnsScan `json:"dns"`
	LineagePath string             `json:"lineage_path,omitempty"`
	IssuedAt    string             `json:"issued_at,omitempty"`
	ExpiresAt   string             `json:"expires_at,omitempty"`
	Detail      string             `json:"detail,omitempty"`
}

// dnsScan is the per-hostname A-record check result the UI renders
// in step 6 (DNS table).
type dnsScan struct {
	Hostname  string   `json:"hostname"`
	Addresses []string `json:"addresses"`
	Matches   bool     `json:"matches"`
	Error     string   `json:"error,omitempty"`
}

// scanMailSANDNS resolves each SAN hostname and records whether it
// points at publicIP. The mandatory-vs-opportunistic decision lives
// in the caller: only mail.<d> MUST resolve to us (it is the live
// SMTP/IMAP/JMAP endpoint). autoconfig/autodiscover/mta-sts often
// legitimately point at a separate webmail or MTA-STS policy host,
// so they are folded into the cert only when they happen to resolve
// to us — bundling a name that resolves elsewhere fails HTTP-01 for
// the ENTIRE certbot order, which previously parked the whole cert
// in dns_missing and left Stalwart on its self-signed default
// (GH #132). Returns the per-hostname result map.
// sanPublicLookup resolves a host via public resolvers, reporting whether
// any resolver gave a definitive answer (see
// dnsverify.LookupHostExternalResult). Injected so tests can stub DNS.
type sanPublicLookup func(ctx context.Context, host string) (addrs []string, queried bool)

func scanMailSANDNS(ctx context.Context, sans []string, publicIP string, publicLookup sanPublicLookup) map[string]dnsScan {
	if publicLookup == nil {
		publicLookup = dnsverify.LookupHostExternalResult
	}
	boxResolver := &net.Resolver{PreferGo: true}
	out := make(map[string]dnsScan, len(sans))
	for _, h := range sans {
		row := dnsScan{Hostname: h}
		// 6s budget per lookup; reconciler tick has more, this just
		// prevents a single dead nameserver from stalling the verb.
		lookupCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		// Resolve as the Let's Encrypt validator will — via PUBLIC
		// resolvers, NOT the box. The box's local PDNS is authoritative
		// for every zone hosted here, so a name like
		// autoconfig.<tenant> (a local CNAME -> mail.<tenant> -> our IP)
		// resolves to publicIP locally even when the tenant's real
		// nameservers have no such record. The old box-resolver scan
		// then kept it as a SAN, and certbot's HTTP-01 order failed
		// with NXDOMAIN for the whole cert -> Stalwart fell back to the
		// shared self-signed/default cert (GH #132). LookupHostExternalResult
		// returns queried=false only when EVERY public resolver was
		// unreachable (blocked egress); in that case we fall back to the
		// box so an isolated host still issues rather than dropping
		// every SAN.
		addrs, queried := publicLookup(lookupCtx, h)
		if !queried {
			var err error
			addrs, err = boxResolver.LookupHost(lookupCtx, h)
			if err != nil {
				row.Error = err.Error()
				cancel()
				out[h] = row
				continue
			}
		}
		cancel()
		row.Addresses = addrs
		if publicIP != "" {
			for _, a := range addrs {
				if a == publicIP {
					row.Matches = true
					break
				}
			}
		}
		out[h] = row
	}
	return out
}

// selectEffectiveSANs returns the cert SAN list given the full
// mailSANHostnames slice and the DNS scan. allSANs[0] (mail.<d>) is
// always included — the caller has already verified it resolves to
// us. Each aux name is appended only when its scan row reports a
// match, so names pointing at a separate webmail / MTA-STS host are
// dropped rather than failing the whole certbot order (GH #132).
// Order is preserved so the lineage primary stays mail.<d>.
func selectEffectiveSANs(allSANs []string, dns map[string]dnsScan) []string {
	if len(allSANs) == 0 {
		return allSANs
	}
	out := []string{allSANs[0]}
	for _, h := range allSANs[1:] {
		if dns[h].Matches {
			out = append(out, h)
		}
	}
	return out
}

func sslMailIssueHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sslMailIssueParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse: %v", err),
		}
	}
	p.Domain = strings.TrimSpace(strings.ToLower(p.Domain))
	if p.Domain == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "domain is required",
		}
	}
	if !sslDomainRegex.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid domain %q", p.Domain),
		}
	}

	allSANs := mailSANHostnames(p.Domain)
	// effectiveSANs is what we actually hand to certbot. mail.<d> is
	// always first (primary lineage name); aux names are appended
	// only when DNS proves they point at us.
	effectiveSANs := allSANs

	if !p.SkipDNS {
		if p.PublicIP == "" {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: "public_ip is required for DNS pre-check (or set skip_dns)",
			}
		}
		dns := scanMailSANDNS(ctx, allSANs, p.PublicIP, nil)
		// Only mail.<d> (allSANs[0]) is mandatory — it is the live
		// mail endpoint the cert protects. If it doesn't resolve to
		// us there is nothing to issue for, so park in dns_missing.
		if !dns[allSANs[0]].Matches {
			return sslMailIssueResponse{
				Outcome: "dns_missing",
				Domain:  p.Domain,
				SANs:    allSANs,
				DNS:     dns,
				// GH #723: two reporters read this as a dead-end error and
				// waited/worried. Say the recheck out loud — the panel
				// retries every 3 minutes (MarkDNSMissing) and issues the
				// moment public DNS shows the record.
				Detail: fmt.Sprintf("%s must resolve to %s before a mail certificate can be issued; rechecked automatically every few minutes", allSANs[0], p.PublicIP),
			}, nil
		}
		// Fold in autoconfig/autodiscover/mta-sts only when each
		// resolves to us. A name pointing at a separate webmail /
		// MTA-STS host is silently dropped from the order instead of
		// failing the whole issuance (GH #132).
		effectiveSANs = selectEffectiveSANs(allSANs, dns)
	}

	if p.Webroot == "" {
		// Shared mail ACME webroot. Every per-domain mail vhost serves
		// /.well-known/acme-challenge/ from here (jabali-mail-vhost.conf.tmpl
		// `root /var/www/jabali-acme`), and certbot --webroot writes the
		// challenge here. (Distinct from the panel-hostname cert's
		// /var/www/jabali-panel-acme.)
		p.Webroot = "/var/www/jabali-acme"
	}
	if p.Email == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "email is required for LE registration",
		}
	}

	// certbot certonly --webroot --domains mail.<d>[,autoconfig.<d>]
	// [,autodiscover.<d>][,mta-sts.<d>]. Primary SAN = mail.<d> so the
	// lineage lands at /etc/letsencrypt/live/mail.<d>/ -- the
	// deploy-hook (step 4) uses the lineage basename to route the
	// new cert into Stalwart's per-domain x:Certificate entry. The
	// aux SANs are only present when DNS proved they point at us.
	// Ensure the webroot exists before certbot — install.sh provisions it on
	// fresh installs, but existing installs (or a missed bootstrap) would hit
	// "webroot does not exist or is not a directory" and fail every mail cert
	// (GH#132 root cause: nothing created /var/www/jabali-acme). Placed after
	// all validation so a bad request still 4xx's before we touch the FS. 0755
	// so nginx (www-data) can traverse + serve the public challenge file.
	if err := os.MkdirAll(p.Webroot, 0o755); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("could not create ACME webroot %s: %v", p.Webroot, err),
		}
	}

	runner := certbot.NewRunner()
	res, err := runner.Issue(effectiveSANs[0], p.Webroot, p.Email, p.Staging, effectiveSANs[1:])
	if err != nil {
		// Surface the actual LE failure reason, not just "exit status 1".
		// certbot prints a "Domain: / Type: / Detail:" block before "Some
		// challenges have failed"; ExtractActionableDetail pulls it so the
		// panel's last_error tells the operator which SAN failed and why
		// (rate limit, CAA, HTTP-01 404, ...) without VPS shell access to
		// /var/log/letsencrypt/letsencrypt.log. Mirrors ssl.issue /
		// ssl.panel.issue (GH #132).
		detail := firstMailErrLine(err.Error())
		if res != nil {
			reason := res.Reason
			if reason == "" {
				reason = "unknown"
			}
			if actionable := certbot.ExtractActionableDetail(res.Stderr); actionable != "" {
				detail = fmt.Sprintf("certbot issue failed (%s): %s", reason, actionable)
			} else if tail := certbot.RawErrorTail(res.Stderr, 6); tail != "" {
				// No structured Domain:/Detail: block matched — surface the raw
				// certbot output tail so last_error carries the real reason
				// instead of a bare "exit status 1" (GH#132).
				detail = fmt.Sprintf("certbot issue failed (%s): %s", reason, tail)
			} else if res.Reason != "" {
				detail = fmt.Sprintf("certbot issue failed (%s): %s", reason, detail)
			}
		}
		return sslMailIssueResponse{
			Outcome: "failed",
			Domain:  p.Domain,
			SANs:    effectiveSANs,
			Detail:  detail,
		}, nil
	}

	// Trigger deploy-hook so Stalwart picks up the new cert. Pass
	// the domain_id through JABALI_MAIL_DOMAIN_ID so the hook routes
	// to the mail-domain branch (step 4) and the push-cert
	// subprocess names the Stalwart Certificate entry uniquely per
	// tenant domain.
	if hookErr := runPanelCertDeployHook(ctx, "mail-domain", res.CertPath, p.DomainID); hookErr != nil {
		// Non-fatal: the cert IS issued + on disk; the hook will
		// run again on certbot's renewal timer. Surface so the
		// reconciler can warn.
		return sslMailIssueResponse{
			Outcome:     "issued",
			Domain:      p.Domain,
			SANs:        effectiveSANs,
			LineagePath: lineageDirFromCertPath(res.CertPath),
			IssuedAt:    res.IssuedAt.UTC().Format(time.RFC3339),
			ExpiresAt:   res.ExpiresAt.UTC().Format(time.RFC3339),
			Detail:      fmt.Sprintf("issued but deploy-hook failed: %v", hookErr),
		}, nil
	}

	return sslMailIssueResponse{
		Outcome:     "issued",
		Domain:      p.Domain,
		SANs:        effectiveSANs,
		LineagePath: lineageDirFromCertPath(res.CertPath),
		IssuedAt:    res.IssuedAt.UTC().Format(time.RFC3339),
		ExpiresAt:   res.ExpiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// lineageDirFromCertPath strips /fullchain.pem off a certbot
// --cert-path return so callers store the lineage DIR they need to
// pass back on renewal.
func lineageDirFromCertPath(certPath string) string {
	if i := strings.LastIndex(certPath, "/"); i > 0 {
		return certPath[:i]
	}
	return certPath
}

// runPanelCertDeployHook invokes the install.sh-shipped deploy-hook
// with the JABALI_PANEL_CERT_KIND env var set so it routes to the
// step 4 mail-domain branch. The hook script lives at the standard
// certbot location and is therefore always present on a Jabali host.
func runPanelCertDeployHook(ctx context.Context, kind, certPath, domainID string) error {
	hook := "/etc/letsencrypt/renewal-hooks/deploy/jabali-panel-cert.sh"
	if _, err := os.Stat(hook); err != nil {
		return fmt.Errorf("deploy-hook missing at %s: %w", hook, err)
	}
	lineage := lineageDirFromCertPath(certPath)
	cmd := execCommandContext(ctx, hook)
	env := append(os.Environ(),
		"JABALI_PANEL_CERT_KIND="+kind,
		"RENEWED_LINEAGE="+lineage,
	)
	if domainID != "" {
		// Disambiguates mail-vs-mail-domain on the deploy-hook side
		// (both lineages start with mail.) AND drives the unique
		// Stalwart Certificate naming via the push-cert subprocess.
		env = append(env, "JABALI_MAIL_DOMAIN_ID="+domainID)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deploy-hook exit: %w; output: %s", err, string(out))
	}
	return nil
}

// firstMailErrLine trims a multi-line error down to its first line so
// the reconciler / UI surface a clean summary instead of certbot's
// 30-line stderr dump.
func firstMailErrLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func init() {
	Default.Register("ssl.mail.issue", sslMailIssueHandler)
}
