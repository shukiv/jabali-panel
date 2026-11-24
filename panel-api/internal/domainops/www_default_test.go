package domainops

import "testing"

func TestDefaultCreateWWW(t *testing.T) {
	cases := map[string]bool{
		"example.com":         true,
		"Example.COM.":        true,
		"example.co.il":       true, // two-part suffix: a main domain
		"example.org.il":      true,
		"example.co.uk":       true,
		"xn--4dbrk0ce.co.il":  true, // IDN main domain
		"jabali-panel2.local": true, // not on the list: last label is the suffix
		"blog.example.com":    false,
		"shop.example.co.il":  false,
		"www.example.com":     false,
		"www.example.co.il":   false,
		"co.il":               false, // a suffix alone
		"com":                 false,
		"":                    false,
	}
	for name, want := range cases {
		if got := DefaultCreateWWW(name); got != want {
			t.Errorf("DefaultCreateWWW(%q) = %v, want %v", name, got, want)
		}
	}
}
