package models

import "testing"

// ValidateSFTPHostUser is the single boundary rule both the REST and CLI
// destination validators call (JAB-310): host/user carrying whitespace or shell
// metacharacters must be rejected before they reach restic's sftp.command.
func TestValidateSFTPHostUser(t *testing.T) {
	if err := ValidateSFTPHostUser("backup.example.com", "resticuser"); err != nil {
		t.Fatalf("a plain host/user must be accepted, got %v", err)
	}

	bad := []struct {
		name, host, user string
	}{
		{"semicolon host", "h;rm -rf /", "u"},
		{"pipe user", "h", "u|nc"},
		{"space host", "bad host", "u"},
		{"tab host", "h\tx", "u"},
		{"ampersand host", "a&b", "u"},
		{"dollar user", "h", "u$IFS"},
		{"backtick host", "h`id`", "u"},
		{"redirect user", "h", "u>f"},
		{"single quote host", "h'", "u"},
		{"double quote host", "h\"", "u"},
		{"backslash host", "h\\x", "u"},
		{"paren user", "h", "u(x)"},
		{"newline host", "h\nx", "u"},
	}
	for _, tc := range bad {
		if err := ValidateSFTPHostUser(tc.host, tc.user); err == nil {
			t.Errorf("%s: host=%q user=%q must be rejected", tc.name, tc.host, tc.user)
		}
	}
}
