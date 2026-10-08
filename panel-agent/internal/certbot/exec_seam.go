package certbot

import (
	"os"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/execstub"
)

// Test-safety for GH #994 / #1160.
//
// internal/certbot shells out to `certbot` (see runner.go), which for a real
// issuance runs `certbot certonly ...` — a live ACME order. The commands seam
// (#1159) can't cover this because certbot has its own exec, and it's reached
// TRANSITIVELY: a commands test that calls an SSL handler → certbot.NewRunner()
// would hit real certbot/ACME on any box where it's installed. A per-package
// TestMain can't fix that (it can't reach this package's state from the commands
// test binary), so the default binary is chosen at NewRunner time based on
// testing.Testing(): under `go test`, unless the operator opts into real host
// mutation, NewRunner points Binary at a harmless no-op stub. Production
// binaries (testing.Testing() == false) always get the real `certbot`. Tests
// that need real certbot output inject their own Binary after NewRunner (as
// runner_test.go does), which overrides this default.

// defaultCertbotBinary is "certbot" in production, or a no-op stub under tests
// (unless JABALI_ALLOW_HOST_MUTATION=1).
func defaultCertbotBinary() string {
	if testing.Testing() && os.Getenv("JABALI_ALLOW_HOST_MUTATION") != "1" {
		return certbotTestStub()
	}
	return "certbot"
}

var (
	certbotStubOnce sync.Once
	certbotStubPath string
)

// certbotTestStub writes (once) a shell script that drains stdin and exits 0 with
// no output, and returns its path. It stands in for `certbot` so a test that
// gets past validation reaches a harmless binary instead of real ACME. Tests
// asserting parsed certbot output inject their own fake Binary instead.
func certbotTestStub() string {
	certbotStubOnce.Do(func() {
		p, err := execstub.NoOpBinary("certbot")
		if err != nil {
			panic("certbot test stub: " + err.Error())
		}
		certbotStubPath = p
	})
	return certbotStubPath
}
