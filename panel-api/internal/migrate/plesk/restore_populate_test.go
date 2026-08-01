package plesk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidDBName(t *testing.T) {
	ok := []string{"lycheefr_wp2", "db1", "A_b_9"}
	bad := []string{"", "../etc", "a.sql", "a b", "a;b", "a/b", "a'b"}
	for _, s := range ok {
		if !validDBName(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		if validDBName(s) {
			t.Errorf("%q should be rejected", s)
		}
	}
}

func TestPopulatePleskDBs_StreamsValidSkipsBad(t *testing.T) {
	extract := t.TempDir()
	slug := "lychee_fruits_co_il"
	base := filepath.Join(extract, "cpmove-"+slug)
	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatal(err)
	}
	// databases.txt: two valid + one path-traversal attempt (must be skipped).
	if err := os.WriteFile(filepath.Join(base, "databases.txt"),
		[]byte("lycheefr_wp2\nshop_db\n../../etc/passwd\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	var streamed []string
	fake := func(cmd, dstPath string) error {
		streamed = append(streamed, dstPath)
		// the command must be the read-only dump for the right db.
		if !strings.Contains(cmd, "mysqldump") || !strings.Contains(cmd, "--single-transaction") {
			t.Errorf("stream cmd not a read-only dump: %q", cmd)
		}
		return os.WriteFile(dstPath, []byte("-- dump\n"), 0o640)
	}
	n, _, err := PopulatePleskDBs(extract, slug, fake)
	if err != nil {
		t.Fatalf("PopulatePleskDBs: %v", err)
	}
	if n != 2 {
		t.Fatalf("streamed %d dbs, want 2 (traversal skipped)", n)
	}
	// dumps landed under mysql/ with safe filenames.
	for _, want := range []string{
		filepath.Join(base, "mysql", "lycheefr_wp2.sql"),
		filepath.Join(base, "mysql", "shop_db.sql"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected dump %s: %v", want, err)
		}
	}
}

func TestPopulatePleskDBs_NoManifestIsQuiet(t *testing.T) {
	extract := t.TempDir()
	slug := "x"
	if err := os.MkdirAll(filepath.Join(extract, "cpmove-"+slug), 0o750); err != nil {
		t.Fatal(err)
	}
	n, _, err := PopulatePleskDBs(extract, slug, func(_, _ string) error { return nil })
	if err != nil || n != 0 {
		t.Errorf("no databases.txt → (0,nil), got (%d,%v)", n, err)
	}
}

func TestSlugExported(t *testing.T) {
	if Slug("Lychee-Fruits.CO.il") != "lychee_fruits_co_il" {
		t.Errorf("Slug wrapper wrong: %q", Slug("Lychee-Fruits.CO.il"))
	}
}
