package dbconsoleops

import (
	"testing"
)

func TestNormalizeEngine(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Empty and whitespace → mariadb default
		{"", "mariadb"},
		{" ", "mariadb"},
		{"\t", "mariadb"},
		{"\n", "mariadb"},

		// Valid engines preserved (trimmed)
		{"mariadb", "mariadb"},
		{"postgres", "postgres"},
		{" mariadb ", "mariadb"},
		{" postgres ", "postgres"},

		// Unknown engines passed through (validation is Issue's job)
		{"unknown", "unknown"},
		{"mysql", "mysql"},
		{"Oracle", "Oracle"},
	}

	for _, tt := range tests {
		t.Run("NormalizeEngine("+tt.input+")", func(t *testing.T) {
			got := NormalizeEngine(tt.input)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}
