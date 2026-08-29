package commands

import (
	"strings"
	"testing"
)

func TestPgDumpIsArchive(t *testing.T) {
	// custom-format magic
	if !pgDumpIsArchive([]byte("PGDMP\x01\x0e\x00")) {
		t.Error("PGDMP magic must be detected as an archive (custom format)")
	}
	// tar: "ustar" at offset 257
	tar := make([]byte, 300)
	copy(tar[257:], "ustar")
	if !pgDumpIsArchive(tar) {
		t.Error("ustar magic at offset 257 must be detected as an archive (tar format)")
	}
	// plain SQL variants — NOT archives
	for _, plain := range []string{
		"--\n-- PostgreSQL database dump\n--\nSET statement_timeout = 0;\n",
		"\xef\xbb\xbf-- BOM-prefixed plain dump\n", // UTF-8 BOM
		"SET client_encoding = 'UTF8';\r\nCREATE TABLE t();\r\n", // CRLF
		"",   // empty
		"PGD", // too short to be PGDMP
	} {
		if pgDumpIsArchive([]byte(plain)) {
			t.Errorf("plain/short input %q must NOT be detected as an archive", plain)
		}
	}
	// a <262-byte plain dump must not panic the tar check
	if pgDumpIsArchive([]byte("SELECT 1;")) {
		t.Error("short plain dump misclassified as archive")
	}
}

func TestPgTrimLoaderError(t *testing.T) {
	// strips absolute staging paths
	got := pgTrimLoaderError(`psql: error: /var/lib/jabali/restore/up-abc.sql:5: syntax error`)
	if strings.Contains(got, "/var/lib/jabali") {
		t.Errorf("absolute jabali path must be scrubbed: %q", got)
	}
	if !strings.Contains(got, "syntax error") {
		t.Errorf("the real diagnostic must survive: %q", got)
	}
	// caps to the first handful of lines
	many := strings.Repeat("ERROR: cascading failure\n", 50)
	if n := strings.Count(pgTrimLoaderError(many), "\n"); n > 6 {
		t.Errorf("must cap cascading output to a few lines, got %d newlines", n)
	}
	// caps length
	long := strings.Repeat("x", 5000)
	if len(pgTrimLoaderError(long)) > 520 {
		t.Errorf("must cap length, got %d", len(pgTrimLoaderError(long)))
	}
	// empty → a non-empty placeholder (never a blank tenant-facing error)
	if pgTrimLoaderError("   ") == "" {
		t.Error("empty loader output must yield a placeholder, not an empty string")
	}
}
