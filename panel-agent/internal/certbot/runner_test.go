package certbot

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestIssueHappyPath tests successful certificate issuance.
func TestIssueHappyPath(t *testing.T) {
	tmp := t.TempDir()
	runner := setupFakeCertbot(t, tmp, "success")

	result, err := runner.Issue("example.com", "/var/www/example", "admin@example.com", false, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if result.CertPath != filepath.Join(tmp, "letsencrypt/live/example.com/fullchain.pem") {
		t.Errorf("unexpected cert path: %s", result.CertPath)
	}
	if result.Skipped {
		t.Error("cert should not be skipped")
	}
	if result.IssuedAt.IsZero() {
		t.Error("issued_at should not be zero")
	}
	if result.ExpiresAt.IsZero() {
		t.Error("expires_at should not be zero")
	}
}

// TestIssueWithStaging tests issuance with --staging flag.
func TestIssueWithStaging(t *testing.T) {
	tmp := t.TempDir()
	runner := setupFakeCertbot(t, tmp, "success")

	result, err := runner.Issue("example.com", "/var/www/example", "admin@example.com", true, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if !result.IssuedAt.Before(result.ExpiresAt) {
		t.Error("issued_at should be before expires_at")
	}
}

// TestRenewHappyPath tests successful certificate renewal.
func TestRenewHappyPath(t *testing.T) {
	tmp := t.TempDir()
	runner := setupFakeCertbot(t, tmp, "success")

	result, err := runner.Renew("example.com", false)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if result.Skipped {
		t.Error("cert renewal should not be skipped")
	}
}

// TestRenewSkipped tests when renewal is skipped (cert not due).
func TestRenewSkipped(t *testing.T) {
	tmp := t.TempDir()
	runner := setupFakeCertbot(t, tmp, "skipped")

	result, err := runner.Renew("example.com", false)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if !result.Skipped {
		t.Error("cert renewal should be skipped")
	}
}

// TestIssueSkippedWhenCertKept covers the panel-cert self-restart
// deadlock fix: when certbot keeps an existing still-valid cert
// (`--keep-until-expiring` no-op), Issue must report Skipped=true so
// ssl.panel.issue does NOT re-run the deploy-hook (which restarts
// jabali-panel — the reconciler that called it). A plain successful
// issuance (no keep message) must stay Skipped=false (asserted by
// TestIssueHappyPath).
func TestIssueSkippedWhenCertKept(t *testing.T) {
	tmp := t.TempDir()
	certbotPath := filepath.Join(tmp, "certbot")
	script := fmt.Sprintf(`#!/bin/bash
if [[ "$1" == "certonly" ]]; then
  domain="${3}"  # value after --cert-name (now: certonly --cert-name <domain> ...)
  mkdir -p "%s/letsencrypt/live/$domain"
  cat > "%s/letsencrypt/live/$domain/fullchain.pem" << 'EOF'
%s
EOF
  cat > "%s/letsencrypt/live/$domain/privkey.pem" << 'EOF'
-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg9r2Zcg3Q2D6+IRAc
1lixKYEJ6KFDCGv2GeLkF/sTrOGhRANCAATxBHvBCGCdtLTn5LmLVR8PJ/lDx8X/
ZRKRupd0Ey0vLHb7PkHYTJwXQxG3LpTdZ9EVYxGUAKW8gHdmxJP7SB1a
-----END PRIVATE KEY-----
EOF
  echo "Certificate not yet due for renewal; no action taken."
  exit 0
fi
exit 1
`, tmp, tmp, string(createTestCertPEM(t)), tmp)
	if err := os.WriteFile(certbotPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake certbot: %v", err)
	}
	runner := NewRunner()
	runner.Binary = certbotPath
	runner.LERoot = filepath.Join(tmp, "letsencrypt")
	runner.OpenSSL = "openssl"

	result, err := runner.Issue("example.com", "/var/www/example", "admin@example.com", false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if !result.Skipped {
		t.Error("Issue should report Skipped=true when certbot kept the existing cert")
	}
	if result.CertPath == "" || result.IssuedAt.IsZero() {
		t.Error("kept cert should still return CertPath + IssuedAt so the reconciler can mark issued")
	}
}

// TestRevokeHappyPath tests successful certificate revocation.
func TestRevokeHappyPath(t *testing.T) {
	tmp := t.TempDir()
	runner := setupFakeCertbot(t, tmp, "success")

	result, err := runner.Revoke("example.com", "superseded")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

// TestClassifyStderrWebrootUnreachable tests error classification for webroot issues.
func TestClassifyStderrWebrootUnreachable(t *testing.T) {
	stderr := []byte("Error 404: Not Found while accessing webroot")
	reason := classifyStderr(stderr)
	if reason != "webroot_unreachable" {
		t.Errorf("expected 'webroot_unreachable', got '%s'", reason)
	}
}

// TestClassifyStderrRateLimited tests error classification for rate limiting.
func TestClassifyStderrRateLimited(t *testing.T) {
	stderr := []byte("Error: too many certificates already issued for exact set of domains")
	reason := classifyStderr(stderr)
	if reason != "rate_limited" {
		t.Errorf("expected 'rate_limited', got '%s'", reason)
	}
}

// TestClassifyStderrDNSFailed tests error classification for DNS failures.
func TestClassifyStderrDNSFailed(t *testing.T) {
	stderr := []byte("Error: DNS problem: NXDOMAIN looking up example.com")
	reason := classifyStderr(stderr)
	if reason != "dns_resolve_failed" {
		t.Errorf("expected 'dns_resolve_failed', got '%s'", reason)
	}
}

// TestClassifyStderrUnknown tests fallback for unknown errors.
func TestClassifyStderrUnknown(t *testing.T) {
	stderr := []byte("Some random error that we don't recognize")
	reason := classifyStderr(stderr)
	if reason != "unknown" {
		t.Errorf("expected 'unknown', got '%s'", reason)
	}
}

// TestTruncateStderr tests stderr truncation.
func TestTruncateStderr(t *testing.T) {
	longStderr := ""
	for i := 0; i < 1000; i++ {
		longStderr += "x"
	}
	truncated := truncateStderr(longStderr, 100)
	if len(truncated) != 100 {
		t.Errorf("expected truncated stderr to be 100 bytes, got %d", len(truncated))
	}
	// Should be the tail
	if truncated != longStderr[len(longStderr)-100:] {
		t.Error("truncated stderr should be the tail")
	}
}

// TestParseCertValidity tests certificate validity parsing.
func TestParseCertValidity(t *testing.T) {
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "cert.pem")

	// Create a self-signed test certificate
	testCert := createTestCertPEM(t)
	if err := os.WriteFile(certPath, testCert, 0644); err != nil {
		t.Fatalf("failed to write test cert: %v", err)
	}

	issuedAt, expiresAt, err := ParseCertValidity(certPath)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if issuedAt.IsZero() {
		t.Error("issued_at should not be zero")
	}
	if expiresAt.IsZero() {
		t.Error("expires_at should not be zero")
	}
	if !issuedAt.Before(expiresAt) {
		t.Error("issued_at should be before expires_at")
	}
}

// setupArgCapturingCertbot installs a fake certbot that captures its argv
// into tmp/args.log on every invocation and writes a valid cert to
// LERoot/live/<domain>/. Used by SAN-expansion tests to assert the exact
// -d / --expand flags the runner emits.
func setupArgCapturingCertbot(t *testing.T, tmp string) *Runner {
	certbotPath := filepath.Join(tmp, "certbot")
	argsLog := filepath.Join(tmp, "args.log")

	// The script dumps all positional args (one per line) then creates
	// the expected cert files so the runner's post-issue read succeeds.
	script := fmt.Sprintf(`#!/bin/bash
for a in "$@"; do echo "$a" >> %q; done
echo "----" >> %q
# cert-name is passed explicitly via --cert-name for BOTH certonly
# and renew, so derive it the same way for either action.
cert_name=""
i=1
for a in "$@"; do
  if [[ "$a" == "--cert-name" ]]; then
    cert_name="${@:$((i+1)):1}"
    break
  fi
  i=$((i+1))
done
if [[ -n "$cert_name" ]]; then
  mkdir -p "%s/letsencrypt/live/$cert_name"
  cat > "%s/letsencrypt/live/$cert_name/fullchain.pem" <<'EOF'
%s
EOF
fi
exit 0
`, argsLog, argsLog, tmp, tmp, string(createTestCertPEM(t)))

	if err := os.WriteFile(certbotPath, []byte(script), 0755); err != nil {
		t.Fatalf("create fake certbot: %v", err)
	}
	r := NewRunner()
	r.Binary = certbotPath
	r.LERoot = filepath.Join(tmp, "letsencrypt")
	return r
}

// readCapturedArgs returns every argv batch recorded by
// setupArgCapturingCertbot. Each entry is one invocation's args in order.
func readCapturedArgs(t *testing.T, tmp string) [][]string {
	bs, err := os.ReadFile(filepath.Join(tmp, "args.log"))
	if err != nil {
		t.Fatalf("read args.log: %v", err)
	}
	lines := strings.Split(string(bs), "\n")
	var batches [][]string
	var cur []string
	for _, l := range lines {
		if l == "----" {
			if cur != nil {
				batches = append(batches, cur)
				cur = nil
			}
			continue
		}
		if l == "" {
			continue
		}
		cur = append(cur, l)
	}
	return batches
}

// TestIssueExtraHostnamesAreAddedAsDashD asserts every extra hostname is
// passed as a separate -d flag after the primary domain's -d, preserving
// input order and deduping.
func TestIssueExtraHostnamesAreAddedAsDashD(t *testing.T) {
	tmp := t.TempDir()
	r := setupArgCapturingCertbot(t, tmp)

	_, err := r.Issue("example.com", "/var/www/example", "a@b.com", false,
		[]string{"mail.example.com", "example.com", "autoconfig.example.com"}) // dup included to test dedup
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	batches := readCapturedArgs(t, tmp)
	if len(batches) != 1 {
		t.Fatalf("expected 1 certbot invocation, got %d", len(batches))
	}
	args := batches[0]

	// Collect -d values in order.
	var gotD []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-d" && i+1 < len(args) {
			gotD = append(gotD, args[i+1])
		}
	}
	wantD := []string{"example.com", "mail.example.com", "autoconfig.example.com"}
	if len(gotD) != len(wantD) {
		t.Fatalf("-d values: got %v, want %v", gotD, wantD)
	}
	for i, w := range wantD {
		if gotD[i] != w {
			t.Errorf("-d[%d]: got %q, want %q", i, gotD[i], w)
		}
	}

	// No existing cert → --expand should NOT be present.
	for _, a := range args {
		if a == "--expand" {
			t.Error("--expand should not be set on first issuance (no existing cert)")
		}
	}
}

// TestIssueAddsExpandWhenExistingCertMissesSAN asserts --expand is added
// iff the existing cert at LERoot/live/<domain>/fullchain.pem doesn't
// cover every requested SAN.
func TestIssueAddsExpandWhenExistingCertMissesSAN(t *testing.T) {
	tmp := t.TempDir()
	r := setupArgCapturingCertbot(t, tmp)

	// Pre-populate an existing cert that only covers example.com (via
	// createTestCertPEM which sets DNSNames: []string{"example.com"}).
	certDir := filepath.Join(tmp, "letsencrypt", "live", "example.com")
	if err := os.MkdirAll(certDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "fullchain.pem"), createTestCertPEM(t), 0644); err != nil {
		t.Fatal(err)
	}
	writeManagedRenewalConf(t, filepath.Join(tmp, "letsencrypt"), "example.com")

	_, err := r.Issue("example.com", "/var/www/example", "a@b.com", false,
		[]string{"mail.example.com"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	batches := readCapturedArgs(t, tmp)
	args := batches[0]
	foundExpand := false
	for _, a := range args {
		if a == "--expand" {
			foundExpand = true
			break
		}
	}
	if !foundExpand {
		t.Errorf("--expand expected (existing cert lacks mail.example.com); args=%v", args)
	}
}

// TestIssueSkipsExpandWhenExistingCertCovers asserts --expand is not
// added when the existing cert already covers every requested SAN.
func TestIssueSkipsExpandWhenExistingCertCovers(t *testing.T) {
	tmp := t.TempDir()
	r := setupArgCapturingCertbot(t, tmp)

	// Pre-populate a cert covering example.com + mail.example.com.
	certPEM := createTestCertWithSANs(t, []string{"example.com", "mail.example.com"})
	certDir := filepath.Join(tmp, "letsencrypt", "live", "example.com")
	if err := os.MkdirAll(certDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "fullchain.pem"), certPEM, 0644); err != nil {
		t.Fatal(err)
	}
	writeManagedRenewalConf(t, filepath.Join(tmp, "letsencrypt"), "example.com")

	_, err := r.Issue("example.com", "/var/www/example", "a@b.com", false,
		[]string{"mail.example.com"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	args := readCapturedArgs(t, tmp)[0]
	for _, a := range args {
		if a == "--expand" {
			t.Errorf("--expand should NOT be set when existing cert covers all SANs; args=%v", args)
		}
	}
}

// TestIssuePinsLineageWithCertName is the GH #213 regression gate.
// certonly MUST pass --cert-name <domain> so certbot targets the
// <domain> lineage explicitly and skips the cross-lineage overlap
// search that otherwise prompts "expand and replace?" (non-interactive
// death) when the mail.<domain> lineage already owns mail./autoconfig./
// autodiscover. SANs.
func TestIssuePinsLineageWithCertName(t *testing.T) {
	tmp := t.TempDir()
	r := setupArgCapturingCertbot(t, tmp)

	if _, err := r.Issue("example.com", "/var/www/example", "a@b.com", false,
		[]string{"mail.example.com", "autoconfig.example.com"}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	args := readCapturedArgs(t, tmp)[0]
	found := false
	for i, a := range args {
		if a == "--cert-name" {
			if i+1 >= len(args) || args[i+1] != "example.com" {
				t.Fatalf("--cert-name must be followed by example.com; args=%v", args)
			}
			found = true
		}
	}
	if !found {
		t.Errorf("certonly must emit --cert-name <domain> (GH #213); args=%v", args)
	}
}

// createTestCertWithSANs builds a test PEM with the given DNSNames.
// Kept out of setupFakeCertbot's default helper because the SAN
// expansion tests need to control the cert's DNSNames explicitly.
func createTestCertWithSANs(t *testing.T, sans []string) []byte {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: sans[0]},
		NotBefore:    now,
		NotAfter:     now.Add(90 * 24 * time.Hour),
		DNSNames:     sans,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// setupFakeCertbot creates a fake certbot binary and configures the runner.
func setupFakeCertbot(t *testing.T, tmp string, mode string) *Runner {
	certbotPath := filepath.Join(tmp, "certbot")

	// Create a minimal fake certbot script
	script := fmt.Sprintf(`#!/bin/bash
# Fake certbot for testing
action=$1
if [[ "$action" == "certonly" ]]; then
  # domain = value after --cert-name (now: certonly --cert-name <domain> ...)
  domain=""
  i=1
  for a in "$@"; do
    if [[ "$a" == "--cert-name" ]]; then domain="${@:$((i+1)):1}"; break; fi
    i=$((i+1))
  done
  mkdir -p "%s/letsencrypt/live/$domain"
  # Create test certificate
  cat > "%s/letsencrypt/live/$domain/fullchain.pem" << 'EOF'
%s
EOF
  cat > "%s/letsencrypt/live/$domain/privkey.pem" << 'EOF'
-----BEGIN PRIVATE KEY-----
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg9r2Zcg3Q2D6+IRAc
1lixKYEJ6KFDCGv2GeLkF/sTrOGhRANCAATxBHvBCGCdtLTn5LmLVR8PJ/lDx8X/
ZRKRupd0Ey0vLHb7PkHYTJwXQxG3LpTdZ9EVYxGUAKW8gHdmxJP7SB1a
-----END PRIVATE KEY-----
EOF
  exit 0
elif [[ "$action" == "renew" ]]; then
  cert_name="${3}"
  if [[ "%s" == "skipped" ]]; then
    echo "Certificate not due for renewal" >&2
    mkdir -p "%s/letsencrypt/live/$cert_name"
    cat > "%s/letsencrypt/live/$cert_name/fullchain.pem" << 'EOF'
%s
EOF
  else
    mkdir -p "%s/letsencrypt/live/$cert_name"
    cat > "%s/letsencrypt/live/$cert_name/fullchain.pem" << 'EOF'
%s
EOF
  fi
  exit 0
elif [[ "$action" == "revoke" ]]; then
  exit 0
elif [[ "$action" == "delete" ]]; then
  exit 0
fi
exit 1
`, tmp, tmp, string(createTestCertPEM(t)), tmp, mode, tmp, tmp, string(createTestCertPEM(t)), tmp, tmp, string(createTestCertPEM(t)))

	if err := os.WriteFile(certbotPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to create fake certbot: %v", err)
	}

	runner := NewRunner()
	runner.Binary = certbotPath
	runner.LERoot = filepath.Join(tmp, "letsencrypt")
	runner.OpenSSL = "openssl"

	return runner
}

// createTestCertPEM creates a self-signed certificate PEM for testing.
func createTestCertPEM(t *testing.T) []byte {
	// Generate private key
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	// Create certificate template
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "example.com",
		},
		NotBefore: now,
		NotAfter:  now.Add(90 * 24 * time.Hour), // 90-day cert like Let's Encrypt
		DNSNames:  []string{"example.com"},
	}

	// Self-sign
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("failed to create cert: %v", err)
	}

	// Encode to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	return certPEM
}

// TestIssueError_Failure tests Issue when certbot fails.
func TestIssueError_Failure(t *testing.T) {
	runner := NewRunner()
	runner.Binary = "/nonexistent/certbot"

	result, err := runner.Issue("example.com", "/var/www/example", "admin@example.com", false, nil)

	if err == nil {
		t.Fatal("expected error when certbot binary doesn't exist")
	}
	if result == nil {
		t.Fatal("expected result even on error")
	}
}

// TestRenewError_Failure tests Renew when certbot fails.
func TestRenewError_Failure(t *testing.T) {
	runner := NewRunner()
	runner.Binary = "/nonexistent/certbot"

	result, err := runner.Renew("example.com", false)

	if err == nil {
		t.Fatal("expected error when certbot binary doesn't exist")
	}
	if result == nil {
		t.Fatal("expected result even on error")
	}
}

// TestRenewError_WithForce tests Renew with force flag when it fails.
func TestRenewError_WithForce(t *testing.T) {
	runner := NewRunner()
	runner.Binary = "/nonexistent/certbot"

	result, err := runner.Renew("example.com", true)

	if err == nil {
		t.Fatal("expected error")
	}
	if result == nil {
		t.Fatal("expected result")
	}
}

// TestRevokeError_Failure tests Revoke when certbot fails.
func TestRevokeError_Failure(t *testing.T) {
	runner := NewRunner()
	runner.Binary = "/nonexistent/certbot"

	result, err := runner.Revoke("example.com", "superseded")

	if err == nil {
		t.Fatal("expected error when certbot binary doesn't exist")
	}
	if result == nil {
		t.Fatal("expected result even on error")
	}
}

// TestParseCertValidity_MissingFile tests ParseCertValidity with non-existent file.
func TestParseCertValidity_MissingFile(t *testing.T) {
	_, _, err := ParseCertValidity("/nonexistent/path/cert.pem")

	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestParseCertValidity_InvalidPEM tests ParseCertValidity with invalid PEM content.
func TestParseCertValidity_InvalidPEM(t *testing.T) {
	tmp := t.TempDir()
	certPath := filepath.Join(tmp, "invalid.pem")
	if err := os.WriteFile(certPath, []byte("not a valid certificate"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, _, err := ParseCertValidity(certPath)

	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

// TestTruncateStderr_LargeString tests truncation of large stderr.
func TestTruncateStderr_LargeString(t *testing.T) {
	large := "prefix " + strings.Repeat("x", 10000) + " suffix"
	result := truncateStderr(large, 100)

	if len(result) > 110 { // 100 + padding for "..."
		t.Errorf("expected truncated result, got %d chars", len(result))
	}
}
