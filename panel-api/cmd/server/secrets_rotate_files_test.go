// JAB-357 crit-7 — unit tests for the secret-rewrite primitives. These run
// with no box: the point is that the bug-prone core (mode preservation, key
// drop, DSN parsing, rollback) is proven off-host before any live rotation.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvReplaceKey_PreservesSiblings(t *testing.T) {
	in := "PANEL_ADDR=127.0.0.1:8443\nJWT_SECRET=old\nDATABASE_URL=u:p@unix(/s)/db\nJABALI_REDIS_PANEL_TOKEN=tok\n"
	out, ok := envReplaceKey(in, "JWT_SECRET", "new")
	if !ok {
		t.Fatal("key not found")
	}
	if !strings.Contains(out, "JWT_SECRET=new\n") {
		t.Errorf("value not replaced:\n%s", out)
	}
	// Every sibling line must survive verbatim.
	for _, want := range []string{"PANEL_ADDR=127.0.0.1:8443", "DATABASE_URL=u:p@unix(/s)/db", "JABALI_REDIS_PANEL_TOKEN=tok"} {
		if !strings.Contains(out, want) {
			t.Errorf("dropped sibling %q from:\n%s", want, out)
		}
	}
	if strings.Contains(out, "JWT_SECRET=old") {
		t.Error("old value still present")
	}
}

func TestEnvReplaceKey_AbsentKeyIsNotAppended(t *testing.T) {
	in := "A=1\nB=2\n"
	out, ok := envReplaceKey(in, "MISSING", "x")
	if ok {
		t.Error("reported found for absent key")
	}
	if out != in {
		t.Errorf("content changed for absent key: %q", out)
	}
}

func TestEnvReplaceKey_PrefixIsNotAPartialMatch(t *testing.T) {
	// JWT_SECRET must not match JWT_SECRET_OLD (the '=' guards the boundary).
	in := "JWT_SECRET_OLD=keepme\nJWT_SECRET=hit\n"
	out, ok := envReplaceKey(in, "JWT_SECRET", "new")
	if !ok || !strings.Contains(out, "JWT_SECRET=new") || !strings.Contains(out, "JWT_SECRET_OLD=keepme") {
		t.Errorf("prefix boundary mishandled:\n%s", out)
	}
}

func TestDSNReplacePassword(t *testing.T) {
	const dsn = "jabali_panel_app:oldpw@unix(/var/run/mysqld/mysqld.sock)/jabali_panel?parseTime=true&charset=utf8mb4&loc=UTC"
	out, err := dsnReplacePassword(dsn, "N3w-Secret_val")
	if err != nil {
		t.Fatal(err)
	}
	want := "jabali_panel_app:N3w-Secret_val@unix(/var/run/mysqld/mysqld.sock)/jabali_panel?parseTime=true&charset=utf8mb4&loc=UTC"
	if out != want {
		t.Errorf("got  %q\nwant %q", out, want)
	}
}

func TestDSNReplacePassword_Malformed(t *testing.T) {
	for _, bad := range []string{"noatnocolon", "user@host/db"} {
		if _, err := dsnReplacePassword(bad, "x"); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestACLReplacePanelToken(t *testing.T) {
	in := "user default off\n" +
		"user jabali_panel on >oldtok ~jabali:* ~automation:* resetchannels +@all -@dangerous +acl +@connection\n" +
		"user wp_alice on >alicepw ~wp:alice:* +@read\n"
	out, ok := aclReplacePanelToken(in, "NEWTOK")
	if !ok {
		t.Fatal("jabali_panel line not found")
	}
	if !strings.Contains(out, "user jabali_panel on >NEWTOK ~jabali:* ~automation:* resetchannels +@all -@dangerous +acl +@connection") {
		t.Errorf("jabali_panel line wrong:\n%s", out)
	}
	if strings.Contains(out, ">oldtok") {
		t.Error("old token still present")
	}
	// default + tenant lines preserved verbatim.
	for _, keep := range []string{"user default off", "user wp_alice on >alicepw ~wp:alice:* +@read"} {
		if !strings.Contains(out, keep) {
			t.Errorf("dropped line %q", keep)
		}
	}
}

func TestACLReplacePanelToken_Absent(t *testing.T) {
	if _, ok := aclReplacePanelToken("user default off\n", "x"); ok {
		t.Error("reported found with no jabali_panel line")
	}
}

func TestAtomicRewritePreserving_KeepsModeAndReplaces(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret")
	if err := os.WriteFile(p, []byte("old-value\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o640); err != nil { // WriteFile honors umask; pin it
		t.Fatal(err)
	}
	if err := atomicRewritePreserving(p, "new-value\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "new-value\n" {
		t.Errorf("content = %q", got)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o, want 640 (must not loosen)", fi.Mode().Perm())
	}
	// No temp files left behind in the dir.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".jabali-rotate-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestAtomicRewritePreserving_RefusesMissingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "does-not-exist")
	if err := atomicRewritePreserving(p, "x"); err == nil {
		t.Error("expected error when rewriting a nonexistent secret")
	}
}

func TestBackupRestorePurge_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret")
	if err := os.WriteFile(p, []byte("original\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(p, 0o640)

	bak, err := backupToBak(p)
	if err != nil {
		t.Fatal(err)
	}
	if bak != p+bakSuffix {
		t.Errorf("bak path = %s", bak)
	}
	// Simulate a rotation that we then roll back.
	if err := atomicRewritePreserving(p, "rotated\n"); err != nil {
		t.Fatal(err)
	}
	if err := restoreFromBak(p, bak); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "original\n" {
		t.Errorf("restore = %q, want original", got)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o640 {
		t.Errorf("restore mode = %o, want 640", fi.Mode().Perm())
	}
	// restoreFromBak removes the snapshot.
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Error("bak not removed after restore")
	}
	// purgeBak is idempotent on an already-gone bak.
	if err := purgeBak(bak); err != nil {
		t.Errorf("purge on missing bak: %v", err)
	}
}

func TestPurgeBak_RemovesSnapshot(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "secret")
	_ = os.WriteFile(p, []byte("x"), 0o640)
	bak, err := backupToBak(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := purgeBak(bak); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bak); !os.IsNotExist(err) {
		t.Error("bak still present after purge")
	}
}
