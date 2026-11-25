package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every agent reload of crowdsec must go through crowdsecReload, which hands
// it to the AppSec watchdog: a SIGHUP can hang crowdsec with AppSec down, and
// a reload outside the wrapper would leave that unnoticed.
func TestCrowdsecReloadsGoThroughTheWatchdog(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	const raw = `"systemctl", "reload", "crowdsec"`
	found := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		n := strings.Count(string(b), raw)
		found += n
		if n > 0 && f != "security_crowdsec.go" {
			t.Errorf("%s reloads crowdsec directly; call crowdsecReload instead", f)
		}
	}
	if found != 1 {
		t.Errorf("want exactly one direct reload (inside crowdsecReload), found %d", found)
	}
}
