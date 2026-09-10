package commands

import "testing"

// GH #1625: server_name is raw nginx config, so sanitizeAliasServerNames is a
// trust boundary — the panel already validated, but the agent RE-validates so a
// value carrying a space, semicolon, brace, or newline can never break out of
// the `server_name ...;` directive. Fail-closed: bad entries are DROPPED.
func TestSanitizeAliasServerNames(t *testing.T) {
	tests := []struct {
		name    string
		domain  string
		aliases []string
		want    string
	}{
		{"none", "example.com", nil, ""},
		{"single valid", "example.com", []string{"shop.example.net"}, " shop.example.net"},
		{
			"multiple valid, order preserved",
			"example.com",
			[]string{"shop.example.net", "www.brand.io"},
			" shop.example.net www.brand.io",
		},
		{"lowercased", "example.com", []string{"Shop.Example.NET"}, " shop.example.net"},
		{"trimmed", "example.com", []string{"  shop.example.net  "}, " shop.example.net"},
		{"deduped", "example.com", []string{"a.example.net", "a.example.net"}, " a.example.net"},
		{"skips primary", "example.com", []string{"example.com", "shop.example.net"}, " shop.example.net"},
		{"skips www.primary", "example.com", []string{"www.example.com", "shop.example.net"}, " shop.example.net"},
		// Injection / malformed inputs — every one dropped, nothing emitted raw.
		{"drops semicolon injection", "example.com", []string{"evil.net; return 301 http://x"}, ""},
		{"drops space", "example.com", []string{"a.net b.net"}, ""},
		{"drops brace", "example.com", []string{"a.net}server{"}, ""},
		{"drops newline", "example.com", []string{"a.net\nb.net"}, ""},
		{"drops single label", "example.com", []string{"localhost"}, ""},
		{"drops empty", "example.com", []string{""}, ""},
		{"drops leading dot", "example.com", []string{".example.net"}, ""},
		{
			"keeps only the valid survivor",
			"example.com",
			[]string{"bad one", "good.example.net", "also;bad"},
			" good.example.net",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeAliasServerNames(tc.domain, tc.aliases); got != tc.want {
				t.Errorf("sanitizeAliasServerNames(%q, %v) = %q, want %q", tc.domain, tc.aliases, got, tc.want)
			}
		})
	}
}
