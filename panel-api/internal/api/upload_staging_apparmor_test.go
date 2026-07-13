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

	// uploadStagingDir is "/var/lib/jabali-uploads" (files.go). The
	// profile must grant rw to both the dir itself and its contents.
	dir := strings.TrimRight(uploadStagingDir, "/") + "/"

	// Match e.g. `/var/lib/jabali-uploads/**  rwk,` — the path, some
	// whitespace, then a permission set containing at least r and w.
	for _, pathGlob := range []string{regexp.QuoteMeta(dir), regexp.QuoteMeta(dir) + `\*\*`} {
		re := regexp.MustCompile(`(?m)^\s*` + pathGlob + `\s+[a-z]*r[a-z]*w[a-z]*\s*,`)
		if !re.MatchString(profile) {
			t.Errorf("AppArmor profile is missing an rw rule for %q — file-manager uploads will EACCES under enforce mode (GH #355). Add `%s rw,`", pathGlob, dir)
		}
	}
}
