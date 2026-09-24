package main

import (
	"strings"
	"testing"
)

// TestValidateDomainName_CLIAdapter drives the SAME input matrix as the HTTP
// adapter test (internal/api) through the CLI adapter over the shared domainops
// FQDN gate (JAB-279 AC2/AC6). The accept/reject verdict must match the HTTP door
// row-for-row (both anchor to the one leaf); the CLI now surfaces a specific
// reason per rejection instead of the old blanket "not a valid FQDN". Messages
// are matched by substring because the CLI quotes the offending value with %q.
func TestValidateDomainName_CLIAdapter(t *testing.T) {
	cases := []struct {
		in         string
		accept     bool
		wantSubstr string
	}{
		{"example.com", true, ""},
		{"a.b.example.com", true, ""},
		{"", false, "domain name cannot be empty"},
		{"exa mple.com", false, "contains whitespace"},
		{"example.com\r", false, "contains whitespace"},
		{"<script>.com", false, "contains invalid HTML characters"},
		{"a..b.com", false, "contains invalid path characters"},
		{"example.com/evil", false, "contains invalid path characters"},
		{"localhost", false, "is not a valid FQDN"},
		{"192.168.1.1", false, "is not a valid FQDN"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			err := validateDomainName(tc.in)
			if tc.accept {
				if err != nil {
					t.Fatalf("validateDomainName(%q) = %v, want accepted", tc.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateDomainName(%q) = nil, want rejected", tc.in)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("validateDomainName(%q) = %q, want substring %q", tc.in, err.Error(), tc.wantSubstr)
			}
		})
	}
}
