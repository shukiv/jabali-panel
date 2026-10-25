package models

import "testing"

func TestIsFreeHostnameBase(t *testing.T) {
	for name, want := range map[string]bool{
		"203-0-113-7.jabalihosted.com":   true,
		"203-0-113-7-b.jabalihosted.com": true,
		"203-0-113-7.JabaliHosted.com.":  true,
		"jabalihosted.com":               false,
		".jabalihosted.com":              false,
		"a.b.jabalihosted.com":           false, // a preview host, not a base
		"evil-jabalihosted.com":          false,
		"jabalihosted.com.evil.com":      false,
		"preview.host.tld":               false,
		"":                               false,
	} {
		if got := IsFreeHostnameBase(name); got != want {
			t.Errorf("IsFreeHostnameBase(%q) = %v, want %v", name, got, want)
		}
	}
}
