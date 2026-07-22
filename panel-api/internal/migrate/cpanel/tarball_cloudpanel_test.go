package cpanel

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// tarEntry is one regular-file entry; an ordered slice is used (not a map) so
// the stream order is deterministic — ParseTarball's wrapper detection is lazy
// on the first cp/ segment, and cloudpanel/backup.go guarantees cp/ is archived
// first. The test reproduces that ordering.
type tarEntry struct{ name, content string }

func writeGzTar(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name: e.name, Mode: 0o644, Size: int64(len(e.content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("tar header %s: %v", e.name, err)
		}
		if _, err := tw.Write([]byte(e.content)); err != nil {
			t.Fatalf("tar write %s: %v", e.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// The CloudPanel source (GH #522) synthesises a cpmove-shaped tarball whose
// areas sit one level below a cpmove-<user>/ wrapper — exactly what the
// DirectAdmin source produces. This asserts ParseTarball strips the wrapper,
// detects SourceUser from cp/<user>, and classifies the web-only subset
// (mysql/ + cron/ + homedir/.ssh) plus extracts domains-paths.txt for the
// import stage to read. A regression here would silently drop a CloudPanel
// account's databases, cron jobs, or SSH keys on restore.
func TestParseTarball_CloudPanelSynthesizedLayout(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "cpmove-smokeuser.tar.gz")
	// cp/ first, then the areas — the exact order cloudpanel/backup.go emits.
	writeGzTar(t, tarPath, []tarEntry{
		{"cpmove-smokeuser/cp/smokeuser", "USER=smokeuser\nDNS=smoke.jabalitest.com\n"},
		{"cpmove-smokeuser/mysql/smokedb.sql", "CREATE TABLE `widgets` (id INT);\nINSERT INTO `widgets` VALUES (1);\n"},
		{"cpmove-smokeuser/cron/smokeuser", "30 3 * * * /usr/bin/php /home/smokeuser/htdocs/smoke.jabalitest.com/cron.php\n"},
		{"cpmove-smokeuser/domains-paths.txt", "smoke.jabalitest.com\t/home/smokeuser/htdocs/smoke.jabalitest.com\n"},
		{"cpmove-smokeuser/homedir/.ssh/authorized_keys", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAISmoke smoke@test\n"},
	})

	extractDir := filepath.Join(dir, "extracted")
	p, err := ParseTarball(tarPath, extractDir)
	if err != nil {
		t.Fatalf("ParseTarball: %v", err)
	}
	if p.SourceUser != "smokeuser" {
		t.Errorf("SourceUser = %q, want smokeuser (from cp/<user> under the cpmove- wrapper)", p.SourceUser)
	}
	if len(p.MySQLDumps) != 1 {
		t.Errorf("MySQLDumps = %v, want the single smokedb.sql", p.MySQLDumps)
	}
	if len(p.CronFiles) != 1 {
		t.Errorf("CronFiles = %v, want cron/smokeuser", p.CronFiles)
	}
	if len(p.SSHAuthorized) != 1 {
		t.Errorf("SSHAuthorized = %v, want homedir/.ssh/authorized_keys", p.SSHAuthorized)
	}
	// CloudPanel is web-only: no BIND zones, so ImportDomains must fall back to
	// the domains-paths.txt list (populated in migrate_run_cmd, not here).
	if len(p.ZoneFiles) != 0 {
		t.Errorf("ZoneFiles = %v, want none (CloudPanel has no dnszones/)", p.ZoneFiles)
	}
	// The import stage reads domains-paths.txt from the extracted tree, so it
	// must have been written to disk under the wrapper dir.
	if _, err := os.Stat(filepath.Join(extractDir, "cpmove-smokeuser", "domains-paths.txt")); err != nil {
		t.Errorf("domains-paths.txt not extracted for the import stage: %v", err)
	}
}

// CyberPanel's account id is a domain (dotted), so the cpmove wrapper is
// cpmove-<domain>/ and cp/<domain> carries dots. Assert ParseTarball detects
// the wrapper + SourceUser correctly with a dotted account (GH #522 CyberPanel).
func TestParseTarball_CyberPanelDottedDomainAccount(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "cpmove-smoke.jabalitest.com.tar.gz")
	writeGzTar(t, tarPath, []tarEntry{
		{"cpmove-smoke.jabalitest.com/cp/smoke.jabalitest.com", "USER=smoke.jabalitest.com\nDNS=smoke.jabalitest.com\nCONTACTEMAIL=admin@smoke.jabalitest.com\n"},
		{"cpmove-smoke.jabalitest.com/mysql/smokedb.sql", "CREATE TABLE t (id INT);\n"},
		{"cpmove-smoke.jabalitest.com/cron/smoke.jabalitest.com", "15 2 * * * /usr/bin/php /home/smoke.jabalitest.com/public_html/cron.php\n"},
		{"cpmove-smoke.jabalitest.com/domains-paths.txt", "smoke.jabalitest.com\t/home/smoke.jabalitest.com/public_html\n"},
	})
	p, err := ParseTarball(tarPath, filepath.Join(dir, "extracted"))
	if err != nil {
		t.Fatalf("ParseTarball: %v", err)
	}
	if p.SourceUser != "smoke.jabalitest.com" {
		t.Errorf("SourceUser = %q, want the dotted domain", p.SourceUser)
	}
	if len(p.MySQLDumps) != 1 || len(p.CronFiles) != 1 {
		t.Errorf("dumps=%v cron=%v — dotted-account wrapper strip broke classification", p.MySQLDumps, p.CronFiles)
	}
}
