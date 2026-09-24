package api

import "testing"

// TestValidateDomainName_HTTPAdapter pins the HTTP door's wire over the shared
// domainops FQDN gate (JAB-279 AC2/AC6): the same input matrix the CLI adapter
// test (cmd/server) drives, so the two doors provably accept and reject the
// identical set of names, and each mapped message is byte-identical to what the
// handler returned before the extraction. `want` is the exact error string
// (empty = the name is accepted).
func TestValidateDomainName_HTTPAdapter(t *testing.T) {
	cases := []struct {
		in   string
		want string // "" = accepted
	}{
		{"example.com", ""},
		{"a.b.example.com", ""},
		{"", "domain name cannot be empty"},
		{"exa mple.com", "domain name contains invalid whitespace characters"},
		{"example.com\r", "domain name contains invalid whitespace characters"},
		{"<script>.com", "domain name contains invalid HTML characters"},
		{"a..b.com", "domain name contains invalid path characters"},
		{"example.com/evil", "domain name contains invalid path characters"},
		{"localhost", "domain name is not a valid FQDN (requires at least two labels and 2+ letter TLD)"},
		{"192.168.1.1", "domain name is not a valid FQDN (requires at least two labels and 2+ letter TLD)"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			err := validateDomainName(tc.in)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validateDomainName(%q) = %v, want accepted", tc.in, err)
				}
				return
			}
			if err == nil || err.Error() != tc.want {
				t.Fatalf("validateDomainName(%q) = %v, want %q", tc.in, err, tc.want)
			}
		})
	}
}
