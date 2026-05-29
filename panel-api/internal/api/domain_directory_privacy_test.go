package api

import "testing"

func TestValidatePrivacyPath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"/secret", "/secret", false},
		{"/a/b/c", "/a/b/c", false},
		{"/trim/", "/trim", false},
		{"/", "/", false},
		{"", "", true},
		{"no-leading-slash", "", true},
		{"/has space", "", true},
		{"/has//double", "", true},
		{"/../etc/passwd", "", true},
		{"/dot/../boom", "", true},
		{"/special$char", "", true},
	}
	for _, tc := range cases {
		got, err := validatePrivacyPath(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("validatePrivacyPath(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("validatePrivacyPath(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("validatePrivacyPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidatePrivacyPath_LengthCap(t *testing.T) {
	long := "/" + string(make([]byte, 255)) // 256 chars total
	for i := 1; i < len(long); i++ {
		long = long[:i] + "a" + long[i+1:]
	}
	if _, err := validatePrivacyPath(long); err == nil {
		t.Errorf("expected error on len > 255")
	}
}

func TestValidatePrivacyRealm(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "Restricted", false},
		{"Staging", "Staging", false},
		{` ok `, "ok", false},
		{`bad"quote`, "", true},
		{`bad\back`, "", true},
		{"highÿascii", "", true},
		{string(make([]byte, 256)), "", true},
	}
	for _, tc := range cases {
		got, err := validatePrivacyRealm(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("validatePrivacyRealm(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("validatePrivacyRealm(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("validatePrivacyRealm(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidatePrivacyUsername(t *testing.T) {
	good := []string{"alice", "bob.smith", "a_b-c", "1234"}
	for _, u := range good {
		if err := validatePrivacyUsername(u); err != nil {
			t.Errorf("validatePrivacyUsername(%q) error: %v", u, err)
		}
	}
	bad := []string{"", "has space", "$evil", "ünicode", string(make([]byte, 65))}
	for _, u := range bad {
		if err := validatePrivacyUsername(u); err == nil {
			t.Errorf("validatePrivacyUsername(%q) = nil, want error", u)
		}
	}
}

func TestValidatePrivacyPassword(t *testing.T) {
	if err := validatePrivacyPassword("short"); err == nil {
		t.Errorf("expected error on short")
	}
	if err := validatePrivacyPassword("12345678"); err != nil {
		t.Errorf("8-char password rejected: %v", err)
	}
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if err := validatePrivacyPassword(string(long)); err == nil {
		t.Errorf("expected error on len > 128")
	}
}
