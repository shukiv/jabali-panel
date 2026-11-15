package agentwire

import (
	"strings"
	"testing"
)

func TestValidWebmailEditorURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{"empty is off", "", true},
		{"bare origin", "https://office.example.com", true},
		{"discovery path", "https://office.example.com/hosting/discovery", true},
		{"plain http refused", "http://office.example.com", false},
		{"user info refused", "https://user:pw@office.example.com", false},
		{"query refused", "https://office.example.com?a=b", false},
		{"fragment refused", "https://office.example.com#x", false},
		{"newline refused", "https://office.example.com\nWOPI_CLIENT_URL=x", false},
		{"space refused", "https://office.example.com /", false},
		{"no scheme refused", "office.example.com", false},
		{"255 characters accepted", "https://office.example.com/" + strings.Repeat("a", 228), true},
		{"256 characters refused", "https://office.example.com/" + strings.Repeat("a", 229), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidWebmailEditorURL(tc.url); got != tc.want {
				t.Errorf("ValidWebmailEditorURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
