package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsLoopbackOrAnyHex(t *testing.T) {
	yes := []string{
		"0100007F", // 127.0.0.1
		"00000000", // 0.0.0.0
		"00000000000000000000000001000000", // ::1
		"00000000000000000000000000000000", // ::
		"0000000000000000FFFF00000100007F", // ::ffff:127.0.0.1
	}
	for _, a := range yes {
		if !isLoopbackOrAnyHex(a) {
			t.Errorf("%s should be loopback/any", a)
		}
	}
	no := []string{
		"0101A8C0",                         // 192.168.1.1 (public-ish, LE)
		"3CEC36B6",                         // some public IPv4
		"FE800000000000000000000000000001", // link-local v6
	}
	for _, a := range no {
		if isLoopbackOrAnyHex(a) {
			t.Errorf("%s must NOT count as loopback/any", a)
		}
	}
}

func TestScanProcNetTCP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	// port 5432 = 0x1538, 6875 = 0x1ADB, 8443 = 0x20FB
	content := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000   106        0 111 1 x
   1: 0100007F:1ADB 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 222 1 x
   2: 0100007F:1ADB C0A80101:E4C2 01 00000000:00000000 00:00000000 00000000     0        0 333 1 x
   3: 3CEC36B6:20FB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 444 1 x
   4: 00000000:2328 00000000:0000 0A 00000000:00000000 00:00000000 00000000    33        0 555 1 x
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// 5432 → postgres, uid 106 (system).
	if uid, ok := scanProcNetTCP(path, 5432); !ok || uid != 106 {
		t.Errorf("5432: got uid=%d ok=%v, want 106/true", uid, ok)
	}
	// 6875 → LISTEN uid 1000 (tenant); the ESTABLISHED (st=01) row on the same
	// port must be ignored, so the result is the LISTEN uid.
	if uid, ok := scanProcNetTCP(path, 6875); !ok || uid != 1000 {
		t.Errorf("6875: got uid=%d ok=%v, want 1000/true", uid, ok)
	}
	// 8443 (0x20FB) is bound only on a public IP (3CEC36B6), not loopback/any →
	// not reachable via 127.0.0.1 → not found.
	if _, ok := scanProcNetTCP(path, 8443); ok {
		t.Error("8443 bound on a public IP only must not count as loopback-reachable")
	}
	// 9000 (0x2328) on 0.0.0.0 → reachable on loopback, uid 33.
	if uid, ok := scanProcNetTCP(path, 9000); !ok || uid != 33 {
		t.Errorf("9000 (any-addr): got uid=%d ok=%v, want 33/true", uid, ok)
	}
	// unbound port.
	if _, ok := scanProcNetTCP(path, 12345); ok {
		t.Error("an unbound port must return not-found")
	}
}
