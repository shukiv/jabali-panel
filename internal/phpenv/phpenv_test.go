package phpenv

import "testing"

func TestValidKey(t *testing.T) {
	ok := []string{"APP_ENV", "DB_HOST", "_X", "A1_B2", "REDIS_URL"}
	for _, k := range ok {
		if err := ValidKey(k); err != nil {
			t.Errorf("ValidKey(%q) = %v, want nil", k, err)
		}
	}
	// The jailbreak set + CGI meta + HTTP_ prefix + bad shapes must all be rejected.
	bad := []string{
		"PHP_ADMIN_VALUE", "php_admin_value", "PHP_VALUE", "PHP_ADMIN_FLAG",
		"SCRIPT_FILENAME", "DOCUMENT_ROOT", "PATH_INFO", "HTTPS", "REDIRECT_STATUS",
		"HTTP_X_FORWARDED_FOR", "1BAD", "has space", "has-dash", "toooo" + longName(),
		"",
	}
	for _, k := range bad {
		if err := ValidKey(k); err == nil {
			t.Errorf("ValidKey(%q) = nil, want error", k)
		}
	}
}

func longName() string {
	b := make([]byte, 70)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestValidValue(t *testing.T) {
	if err := ValidValue("postgres://u:p@localhost/db"); err != nil {
		t.Errorf("normal value rejected: %v", err)
	}
	if err := ValidValue("line1\nline2"); err == nil {
		t.Error("newline value must be rejected")
	}
	if err := ValidValue(string(make([]byte, MaxValueLen+1))); err == nil {
		t.Error("over-long value must be rejected")
	}
}

func TestEscape(t *testing.T) {
	if got := Escape(`a"b\c`); got != `a\"b\\c` {
		t.Errorf("Escape = %q, want %q", got, `a\"b\\c`)
	}
}
