package main

import "testing"

func TestDeriveUsername(t *testing.T) {
	cases := map[string]string{
		"admin@jabali-panel.local": "admin",
		"John.Doe@example.com":     "john_doe",
		"a+b@x.io":                 "a_b",
		"123num@x.io":              "u123num",
		"@weird":                   "_weird",
	}
	for in, want := range cases {
		if got := deriveUsername(in); got != want {
			t.Errorf("deriveUsername(%q) = %q, want %q", in, got, want)
		}
	}
	if got := deriveUsername("averyveryveryveryveryverylongemaillocalpart@x.io"); len(got) > 32 {
		t.Errorf("derived username too long: %q (%d)", got, len(got))
	}
}

func TestUniqueUsername(t *testing.T) {
	taken := map[string]bool{"admin": true, "admin-2": true}
	if got := uniqueUsername("admin", taken); got != "admin-3" {
		t.Errorf("uniqueUsername collision = %q, want admin-3", got)
	}
	if got := uniqueUsername("fresh", taken); got != "fresh" {
		t.Errorf("uniqueUsername free = %q, want fresh", got)
	}
}
