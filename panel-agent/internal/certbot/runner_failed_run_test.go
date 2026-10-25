package certbot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A failed certbot run must not be reported as issued just because an older
// cert still sits in the lineage. --keep-until-expiring renewals of a shared
// wildcard (*.preview.<hostname>) failed every day on a box whose zone was not
// publicly delegated, and the old leaf came back as a fresh issuance — the
// panel logged "issued" with the old expiry and the cert would have lapsed
// without a single error shown.

const nxdomainFailure = `Certbot failed to authenticate some domains (authenticator: manual). The Certificate Authority reported these problems:
  Domain: preview.example.com
  Type:   dns
  Detail: DNS problem: NXDOMAIN looking up TXT for _acme-challenge.preview.example.com - check that a DNS record exists for this domain
`

// setupLineage writes an existing certbot-managed lineage (renewal conf
// included, so clearStaleLineageDir leaves it alone).
func setupLineage(t *testing.T, leRoot, name string) {
	t.Helper()
	live := filepath.Join(leRoot, "live", name)
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(leRoot, "renewal"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string][]byte{
		filepath.Join(live, "fullchain.pem"):           createTestCertPEM(t),
		filepath.Join(live, "privkey.pem"):             []byte("key"),
		filepath.Join(leRoot, "renewal", name+".conf"): []byte("[renewalparams]\n"),
	} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeCertbot writes a certbot stand-in. With newCert set it replaces the
// lineage's leaf first; it always prints output to stderr and exits with code.
func fakeCertbot(t *testing.T, tmp string, newCert []byte, output string, code int) string {
	t.Helper()
	install := ""
	if newCert != nil {
		src := filepath.Join(tmp, "new-fullchain.pem")
		if err := os.WriteFile(src, newCert, 0o644); err != nil {
			t.Fatal(err)
		}
		install = fmt.Sprintf(`cp %q "%s/letsencrypt/live/$name/fullchain.pem"`, src, tmp)
	}
	outFile := filepath.Join(tmp, "certbot-output.txt")
	if err := os.WriteFile(outFile, []byte(output), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/bash
name=""
i=1
for a in "$@"; do
  if [[ "$a" == "--cert-name" ]]; then name="${@:$((i+1)):1}"; break; fi
  i=$((i+1))
done
%s
cat %q >&2
exit %d
`, install, outFile, code)
	path := filepath.Join(tmp, "certbot")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

var issuers = []struct {
	name    string
	lineage string
	issue   func(r *Runner) (*Result, error)
}{
	{"webroot", "example.com", func(r *Runner) (*Result, error) {
		return r.Issue("example.com", "/var/www/example", "admin@example.com", false, nil)
	}},
	{"dns-01", "wildcard.preview.example.com", func(r *Runner) (*Result, error) {
		return r.IssueDNS01("wildcard.preview.example.com", []string{"*.preview.example.com"}, "admin@example.com", false, "auth", "cleanup")
	}},
}

func TestIssue_FailedRunWithOldCertIsAnError(t *testing.T) {
	for _, tc := range issuers {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			leRoot := filepath.Join(tmp, "letsencrypt")
			setupLineage(t, leRoot, tc.lineage)
			r := &Runner{Binary: fakeCertbot(t, tmp, nil, nxdomainFailure, 1), OpenSSL: "openssl", LERoot: leRoot}

			res, err := tc.issue(r)
			if err == nil {
				t.Fatalf("certbot exited 1 and left the old cert, got success: %+v", res)
			}
			if res == nil || res.Reason != "dns_resolve_failed" {
				t.Errorf("Reason = %q, want dns_resolve_failed", reasonOf(res))
			}
			if res == nil || !strings.Contains(res.Stderr, "NXDOMAIN looking up TXT") {
				t.Errorf("Stderr does not carry certbot's detail: %q", stderrOf(res))
			}
		})
	}
}

func TestIssue_FailedRunThatSavedANewCertIsIssued(t *testing.T) {
	for _, tc := range issuers {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			leRoot := filepath.Join(tmp, "letsencrypt")
			setupLineage(t, leRoot, tc.lineage)
			r := &Runner{Binary: fakeCertbot(t, tmp, createTestCertPEM(t), "Hook command \"deploy\" returned error code 1\n", 1), OpenSSL: "openssl", LERoot: leRoot}

			res, err := tc.issue(r)
			if err != nil {
				t.Fatalf("certbot saved a new cert before failing, got error: %v", err)
			}
			if res.CertPath == "" || res.Skipped {
				t.Errorf("want the new cert reported as issued, got %+v", res)
			}
		})
	}
}

func TestIssue_KeptCertStaysSkipped(t *testing.T) {
	for _, tc := range issuers {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			leRoot := filepath.Join(tmp, "letsencrypt")
			setupLineage(t, leRoot, tc.lineage)
			r := &Runner{Binary: fakeCertbot(t, tmp, nil, "Certificate not yet due for renewal; no action taken.\n", 0), OpenSSL: "openssl", LERoot: leRoot}

			res, err := tc.issue(r)
			if err != nil {
				t.Fatalf("kept cert: unexpected error: %v", err)
			}
			if !res.Skipped || res.CertPath == "" {
				t.Errorf("want Skipped with the kept cert's path, got %+v", res)
			}
		})
	}
}

func reasonOf(r *Result) string {
	if r == nil {
		return "<nil result>"
	}
	return r.Reason
}

func stderrOf(r *Result) string {
	if r == nil {
		return "<nil result>"
	}
	return r.Stderr
}
