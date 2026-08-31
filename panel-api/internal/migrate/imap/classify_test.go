package imap

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
	}{
		{"nil", nil, ""},
		{"auth", fmt.Errorf("%w: remote said NO [AUTHENTICATIONFAILED]", ErrAuth), "auth_failed"},
		{"ssrf", fmt.Errorf("imap: connect h:143: ssrf: 192.168.1.10 private range rejected"), "blocked_host"},
		{"dns", fmt.Errorf("imap: connect h:993: %w", &net.DNSError{Err: "no such host", Name: "nope.example", IsNotFound: true}), "dns"},
		// Real shape: the SSRF guard resolves the host itself and wraps a lookup
		// failure with an "ssrf:" prefix — must still classify as DNS, not
		// blocked_host (the %w chain preserves the *net.DNSError).
		{"dns-via-ssrf", fmt.Errorf("imap: connect h:993: %w",
			fmt.Errorf("ssrf: resolve nope.example: %w", &net.DNSError{Err: "no such host", Name: "nope.example", IsNotFound: true})), "dns"},
		{"ctx-timeout", fmt.Errorf("imap: connect h:993: %w", context.DeadlineExceeded), "timeout"},
		{"net-timeout", &net.OpError{Op: "dial", Err: os.ErrDeadlineExceeded}, "timeout"},
		{"refused", fmt.Errorf("imap: connect h:993: dial tcp: %w", syscall.ECONNREFUSED), "refused"},
		{"tls-type", fmt.Errorf("imap: tls handshake h: %w", x509.UnknownAuthorityError{}), "tls"},
		{"tls-string", errors.New("imap: tls handshake h: remote error: tls: handshake failure"), "tls"},
		{"generic", errors.New("imap: something unexpected"), "connect_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, hint := Classify(tc.err)
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if tc.err == nil {
				if hint != "" {
					t.Fatalf("nil error must yield empty hint, got %q", hint)
				}
				return
			}
			if hint == "" {
				t.Fatalf("non-nil error must yield a hint")
			}
			// The hint must never echo the raw error / remote banner.
			if strings.Contains(hint, "imap:") || strings.Contains(hint, "ssrf:") ||
				strings.Contains(hint, "AUTHENTICATIONFAILED") || strings.Contains(hint, "192.168") {
				t.Fatalf("hint leaks raw error text: %q", hint)
			}
		})
	}
}
