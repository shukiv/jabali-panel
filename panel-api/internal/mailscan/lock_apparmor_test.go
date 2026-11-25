package mailscan

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The tick flocks lockPath. Under the panel's AppArmor profile in enforce
// mode, flock needs the k permission on that path, or every tick fails with
// "permission denied" while the lock file itself opens fine.
func TestLockPathAppArmorAllowsFlock(t *testing.T) {
	b, err := os.ReadFile("../../../install/apparmor/usr.local.bin.jabali-panel-api")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(lockPath)
	rule := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(dir+"/**") + `\s+[rwlk]*k[rwlk]*,`)
	if !rule.Match(b) {
		t.Fatalf("the panel AppArmor profile has no %s/** rule with k; the mail scan's flock on %s is denied in enforce mode", dir, lockPath)
	}
}
