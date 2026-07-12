package main

import "testing"

func TestCLIValidatePrivacyPath(t *testing.T) {
	okCases := map[string]string{"/admin": "/admin", "/a/b/": "/a/b", "/": "/", "/x_1.2-3": "/x_1.2-3"}
	for in, want := range okCases {
		got, err := cliValidatePrivacyPath(in)
		if err != nil || got != want {
			t.Errorf("path %q -> (%q,%v), want (%q,nil)", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "admin", "/a//b", "/../etc", "/a b"} {
		if _, err := cliValidatePrivacyPath(bad); err == nil {
			t.Errorf("path %q should be invalid", bad)
		}
	}
}

func TestCLIValidatePrivacyRealm(t *testing.T) {
	if r, _ := cliValidatePrivacyRealm(""); r != "Restricted" {
		t.Errorf("empty realm should default to Restricted, got %q", r)
	}
	if _, err := cliValidatePrivacyRealm(`bad"quote`); err == nil {
		t.Error(`realm with " should be invalid`)
	}
}
