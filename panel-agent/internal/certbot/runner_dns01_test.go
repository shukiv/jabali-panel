package certbot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupDNS01FakeCertbot writes a fake certbot that records its argv to
// args.txt and materialises a lineage under LERoot/live/<cert-name>/ so
// readCert succeeds. Reuses the same self-signed PEM the other runner
// tests generate (createTestCertPEM).
func setupDNS01FakeCertbot(t *testing.T, tmp string) *Runner {
	t.Helper()
	certbotPath := filepath.Join(tmp, "certbot")
	script := fmt.Sprintf(`#!/bin/bash
printf '%%s\n' "$@" > %s/args.txt
name=""
i=1
for a in "$@"; do
  if [[ "$a" == "--cert-name" ]]; then name="${@:$((i+1)):1}"; break; fi
  i=$((i+1))
done
mkdir -p "%s/letsencrypt/live/$name"
cat > "%s/letsencrypt/live/$name/fullchain.pem" << 'EOF'
%s
EOF
touch "%s/letsencrypt/live/$name/privkey.pem"
exit 0
`, tmp, tmp, tmp, string(createTestCertPEM(t)), tmp)
	if err := os.WriteFile(certbotPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Runner{
		Binary:  certbotPath,
		OpenSSL: "openssl",
		LERoot:  filepath.Join(tmp, "letsencrypt"),
	}
}

func capturedArgs(t *testing.T, tmp string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(tmp, "args.txt"))
	if err != nil {
		t.Fatalf("fake certbot did not record args: %v", err)
	}
	return string(b)
}

func TestIssueDNS01ArgShape(t *testing.T) {
	tmp := t.TempDir()
	runner := setupDNS01FakeCertbot(t, tmp)

	result, err := runner.IssueDNS01(
		"wildcard.preview.host.tld",
		[]string{"*.preview.host.tld"},
		"admin@host.tld",
		false,
		"/usr/local/bin/jabali-panel dns acme-hook auth",
		"/usr/local/bin/jabali-panel dns acme-hook cleanup",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CertPath == "" || result.KeyPath == "" {
		t.Fatalf("missing cert/key paths in result: %+v", result)
	}

	args := capturedArgs(t, tmp)
	for _, want := range []string{
		"--manual",
		"--preferred-challenges\ndns",
		"--manual-auth-hook\n/usr/local/bin/jabali-panel dns acme-hook auth",
		"--manual-cleanup-hook\n/usr/local/bin/jabali-panel dns acme-hook cleanup",
		"-d\n*.preview.host.tld",
		"--cert-name\nwildcard.preview.host.tld",
		"--key-type\nrsa",
		"--non-interactive",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("certbot args missing %q:\n%s", want, args)
		}
	}
	// DNS-01 must not carry a webroot — that is the HTTP-01 authenticator,
	// and certbot rejects mixing the two.
	for _, forbidden := range []string{"--webroot", "-w\n"} {
		if strings.Contains(args, forbidden) {
			t.Errorf("certbot args must not contain %q for dns-01:\n%s", forbidden, args)
		}
	}
	if strings.Contains(args, "--staging") {
		t.Error("staging flag present on a non-staging issue")
	}
}

func TestIssueDNS01StagingAndDedup(t *testing.T) {
	tmp := t.TempDir()
	runner := setupDNS01FakeCertbot(t, tmp)

	_, err := runner.IssueDNS01(
		"wildcard.example.com",
		[]string{"example.com", "*.example.com", "example.com"},
		"a@example.com",
		true,
		"auth-hook", "cleanup-hook",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	args := capturedArgs(t, tmp)
	if !strings.Contains(args, "--staging") {
		t.Error("staging flag missing")
	}
	if strings.Count(args, "example.com\n")+strings.Count(args, "example.com") == 0 {
		t.Fatal("no -d entries recorded")
	}
	// The duplicate apex must collapse to a single -d.
	if got := strings.Count(args, "-d\nexample.com"); got != 1 {
		t.Errorf("apex -d appears %d times, want 1:\n%s", got, args)
	}
	if got := strings.Count(args, "-d\n*.example.com"); got != 1 {
		t.Errorf("wildcard -d appears %d times, want 1:\n%s", got, args)
	}
}

func TestIssueDNS01RequiresSANs(t *testing.T) {
	runner := NewRunner()
	if _, err := runner.IssueDNS01("x", nil, "a@b.co", false, "h", "h"); err == nil {
		t.Fatal("expected an error for empty SAN list")
	}
}
