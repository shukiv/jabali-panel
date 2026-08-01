package certbot

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Result contains the outcome of a certbot operation.
type Result struct {
	CertPath  string    `json:"cert_path"`
	KeyPath   string    `json:"key_path"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Skipped   bool      `json:"skipped"`
	Reason    string    `json:"reason"`
	Stderr    string    `json:"stderr,omitempty"`
}

// Runner manages certbot operations.
type Runner struct {
	Binary  string
	OpenSSL string
	Env     []string
	LERoot  string
}

// NewRunner creates a default certbot runner.
func NewRunner() *Runner {
	return &Runner{
		Binary:  "certbot",
		OpenSSL: "openssl",
		Env:     nil,
		LERoot:  "/etc/letsencrypt",
	}
}

// Issue runs certbot to issue a new certificate.
//
// extraHostnames adds extra SANs beyond the primary domain. Added as
// repeated -d flags after the primary domain. Duplicates (including the
// primary domain) are skipped.
//
// When an existing cert at LERoot/live/<domain> already covers a strict
// subset of the requested SAN set, Issue adds --expand so certbot
// re-issues a superset cert. Without --expand, --keep-until-expiring
// would silently reuse the narrower existing cert and the new
// hostnames would never appear on the wire.
func (r *Runner) Issue(domain, webroot, email string, staging bool, extraHostnames []string) (*Result, error) {
	// A prior custom-cert install (ssl.install.custom) or a half-broken lineage
	// can leave LERoot/live/<domain>/ as a plain dir with NO
	// renewal/<domain>.conf. `certbot certonly --cert-name <domain>` then can't
	// adopt that dir and issues a duplicate <domain>-0001 lineage instead
	// (GH #738). Clear the stale live/archive dirs first — only when the name is
	// NOT certbot-managed — so issuance lands under the exact name.
	r.clearStaleLineageDir(domain)

	args := []string{
		"certonly",
		// --cert-name pins the lineage explicitly. Without it certbot
		// derives the name from the first -d (also `domain`, so the
		// name is UNCHANGED) — but it ALSO runs a cross-lineage overlap
		// search, and when another lineage already holds some requested
		// SANs (e.g. the mail.<domain> cert owns mail./autoconfig./
		// autodiscover.) it prompts "expand and replace?" which dies
		// under --non-interactive (GH #213: tenant cert stuck pending
		// because the mail cert issued first). Naming the lineage skips
		// that search; the overlap across the <domain> and mail.<domain>
		// lineages is harmless redundancy.
		"--cert-name", domain,
		"--webroot",
		"-w", webroot,
		"-d", domain,
	}
	seen := map[string]struct{}{domain: {}}
	requested := []string{domain}
	for _, h := range extraHostnames {
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		requested = append(requested, h)
		args = append(args, "-d", h)
	}
	args = append(args,
		"-m", email,
		"--agree-tos",
		"--non-interactive",
		"--keep-until-expiring",
		// RSA, not certbot's ECDSA default: an ECDSA-only origin cert
		// breaks Cloudflare-fronted (and other RSA-only) clients —
		// CF's origin pull negotiates an RSA cipher suite, finds no
		// shared cipher with an EC cert, and fails the TLS handshake
		// (error 525). RSA is universally compatible.
		"--key-type", "rsa",
	)

	// Expand-if-needed: when the existing cert's SAN set doesn't cover
	// everything in `requested`, add --expand so certbot issues a
	// superset. No-op when no existing cert yet (first issuance).
	if existingCertMissingAny(fmt.Sprintf("%s/live/%s/fullchain.pem", r.LERoot, domain), requested) {
		args = append(args, "--expand")
	}

	if staging {
		args = append(args, "--staging")
	}

	cmd := exec.Command(r.Binary, args...)
	cmd.Env = r.Env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stderrText := stderr.String()
	// certbot may print the actionable failure block ("Domain:/Type:/Detail:")
	// to EITHER stdout or stderr depending on version + auth plugin. Classify
	// and surface from the combined stream so the real reason isn't lost when
	// it lands on stdout (GH#132: a fresh install still showed only "exit
	// status 1" because we only looked at stderr and the caller then discarded
	// even that).
	combinedOutput := stdout.String() + "\n" + stderrText
	// certbot prints these to stdout when --keep-until-expiring finds an
	// existing cert that is still valid: nothing was renewed, so the
	// on-disk cert (and whatever is already deployed) is unchanged. The
	// caller uses this to skip the deploy-hook — re-running it would
	// needlessly restart jabali-panel on every reconcile tick (the
	// panel-cert self-restart deadlock).
	noopCertbot := func(out string) bool {
		for _, m := range []string{
			"not yet due for renewal",
			"Keeping the existing certificate",
			"no action taken",
		} {
			if strings.Contains(out, m) {
				return true
			}
		}
		return false
	}(stdout.String() + "\n" + stderrText)

	// Classify the error if present
	reason := ""
	if err != nil {
		reason = classifyStderr([]byte(combinedOutput))
	}

	// Try to read the cert regardless of error — certbot may partially succeed
	cert, certErr := r.readCert(domain)
	if err != nil && certErr != nil {
		// Both issue and read failed
		stderrTail := truncateStderr(combinedOutput, 4096)
		return &Result{
			Reason: reason,
			Stderr: stderrTail,
		}, fmt.Errorf("certbot issue failed: %w", err)
	}

	if certErr != nil {
		// Issue succeeded (no error), but we can't read the cert
		stderrTail := truncateStderr(combinedOutput, 4096)
		return &Result{
			Reason: "unknown",
			Stderr: stderrTail,
		}, fmt.Errorf("failed to read issued certificate: %w", certErr)
	}

	// Success. Skipped=true when certbot kept an existing still-valid
	// cert (no renewal); the caller then skips the deploy-hook.
	return &Result{
		CertPath:  cert.CertPath,
		KeyPath:   cert.KeyPath,
		IssuedAt:  cert.IssuedAt,
		ExpiresAt: cert.ExpiresAt,
		Skipped:   noopCertbot,
		Reason:    "",
	}, nil
}

// IssueDNS01 runs certbot with the manual DNS-01 authenticator. This is
// the only challenge type Let's Encrypt accepts for wildcard names, so it
// is what backs `*.<domain>` shared certificates and the panel's
// `*.preview.<hostname>` cert.
//
// certName pins the lineage directory (LERoot/live/<certName>) and MUST
// NOT contain `*` — pass a sanitised name (e.g. "wildcard.example.com")
// and put the wildcard in sans. sans is the full SAN list including the
// primary name; entries may carry a single leading "*." label.
//
// authHook/cleanupHook are the commands certbot runs per name to place
// and remove the _acme-challenge TXT record. On a jabali box they invoke
// the panel binary's `dns acme-hook` subcommand, which writes the record
// through the panel's DNS tables and pushes the zone to PowerDNS —
// keeping the panel DB authoritative even for ephemeral challenge rows.
// certbot persists both hooks in the renewal conf, so `certbot renew`
// keeps working without the caller re-supplying them.
func (r *Runner) IssueDNS01(certName string, sans []string, email string, staging bool, authHook, cleanupHook string) (*Result, error) {
	if len(sans) == 0 {
		return nil, fmt.Errorf("certbot dns-01: at least one SAN required")
	}
	r.clearStaleLineageDir(certName)

	args := []string{
		"certonly",
		"--cert-name", certName,
		"--manual",
		"--preferred-challenges", "dns",
		"--manual-auth-hook", authHook,
		"--manual-cleanup-hook", cleanupHook,
	}
	seen := map[string]struct{}{}
	requested := make([]string, 0, len(sans))
	for _, h := range sans {
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		requested = append(requested, h)
		args = append(args, "-d", h)
	}
	args = append(args,
		"-m", email,
		"--agree-tos",
		"--non-interactive",
		"--keep-until-expiring",
		// RSA for the same Cloudflare-origin compatibility reason as
		// the webroot path above (error 525 on EC-only chains).
		"--key-type", "rsa",
	)
	if existingCertMissingAny(fmt.Sprintf("%s/live/%s/fullchain.pem", r.LERoot, certName), requested) {
		args = append(args, "--expand")
	}
	if staging {
		args = append(args, "--staging")
	}

	cmd := exec.Command(r.Binary, args...)
	cmd.Env = r.Env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	combinedOutput := stdout.String() + "\n" + stderr.String()
	noop := func(out string) bool {
		for _, m := range []string{
			"not yet due for renewal",
			"Keeping the existing certificate",
			"no action taken",
		} {
			if strings.Contains(out, m) {
				return true
			}
		}
		return false
	}(combinedOutput)

	reason := ""
	if err != nil {
		reason = classifyStderr([]byte(combinedOutput))
	}

	cert, certErr := r.readCert(certName)
	if err != nil && certErr != nil {
		return &Result{
			Reason: reason,
			Stderr: truncateStderr(combinedOutput, 4096),
		}, fmt.Errorf("certbot dns-01 issue failed: %w", err)
	}
	if certErr != nil {
		return &Result{
			Reason: "unknown",
			Stderr: truncateStderr(combinedOutput, 4096),
		}, fmt.Errorf("failed to read issued certificate: %w", certErr)
	}

	return &Result{
		CertPath:  cert.CertPath,
		KeyPath:   cert.KeyPath,
		IssuedAt:  cert.IssuedAt,
		ExpiresAt: cert.ExpiresAt,
		Skipped:   noop,
	}, nil
}

// Renew runs certbot to renew an existing certificate.
func (r *Runner) Renew(domain string, force bool) (*Result, error) {
	args := []string{
		"renew",
		"--cert-name", domain,
		"--non-interactive",
		"--no-random-sleep-on-renew",
		// Flip any existing ECDSA lineage to RSA on renewal (see Issue).
		"--key-type", "rsa",
	}

	if force {
		args = append(args, "--force-renewal")
	}

	cmd := exec.Command(r.Binary, args...)
	cmd.Env = r.Env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	stderrText := stderr.String()

	// Check if renewal was skipped (exit 0 but no actual renewal happened)
	skipped := strings.Contains(stderrText, "not due for renewal")

	if err != nil {
		reason := classifyStderr([]byte(stderrText))
		stderrTail := truncateStderr(stderrText, 4096)
		return &Result{
			Reason: reason,
			Stderr: stderrTail,
		}, fmt.Errorf("certbot renew failed: %w", err)
	}

	// Read cert to get current validity
	cert, certErr := r.readCert(domain)
	if certErr != nil {
		stderrTail := truncateStderr(stderrText, 4096)
		return &Result{
			Skipped: skipped,
			Reason:  "unknown",
			Stderr:  stderrTail,
		}, fmt.Errorf("failed to read certificate after renew: %w", certErr)
	}

	return &Result{
		CertPath:  cert.CertPath,
		KeyPath:   cert.KeyPath,
		IssuedAt:  cert.IssuedAt,
		ExpiresAt: cert.ExpiresAt,
		Skipped:   skipped,
		Reason:    "",
	}, nil
}

// Revoke runs certbot to revoke a certificate.
func (r *Runner) Revoke(domain string, reason string) (*Result, error) {
	if reason == "" {
		reason = "unspecified"
	}

	args := []string{
		"revoke",
		"--cert-name", domain,
		"--reason", reason,
		"--non-interactive",
	}

	cmd := exec.Command(r.Binary, args...)
	cmd.Env = r.Env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	if err != nil {
		stderrText := stderr.String()
		stderrTail := truncateStderr(stderrText, 4096)
		return &Result{
			Reason: classifyStderr([]byte(stderrText)),
			Stderr: stderrTail,
		}, fmt.Errorf("certbot revoke failed: %w", err)
	}

	// Delete the cert after revoking
	deleteCmd := exec.Command(r.Binary, "delete", "--cert-name", domain, "--non-interactive")
	deleteCmd.Env = r.Env

	var deleteStderr bytes.Buffer
	deleteCmd.Stderr = &deleteStderr

	if err := deleteCmd.Run(); err != nil {
		// Log but don't fail if delete fails; cert is already revoked
		fmt.Printf("warning: certbot delete failed after revoke: %v\n", err)
	}

	return &Result{
		Reason: "",
	}, nil
}

// readCert reads certificate validity from the issued cert PEM.
func (r *Runner) readCert(domain string) (*struct {
	CertPath  string
	KeyPath   string
	IssuedAt  time.Time
	ExpiresAt time.Time
}, error) {
	certPath := fmt.Sprintf("%s/live/%s/fullchain.pem", r.LERoot, domain)
	keyPath := fmt.Sprintf("%s/live/%s/privkey.pem", r.LERoot, domain)

	issuedAt, expiresAt, err := ParseCertValidity(certPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse cert validity: %w", err)
	}

	return &struct {
		CertPath  string
		KeyPath   string
		IssuedAt  time.Time
		ExpiresAt time.Time
	}{
		CertPath:  certPath,
		KeyPath:   keyPath,
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
	}, nil
}

// ParseCertValidity parses the issued and expiration dates from a PEM certificate file.
func ParseCertValidity(pemPath string) (issuedAt, expiresAt time.Time, err error) {
	// Read the PEM file
	pemBytes, err := exec.Command("cat", pemPath).Output()
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("failed to read cert file: %w", err)
	}

	// Parse the certificate using Go's x509 package
	cert, err := parsePEM(pemBytes)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("failed to parse PEM: %w", err)
	}

	return cert.NotBefore, cert.NotAfter, nil
}

// parsePEM extracts the first certificate from a PEM block.
func parsePEM(pemBytes []byte) (*x509.Certificate, error) {
	// Use Go's pem package to decode the PEM block
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM certificate found")
	}

	// Parse the DER-encoded certificate
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("x509 parse failed: %w", err)
	}

	return cert, nil
}

// clearStaleLineageDir removes a stale, non-certbot-managed live/archive dir
// for `domain` under LERoot so `certbot certonly --cert-name <domain>` issues
// under the exact name instead of spawning a <domain>-0001 duplicate (GH #738).
// It only acts when there is NO renewal/<domain>.conf — a real certbot-managed
// lineage is left untouched so certbot can renew/reuse it. Best-effort: removal
// errors are non-fatal (issuance surfaces any real problem).
func (r *Runner) clearStaleLineageDir(domain string) {
	renewalConf := filepath.Join(r.LERoot, "renewal", domain+".conf")
	if _, err := os.Stat(renewalConf); err == nil {
		return // certbot-managed lineage — let certbot renew/reuse it
	}
	_ = os.RemoveAll(filepath.Join(r.LERoot, "live", domain))
	_ = os.RemoveAll(filepath.Join(r.LERoot, "archive", domain))
}

// existingCertMissingAny reads the cert at certPath (if any) and returns
// true iff its DNSNames don't cover every entry in `want`. When no cert
// exists (first issuance) or the cert is unparseable, returns false
// — there's nothing to expand. The intent is narrow: decide whether
// certbot needs --expand, never to block issuance on cert-read errors.
func existingCertMissingAny(certPath string, want []string) bool {
	pemBytes, err := exec.Command("cat", certPath).Output()
	if err != nil {
		return false
	}
	cert, err := parsePEM(pemBytes)
	if err != nil {
		return false
	}
	have := make(map[string]struct{}, len(cert.DNSNames))
	for _, n := range cert.DNSNames {
		have[n] = struct{}{}
	}
	for _, w := range want {
		if _, ok := have[w]; !ok {
			return true
		}
	}
	return false
}

// classifyStderr maps certbot errors to reason codes.
func classifyStderr(stderr []byte) string {
	stderrStr := string(stderr)

	patterns := map[string]string{
		"404.*Not Found|Fetching|webroot unreachable": "webroot_unreachable",
		"too many certificates already issued":        "rate_limited",
		"DNS problem.*NXDOMAIN":                       "dns_resolve_failed",
		"Invalid email address":                       "invalid_email",
		"Permission denied":                           "permission_denied",
	}

	for pattern, reason := range patterns {
		if matched, _ := regexp.MatchString(pattern, stderrStr); matched {
			return reason
		}
	}

	return "unknown"
}

// truncateStderr truncates stderr to a maximum size, keeping the tail.
func truncateStderr(stderr string, maxBytes int) string {
	if len(stderr) <= maxBytes {
		return stderr
	}
	// Keep the last maxBytes characters (the tail)
	return stderr[len(stderr)-maxBytes:]
}

// ExtractActionableDetail pulls the "Domain: X / Type: Y / Detail: Z"
// block certbot prints right before "Some challenges have failed."
// Returns the multi-line string when found, "" otherwise. Used by
// ssl_issue + ssl_panel_issue handlers so the panel UI's last_error
// Modal carries text the operator can act on without VPS shell
// access to /var/log/letsencrypt/letsencrypt.log.
func ExtractActionableDetail(stderr string) string {
	lines := strings.Split(stderr, "\n")
	var out []string
	capture := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "Domain:") || strings.HasPrefix(trim, "Type:") || strings.HasPrefix(trim, "Detail:") || strings.HasPrefix(trim, "Hint:") {
			capture = true
			out = append(out, trim)
			continue
		}
		if capture {
			// continuation lines are indented; stop on first blank or
			// non-indented line.
			if trim == "" || (len(line) > 0 && line[0] != ' ' && line[0] != '\t') {
				break
			}
			out = append(out, trim)
		}
	}
	return strings.Join(out, " | ")
}

// RawErrorTail returns the last up-to-n non-empty, non-noise lines of certbot's
// output, joined with " | ". Used as the fallback when ExtractActionableDetail
// finds no structured Domain:/Detail: block — so the operator still sees the
// real certbot error (e.g. a rate-limit URL, a plugin error, an unexpected
// traceback) instead of a bare "exit status 1". GH#132.
func RawErrorTail(output string, n int) string {
	if n <= 0 {
		n = 6
	}
	lines := strings.Split(output, "\n")
	var kept []string
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		// Drop certbot's boilerplate framing so the tail is signal, not dashes.
		if strings.HasPrefix(t, "- - -") || strings.HasPrefix(t, "* * *") {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, " | ")
}
