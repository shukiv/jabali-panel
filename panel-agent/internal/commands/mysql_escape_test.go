package commands

import (
	"strings"
	"testing"
)

// TestEscapeMariaDBIdentifier tests the identifier escaping function
// with known-good cases.
func TestEscapeMariaDBIdentifier(t *testing.T) {
	tests := []struct {
		input    string
		want     string
		wantErr  bool
	}{
		// Normal case: no special chars
		{"alice_wp", "`alice_wp`", false},
		// Backtick inside
		{"back`tick", "`back``tick`", false},
		// Multiple backticks
		{"a`b`c", "`a``b``c`", false},
		// Numbers and underscores
		{"test_123", "`test_123`", false},
		// Dashes
		{"db-name", "`db-name`", false},
		// Empty string error
		{"", "", true},
		// NUL byte error
		{"has\x00nul", "", true},
		// Long name (MariaDB limit is 64 chars, we're not enforcing here)
		{strings.Repeat("a", 64), "`" + strings.Repeat("a", 64) + "`", false},
	}

	for _, tt := range tests {
		got, err := EscapeMariaDBIdentifier(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("EscapeMariaDBIdentifier(%q): error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("EscapeMariaDBIdentifier(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// TestEscapeMariaDBLiteral tests the literal escaping function
// with known-good cases.
func TestEscapeMariaDBLiteral(t *testing.T) {
	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		// Normal string
		{"password123", "'password123'", false},
		// Single quote inside
		{"pass'word", "'pass''word'", false},
		// Multiple single quotes
		{"a'b'c", "'a''b''c'", false},
		// Backslash
		{`pass\word`, `'pass\\word'`, false},
		// Both backslash and quote — we use SQL-standard '' (doubled) for
		// quotes, not the \' backslash-escape form, so the source's single
		// quote becomes two apostrophes and its backslash is doubled.
		{`pass\'word`, `'pass\\''word'`, false},
		// Empty string OK (unlike identifier)
		{"", "''", false},
		// NUL byte error
		{"has\x00nul", "", true},
		// UTF-8 string
		{"café", "'café'", false},
		// CR and LF as escapes: the statement stays on one line, and the
		// CR of a CRLF survives the client's line reader.
		{"a\r\nb", `'a\r\nb'`, false},
		{"back\\\nslash", `'back\\\nslash'`, false},
	}

	for _, tt := range tests {
		got, err := EscapeMariaDBLiteral(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("EscapeMariaDBLiteral(%q): error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("EscapeMariaDBLiteral(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// FuzzEscapeMariaDBIdentifier fuzzes the identifier escape function.
func FuzzEscapeMariaDBIdentifier(f *testing.F) {
	f.Add("alice_wp")
	f.Add("back`tick")
	f.Add("")
	f.Add("normal-name")
	f.Add("a`b`c`d")
	f.Add(strings.Repeat("x", 100))

	f.Fuzz(func(t *testing.T, s string) {
		escaped, err := EscapeMariaDBIdentifier(s)
		if err != nil {
			// Empty or NUL byte cases; valid to error
			return
		}
		// If no error, must be wrapped in backticks
		if !strings.HasPrefix(escaped, "`") || !strings.HasSuffix(escaped, "`") {
			t.Fatalf("not wrapped in backticks: %q", escaped)
		}
		// Inner content must have doubled backticks (if any backticks present)
		inner := escaped[1 : len(escaped)-1]
		if strings.Contains(s, "`") && !strings.Contains(inner, "``") {
			t.Fatalf("unescaped backtick in %q -> %q", s, escaped)
		}
		// Must not have odd number of consecutive backticks (except outer pair)
		if strings.Contains(inner, "`") {
			// All backticks in inner should be part of `` pairs
			replaced := strings.ReplaceAll(inner, "``", "")
			if strings.Contains(replaced, "`") {
				t.Fatalf("unescaped backtick remains in %q -> %q", s, escaped)
			}
		}
	})
}

// FuzzEscapeMariaDBLiteral fuzzes the literal escape function.
func FuzzEscapeMariaDBLiteral(f *testing.F) {
	f.Add("password123")
	f.Add("pass'word")
	f.Add("")
	f.Add(`pass\word`)
	f.Add("a'b'c'd")
	f.Add(strings.Repeat("x", 100))

	f.Fuzz(func(t *testing.T, s string) {
		escaped, err := EscapeMariaDBLiteral(s)
		if err != nil {
			// NUL byte case; valid to error
			return
		}
		// Must be wrapped in single quotes
		if !strings.HasPrefix(escaped, "'") || !strings.HasSuffix(escaped, "'") {
			t.Fatalf("not wrapped in single quotes: %q", escaped)
		}
		// Inner content must have doubled single quotes (if any present)
		inner := escaped[1 : len(escaped)-1]
		if strings.Contains(s, "'") && !strings.Contains(inner, "''") {
			t.Fatalf("unescaped single quote in %q -> %q", s, escaped)
		}
	})
}
