package cloudpanel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// captureSession returns a *session whose run() records the last command it was
// handed and replies with a canned stdout, so the synthesize script can be
// asserted without a live CloudPanel host.
func captureSession(stdout string, runErr error) (*session, *string) {
	var last string
	s := &session{dbPath: "/home/clp/htdocs/app/data/db.sq3", commandTimeout: time.Second}
	s.run = func(_ context.Context, _ time.Duration, cmd string) ([]byte, error) {
		last = cmd
		return []byte(stdout), runErr
	}
	return s, &last
}

func TestBackupUser_SynthesizeScriptAndPath(t *testing.T) {
	d := New()
	s, last := captureSession("some log line\n/tmp/cpmove-smokeuser.tar.gz\n", nil)
	path, err := d.BackupUser(context.Background(), s, "smokeuser")
	if err != nil {
		t.Fatalf("BackupUser: %v", err)
	}
	// Returned path is the LAST stdout line (script echoes $OUT last).
	if path != "/tmp/cpmove-smokeuser.tar.gz" {
		t.Errorf("path = %q, want the trailing tarball path", path)
	}
	// The script must build the cpmove subset from CloudPanel's own tables.
	for _, want := range []string{
		`set -e`,                       // abort-on-error
		`BASE="$TMP/cpmove-$ACCT"`,     // cpmove wrapper dir
		`ACCT='smokeuser'`,             // account interpolated + single-quoted
		`echo "USER=$ACCT"`,            // cp/<user> USER line
		`FROM site WHERE user='$ACCT'`, // domains + primary scoping
		`WHERE s.user='$ACCT'`,         // db/cron/ssh scoping to the account
		`mysqldump`,                    // per-db dump
		`d JOIN site s ON d.site_id`,   // db enumeration
		`domains-paths.txt`,            // rsync manifest
		`FROM cron_job cj JOIN site`,   // cron reconstruction
		`FROM ssh_user su JOIN site`,   // authorized_keys
		`TARARGS="cpmove-$ACCT/cp"`,    // cp/ archived FIRST (ParseTarball wrapper detection)
		`tar -czf "$OUT"`,              // final archive
	} {
		if !strings.Contains(*last, want) {
			t.Errorf("synthesize script missing %q", want)
		}
	}
	// set -e safety: no bare `[ ... ] && cmd` that would abort when the test is
	// false (must be if/fi or guarded with || true).
	if strings.Contains(*last, `] && echo`) {
		t.Error("script uses a bare `[ ] && echo` — aborts under set -e when the test is false; use if/fi")
	}
}

func TestBackupUser_RejectsBadAccount(t *testing.T) {
	d := New()
	s, _ := captureSession("/tmp/cpmove-x.tar.gz\n", nil)
	for _, bad := range []string{"a'; rm -rf /;--", "a b", "../etc", "UPPER", ""} {
		if _, err := d.BackupUser(context.Background(), s, bad); err == nil {
			t.Errorf("account %q must be rejected by siteUserRe before any remote exec", bad)
		}
	}
}

func TestBackupUser_EmptyPathIsError(t *testing.T) {
	d := New()
	s, _ := captureSession("   \n", nil) // no path echoed
	if _, err := d.BackupUser(context.Background(), s, "smokeuser"); err == nil {
		t.Error("empty tarball path must be an error")
	}
}

func TestBackupUser_RunErrorSurfaced(t *testing.T) {
	d := New()
	s, _ := captureSession("partial", errors.New("boom"))
	if _, err := d.BackupUser(context.Background(), s, "smokeuser"); err == nil ||
		!strings.Contains(err.Error(), "synthesize cpmove") {
		t.Errorf("run error must be wrapped, got %v", err)
	}
}

func TestRemoveRemote_AllowlistsCpmove(t *testing.T) {
	d := New()
	s, last := captureSession("", nil)
	// Refuses anything outside /tmp/cpmove-*.
	for _, bad := range []string{"/etc/passwd", "/tmp/cpmove-../etc", "/home/clp/htdocs/app/data/db.sq3"} {
		if err := d.RemoveRemote(context.Background(), s, bad); err == nil {
			t.Errorf("RemoveRemote must refuse %q", bad)
		}
	}
	// Accepts the canonical synthesized path.
	if err := d.RemoveRemote(context.Background(), s, "/tmp/cpmove-smokeuser.tar.gz"); err != nil {
		t.Fatalf("RemoveRemote(valid): %v", err)
	}
	if !strings.Contains(*last, "rm -f") {
		t.Errorf("RemoveRemote should issue rm -f, got %q", *last)
	}
}
