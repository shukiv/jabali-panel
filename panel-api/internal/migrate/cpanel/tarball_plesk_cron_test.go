package cpanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GH #429 follow-up: the Plesk source (plesk/backup.go BackupUser) bundles
// the subscription sysuser's crontab as a TOP-LEVEL `cron` file inside the
// cpmove-<slug>/ wrapper — the cPanel full-backup-wizard layout, NOT the
// cpmove cron/<user> directory layout. This pins that ParseTarball strips
// the wrapper and classifies that file into CronFiles, because that is the
// entire bridge between the Plesk pull and the shared ImportCron: if this
// classification breaks, the Cron checkbox silently no-ops again.
func TestParseTarball_PleskTopLevelCronFile(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "cpmove-demolab_test.tar.gz")
	writeGzTar(t, tarPath, []tarEntry{
		// cp/<slug> is a FILE in the Plesk-synthesized layout (wizard style).
		{"cpmove-demolab_test/cp/demolab_test", "USER=demolab_test\nDNS=demolab.test\n"},
		{"cpmove-demolab_test/databases.txt", "wp_demolab\tmysql\n"},
		{"cpmove-demolab_test/domains-paths.txt", "demolab.test\t/var/www/vhosts/demolab.test/httpdocs\n"},
		{"cpmove-demolab_test/cron", "15 2 * * * /usr/bin/php /var/www/vhosts/demolab.test/httpdocs/cron.php\n30 4 * * 0 /bin/sh /var/www/vhosts/demolab.test/private/weekly.sh\n"},
	})

	extract := filepath.Join(dir, "extract")
	parsed, err := ParseTarball(tarPath, extract)
	if err != nil {
		t.Fatalf("ParseTarball: %v", err)
	}
	if len(parsed.CronFiles) != 1 {
		t.Fatalf("CronFiles = %v, want exactly the top-level cron file", parsed.CronFiles)
	}
	body, err := os.ReadFile(parsed.CronFiles[0])
	if err != nil {
		t.Fatalf("read cron file: %v", err)
	}
	if !strings.Contains(string(body), "cron.php") || !strings.Contains(string(body), "weekly.sh") {
		t.Fatalf("cron content lost: %q", body)
	}
}
