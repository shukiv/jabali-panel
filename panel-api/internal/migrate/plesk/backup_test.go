package plesk

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLooksLikePleskSubscription(t *testing.T) {
	ok := []string{"lychee-fruits.co.il", "example.com", "a.b-c_d.net"}
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
	if got := pleskSlug("Lychee-Fruits.CO.il"); got != "lychee_fruits_co_il" {
		t.Errorf("slug = %q, want lychee_fruits_co_il", got)
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
