package cyberpanel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// captureSession returns a *session whose run() records the last command and
// replies with canned stdout, exercising the synthesize script without a live
// CyberPanel host.
func captureSession(stdout string, runErr error) (*session, *string) {
	var last string
	s := &session{dbName: "cyberpanel", commandTimeout: time.Second}
	s.run = func(_ context.Context, _ time.Duration, cmd string) ([]byte, error) {
		last = cmd
		return []byte(stdout), runErr
	}
	return s, &last
}

func TestBackupUser_SynthesizeScriptAndPath(t *testing.T) {
	d := New()
	s, last := captureSession("log\n/tmp/cpmove-smoke.jabalitest.com.tar.gz\n", nil)
	path, err := d.BackupUser(context.Background(), s, "smoke.jabalitest.com")
	if err != nil {
		t.Fatalf("BackupUser: %v", err)
	}
	if path != "/tmp/cpmove-smoke.jabalitest.com.tar.gz" {
		t.Errorf("path = %q, want the trailing tarball path", path)
	}
	for _, want := range []string{
		`set -e`,
		`BASE="$TMP/cpmove-$ACCT"`,
		`ACCT='smoke.jabalitest.com'`, // account interpolated + quoted
		`SELECT id FROM websiteFunctions_websites WHERE domain=`, // website id
		`externalApp`, // Linux user for cron
		`echo "USER=$ACCT"`,
		`FROM databases_databases WHERE website_id=$WID`, // per-db dump
		`mysqldump`,
		`domains-paths.txt`,
		`websiteFunctions_childdomains`, // child domains
		`websiteFunctions_aliasdomains`, // alias domains
		`crontab -u "$EXTAPP" -l`,       // cron
		`TARARGS="cpmove-$ACCT/cp"`,     // cp/ archived FIRST (ParseTarball wrapper detection)
		`tar -czf "$OUT"`,
	} {
		if !strings.Contains(*last, want) {
			t.Errorf("synthesize script missing %q", want)
		}
	}
	// externalApp must be re-validated in-shell before crontab -u.
	if !strings.Contains(*last, `grep -Eq '^[a-z_][a-z0-9._-]{0,63}$'`) {
		t.Error("externalApp must be allow-listed in-shell before reaching crontab -u")
	}
}

func TestBackupUser_RejectsBadAccount(t *testing.T) {
	d := New()
	s, _ := captureSession("/tmp/cpmove-x.tar.gz\n", nil)
	for _, bad := range []string{"a'; DROP TABLE websiteFunctions_websites;--", "a b", "../etc", "UPPER.com", ""} {
		if _, err := d.BackupUser(context.Background(), s, bad); err == nil {
			t.Errorf("account %q must be rejected by domainRe before any remote exec", bad)
		}
	}
}

func TestBackupUser_EmptyPathIsError(t *testing.T) {
	d := New()
	s, _ := captureSession("  \n", nil)
	if _, err := d.BackupUser(context.Background(), s, "smoke.jabalitest.com"); err == nil {
		t.Error("empty tarball path must be an error")
	}
}

func TestBackupUser_RunErrorSurfaced(t *testing.T) {
	d := New()
	s, _ := captureSession("partial", errors.New("boom"))
	if _, err := d.BackupUser(context.Background(), s, "smoke.jabalitest.com"); err == nil ||
		!strings.Contains(err.Error(), "synthesize cpmove") {
		t.Errorf("run error must be wrapped, got %v", err)
	}
}

func TestRemoveRemote_AllowlistsCpmove(t *testing.T) {
	d := New()
	s, last := captureSession("", nil)
	for _, bad := range []string{"/etc/passwd", "/tmp/cpmove-../etc", "/root/.my.cnf"} {
		if err := d.RemoveRemote(context.Background(), s, bad); err == nil {
			t.Errorf("RemoveRemote must refuse %q", bad)
		}
	}
	if err := d.RemoveRemote(context.Background(), s, "/tmp/cpmove-smoke.jabalitest.com.tar.gz"); err != nil {
		t.Fatalf("RemoveRemote(valid): %v", err)
	}
	if !strings.Contains(*last, "rm -f") {
		t.Errorf("RemoveRemote should issue rm -f, got %q", *last)
	}
}
