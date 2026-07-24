package hestiacp

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeHestiaDBConf(t *testing.T, extract, db, line string) {
	t.Helper()
	dir := filepath.Join(extract, "db", db, "hestia")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "db.conf"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// GH #633: the DB user's native password hash lives in db/<db>/hestia/db.conf's
// MD5= field. Both on-disk forms (`*XXXX` and MariaDB's `0x2A…` hex) must be
// recovered; pgsql + empty/unusable hashes must be dropped.
func TestParseDBCredentials(t *testing.T) {
	extract := t.TempDir()

	const starHash = "*0123456789ABCDEF0123456789ABCDEF01234567"
	// MariaDB print_identified_with_as_hex=ON form: hex of the *XXXX string.
	const hexSource = "*ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	hexForm := "0x" + hex.EncodeToString([]byte(hexSource))

	writeHestiaDBConf(t, extract, "alice_wp",
		"DB='alice_wp' DBUSER='alice_wp' MD5='"+starHash+"' HOST='localhost' TYPE='mysql' CHARSET='utf8'")
	writeHestiaDBConf(t, extract, "alice_shop",
		"DB='alice_shop' DBUSER='alice_shop' MD5='"+hexForm+"' HOST='localhost' TYPE='mysql'")
	writeHestiaDBConf(t, extract, "alice_pg",
		"DB='alice_pg' DBUSER='alice_pg' MD5='"+starHash+"' HOST='localhost' TYPE='pgsql'")
	writeHestiaDBConf(t, extract, "alice_nopw",
		"DB='alice_nopw' DBUSER='alice_nopw' MD5='' HOST='localhost' TYPE='mysql'")
	writeHestiaDBConf(t, extract, "alice_bad",
		"DB='alice_bad' DBUSER='alice_bad' MD5='not-a-hash' HOST='localhost' TYPE='mysql'")

	creds := ParseDBCredentials(extract)
	byDB := map[string]DBCredential{}
	for _, c := range creds {
		byDB[c.DBName] = c
	}

	if len(creds) != 2 {
		t.Fatalf("want 2 usable creds, got %d: %+v", len(creds), creds)
	}
	if got := byDB["alice_wp"].Hash; got != starHash {
		t.Errorf("star-form hash = %q, want %q", got, starHash)
	}
	if byDB["alice_wp"].User != "alice_wp" {
		t.Errorf("user = %q", byDB["alice_wp"].User)
	}
	if got := byDB["alice_shop"].Hash; got != hexSource {
		t.Errorf("0x-hex hash decode = %q, want %q", got, hexSource)
	}
	if _, ok := byDB["alice_pg"]; ok {
		t.Error("pgsql database must be skipped (mysql-only native-hash path)")
	}
	if _, ok := byDB["alice_nopw"]; ok {
		t.Error("empty MD5 must be skipped")
	}
	if _, ok := byDB["alice_bad"]; ok {
		t.Error("unparseable hash must be skipped")
	}
}

// Missing db/ tree → empty, never a panic.
func TestParseDBCredentials_NoDBDir(t *testing.T) {
	if got := ParseDBCredentials(t.TempDir()); len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}

func TestNormalizeHestiaDBHash(t *testing.T) {
	star := "*0123456789ABCDEF0123456789ABCDEF01234567"
	cases := map[string]string{
		star:                                    star,
		"0x" + hex.EncodeToString([]byte(star)): star,
		"":                                      "",
		"plaintextpw":                           "",
		"0xZZ":                                  "", // invalid hex
		"*short":                                "",
	}
	for in, want := range cases {
		if got := normalizeHestiaDBHash(in); got != want {
			t.Errorf("normalizeHestiaDBHash(%q) = %q, want %q", in, got, want)
		}
	}
}
