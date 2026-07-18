package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestUploadStagingDirHasAppArmorRule guards GH #355: the panel-api
// AppArmor profile (jabali-panel) must grant read-write to the upload
// staging dir, or the daemon EACCESes the open() of every file-manager
// upload once the profile matures from complain to enforce mode.
//
// The staging dir is listed in the systemd unit's ReadWritePaths, but
// systemd path allow-listing and AppArmor file mediation are independent
// layers — an ReadWritePaths entry without a matching profile rule fails
// silently in complain and hard-fails in enforce. This test pins the two
// together so a future edit to uploadStagingDir (or the profile) can't
// reopen the regression.
func TestUploadStagingDirHasAppArmorRule(t *testing.T) {
	const profilePath = "../../../install/apparmor/usr.local.bin.jabali-panel-api"

	raw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read AppArmor profile %s: %v", profilePath, err)
	}
	profile := string(raw)

	// Every jabali-owned ReadWritePaths dir the daemon writes in-process
	// must also have a matching AppArmor rw rule — systemd path
	// allow-listing and AppArmor file mediation are independent layers.
	// - uploadStagingDir (files.go): file-manager uploads (GH #355).
	// - /var/lib/jabali-migrations (admin_migrations.go migrationStagingDir):
	//   offline cPanel/HestiaCP tarball upload via uploadTarball.
	cases := []struct {
		dir, why string
	}{
		{strings.TrimRight(uploadStagingDir, "/") + "/", "file-manager uploads (GH #355)"},
		{"/var/lib/jabali-migrations/", "offline migration tarball upload"},
	}

	for _, tc := range cases {
		// Match e.g. `/var/lib/jabali-uploads/**  rwk,` — the path, some
		// whitespace, then a permission set containing at least r and w.
		for _, pathGlob := range []string{regexp.QuoteMeta(tc.dir), regexp.QuoteMeta(tc.dir) + `\*\*`} {
			re := regexp.MustCompile(`(?m)^\s*` + pathGlob + `\s+[a-z]*r[a-z]*w[a-z]*\s*,`)
			if !re.MatchString(profile) {
				t.Errorf("AppArmor profile is missing an rw rule for %q — %s will EACCES under enforce mode. Add `%s rw,`", pathGlob, tc.why, tc.dir)
			}
		}
	}
}

// TestBackupDownloadHasAppArmorRules guards GH #462: the backup-artifact
// download tars the agent-materialized snapshot, so the panel-api profile must
// (a) allow exec of tar and (b) grant read on the downloads staging dir — or the
// daemon fails "fork/exec /usr/bin/tar: permission denied", then EACCESes the
// files, once the profile is in enforce mode.
func TestBackupDownloadHasAppArmorRules(t *testing.T) {
	const profilePath = "../../../install/apparmor/usr.local.bin.jabali-panel-api"
	raw, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read AppArmor profile: %v", err)
	}
	profile := string(raw)

	// tar must be exec'able (ix/rix) — it's the download's transport.
	if !regexp.MustCompile(`(?m)^\s*/usr/bin/tar\s+r?ix\s*,`).MatchString(profile) {
		t.Error("AppArmor profile missing an exec rule for /usr/bin/tar (GH #462) — backup download fails 'fork/exec /usr/bin/tar: permission denied' under enforce")
	}

	// The agent-materialized snapshot dir must be readable so tar can read it.
	for _, g := range []string{
		regexp.QuoteMeta("/var/lib/jabali-backups/downloads/"),
		regexp.QuoteMeta("/var/lib/jabali-backups/downloads/") + `\*\*`,
	} {
		if !regexp.MustCompile(`(?m)^\s*` + g + `\s+[a-z]*r[a-z]*\s*,`).MatchString(profile) {
			t.Errorf("AppArmor profile missing a read rule for %q (GH #462) — tar EACCESes the backup files under enforce", g)
		}
	}
}
