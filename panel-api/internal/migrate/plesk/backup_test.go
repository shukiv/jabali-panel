package plesk

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLooksLikePleskSubscription(t *testing.T) {
	ok := []string{"example-b.co.il", "example.com", "a.b-c_d.net"}
	bad := []string{"", "bad;rm -rf", "a b", "x`whoami`", "'; DROP", "a/b"}
	for _, s := range ok {
		if !looksLikePleskSubscription(s) {
			t.Errorf("expected %q valid", s)
		}
	}
	for _, s := range bad {
		if looksLikePleskSubscription(s) {
			t.Errorf("expected %q rejected", s)
		}
	}
}

func TestPleskSlug(t *testing.T) {
	if got := pleskSlug("Example-B.CO.il"); got != "example_b_co_il" {
		t.Errorf("slug = %q, want example_b_co_il", got)
	}
}

func TestRemoveRemote_Allowlist(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{"rm -f": ""})
	// Refuses anything not under /tmp/cpmove-*, or with traversal.
	for _, bad := range []string{"/etc/passwd", "/tmp/other", "/tmp/cpmove-../etc"} {
		if err := d.RemoveRemote(context.Background(), s, bad); err == nil {
			t.Errorf("RemoveRemote(%q) should be refused", bad)
		}
	}
	if err := d.RemoveRemote(context.Background(), s, "/tmp/cpmove-example_com.tar.gz"); err != nil {
		t.Errorf("valid cpmove path refused: %v", err)
	}
}

func TestBackupUser_ReturnsLastLinePath(t *testing.T) {
	d := New()
	// The synthesis script echoes the tarball path on its last line; any
	// preceding WARN lines on stdout must be ignored.
	s := fixtureSession(map[string]string{
		"cpmove-": "WARN: mysqldump skipped\n/tmp/cpmove-example_com.tar.gz\n",
	})
	path, err := d.BackupUser(context.Background(), s, "example.com")
	if err != nil {
		t.Fatalf("BackupUser: %v", err)
	}
	if path != "/tmp/cpmove-example_com.tar.gz" {
		t.Errorf("path = %q, want the last-line tarball path", path)
	}
}

func TestBackupUser_RejectsBadSubscription(t *testing.T) {
	d := New()
	s := fixtureSession(map[string]string{})
	if _, err := d.BackupUser(context.Background(), s, "bad; rm -rf /"); err == nil {
		t.Error("BackupUser must reject a shell-unsafe subscription name")
	}
}

// The synthesis script, run for real against a stub `plesk` CLI: the cpmove
// carries the owner's contact email under cp/<slug>, and each domain's PHP
// version as cPanel userdata/<domain> `phpversion: X.Y`, read from the
// plesk-phpXY-* handler id. A domain on an OS-vendor handler (fpm) gets no
// file, and being the LAST domain must not abort the script under set -e.
func TestBackupUser_ScriptCarriesContactEmailAndPHPVersion(t *testing.T) {
	for _, tool := range []string{"bash", "tar", "sed"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	sub := fmt.Sprintf("jt%d.example", time.Now().UnixNano()%1_000_000_000)
	slug := pleskSlug(sub)
	out := "/tmp/cpmove-" + slug + ".tar.gz"
	t.Cleanup(func() { os.Remove(out) })

	var captured string
	s := &session{execFn: func(_ context.Context, _ time.Duration, cmd string) ([]byte, error) {
		captured = cmd
		return []byte(out + "\n"), nil
	}}
	if _, err := New().BackupUser(context.Background(), s, sub); err != nil {
		t.Fatalf("BackupUser: %v", err)
	}

	bin := t.TempDir()
	docroot := t.TempDir()
	stub := `#!/bin/sh
q="$3"
case "$q" in
  *c.email*) echo "owner@example.net" ;;
  *"SELECT name FROM domains"*) echo "$SUB_A"; echo "shop.$SUB_A" ;;
  *www_root*) echo "$DOCROOT" ;;
  *php_handler_id*) case "$q" in *"'shop."*) echo "fpm" ;; *) echo "plesk-php83-fpm" ;; esac ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "plesk"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", captured)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "SUB_A="+sub, "DOCROOT="+docroot)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, b)
	}

	x := t.TempDir()
	if b, err := exec.Command("tar", "-xzf", out, "-C", x).CombinedOutput(); err != nil {
		t.Fatalf("untar: %v\n%s", err, b)
	}
	root := filepath.Join(x, "cpmove-"+slug)
	if b, _ := os.ReadFile(filepath.Join(root, "cp", slug)); !strings.Contains(string(b), "CONTACTEMAIL=owner@example.net") {
		t.Errorf("cp/%s = %q, want the owner's CONTACTEMAIL", slug, b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "userdata", sub)); string(b) != "phpversion: 8.3\n" {
		t.Errorf("userdata/%s = %q, want \"phpversion: 8.3\\n\"", sub, b)
	}
	if _, err := os.Stat(filepath.Join(root, "userdata", "shop."+sub)); err == nil {
		t.Error("a domain on the OS-vendor handler got a userdata PHP version")
	}
}

// pullCaller is the method set migrate_pull_cmd.go's pullPlesk relies on.
// This compile-time assertion catches a signature drift in BackupUser /
// PullFile / RemoveRemote before it breaks the pull command's build.
type pullCaller interface {
	BackupUser(ctx context.Context, raw interface{}, account string) (string, error)
	PullFile(ctx context.Context, raw interface{}, remotePath, localPath string) (int64, error)
	RemoveRemote(ctx context.Context, raw interface{}, path string) error
}

var _ pullCaller = (*Discoverer)(nil)

// GH #429 follow-up: the synthesis script must bundle the subscription
// sysuser's crontab as a TOP-LEVEL `cron` file (cPanel full-backup-wizard
// layout — that's what cpanel.ParseTarball classifies into CronFiles), and
// must drop the file again when the crontab is empty so the import stage
// never sees a zero-byte cron area.
func TestBackupUser_ScriptBundlesSysuserCrontab(t *testing.T) {
	d := New()
	var captured string
	s := &session{execFn: func(_ context.Context, _ time.Duration, cmd string) ([]byte, error) {
		captured = cmd
		return []byte("/tmp/cpmove-example_com.tar.gz\n"), nil
	}}
	if _, err := d.BackupUser(context.Background(), s, "example.com"); err != nil {
		t.Fatalf("BackupUser: %v", err)
	}
	for _, want := range []string{
		"sys_users",                       // sysuser resolved from psa, not guessed
		`crontab -l -u "$SYSUSER"`,        // real crontab read
		`"$TMP/cpmove-$SLUG/cron"`,        // top-level cron FILE inside the cpmove
		`[ -s "$TMP/cpmove-$SLUG/cron" ]`, // empty crontab -> file removed
	} {
		if !strings.Contains(captured, want) {
			t.Errorf("synthesis script missing %q", want)
		}
	}
	// The cron section must run BEFORE the tar step, or the file never
	// lands in the bundle.
	if strings.Index(captured, `crontab -l`) > strings.Index(captured, `tar -czf`) {
		t.Error("cron section must precede the tar step")
	}
}
