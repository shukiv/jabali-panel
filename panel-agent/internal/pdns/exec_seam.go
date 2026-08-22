package pdns

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Test-safety for GH #994 / #1160.
//
// runPdnsutil (dnssec.go) shells out to `pdnsutil`, whose secure-zone / rectify
// / disable operations MUTATE the host's DNSSEC state. The mutating operations
// are reachable transitively from the commands package (the dns_dnssec_enable /
// _disable handlers call pdns.SecureZone / pdns.DisableDNSSEC), so a commands
// test that exercised those handlers would run real `pdnsutil` — the same
// cross-package exec gap that bit internal/certbot. As with certbot, the default
// binary is chosen by testing.Testing(): under `go test`, unless the operator
// opts into real host mutation, pdnsutilBinary points at a harmless no-op stub.
// Production binaries always get real `/usr/bin/pdnsutil`. Package tests that
// need real/fake pdnsutil output set pdnsutilBinary themselves (dnssec.go's
// contract), which overrides this default.

// defaultPdnsutilBinary is /usr/bin/pdnsutil in production, or a no-op stub under
// tests (unless JABALI_ALLOW_HOST_MUTATION=1).
func defaultPdnsutilBinary() string {
	if testing.Testing() && os.Getenv("JABALI_ALLOW_HOST_MUTATION") != "1" {
		return pdnsutilTestStub()
	}
	return "/usr/bin/pdnsutil"
}

var (
	pdnsutilStubOnce sync.Once
	pdnsutilStubPath string
)

func pdnsutilTestStub() string {
	pdnsutilStubOnce.Do(func() {
		dir, err := os.MkdirTemp("", "jabali-pdnsutil-stub-")
		if err != nil {
			panic("pdnsutil test stub: " + err.Error())
		}
		p := filepath.Join(dir, "pdnsutil")
		if werr := os.WriteFile(p, []byte("#!/bin/sh\ncat >/dev/null 2>&1\nexit 0\n"), 0o700); werr != nil {
			panic("pdnsutil test stub: " + werr.Error())
		}
		pdnsutilStubPath = p
	})
	return pdnsutilStubPath
}
