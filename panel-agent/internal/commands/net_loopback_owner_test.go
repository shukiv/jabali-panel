package commands

import (
	"context"
	"encoding/json"
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
	if uid, ok, _ := scanProcNet(path, 5432, false, false); !ok || uid != 106 {
		t.Errorf("5432: got uid=%d ok=%v, want 106/true", uid, ok)
	}
	// 6875 → LISTEN uid 1000 (tenant); the ESTABLISHED (st=01) row on the same
	// port must be ignored, so the result is the LISTEN uid.
	if uid, ok, _ := scanProcNet(path, 6875, false, false); !ok || uid != 1000 {
		t.Errorf("6875: got uid=%d ok=%v, want 1000/true", uid, ok)
	}
	// 8443 (0x20FB) is bound only on a public IP (3CEC36B6), not loopback/any →
	// not reachable via 127.0.0.1 → not found.
	if _, ok, _ := scanProcNet(path, 8443, false, false); ok {
		t.Error("8443 bound on a public IP only must not count as loopback-reachable")
	}
	// 9000 (0x2328) on 0.0.0.0 → reachable on loopback, uid 33.
	if uid, ok, _ := scanProcNet(path, 9000, false, false); !ok || uid != 33 {
		t.Errorf("9000 (any-addr): got uid=%d ok=%v, want 33/true", uid, ok)
	}
	// unbound port.
	if _, ok, _ := scanProcNet(path, 12345, false, false); ok {
		t.Error("an unbound port must return not-found")
	}
}

const procNetHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func writeProcNet(t *testing.T, dir, name, rows string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(procNetHeader+rows), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A Docker app's host port binds 127.0.0.1 or 0.0.0.0, and 0.0.0.0 collides
// with a socket on any address: any_address counts a listener on a public IP.
func TestScanProcNet_AnyAddress(t *testing.T) {
	// 8443 (0x20FB) LISTENs on a public IP only.
	path := writeProcNet(t, t.TempDir(), "tcp",
		"   0: 3CEC36B6:20FB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 444 1 x\n")
	if _, ok, err := scanProcNet(path, 8443, false, false); ok || err != nil {
		t.Fatalf("loopback-only: ok=%v err=%v, want not found", ok, err)
	}
	if uid, ok, err := scanProcNet(path, 8443, false, true); !ok || uid != 0 || err != nil {
		t.Fatalf("any address: uid=%d ok=%v err=%v, want 0/true/nil", uid, ok, err)
	}
}

// A UDP socket holds its port in any state: an unconnected bound socket is
// st 07, a connected one 01.
func TestScanProcNet_UDP(t *testing.T) {
	// 10000 = 0x2710, 10001 = 0x2711.
	path := writeProcNet(t, t.TempDir(), "udp",
		"   0: 00000000:2710 00000000:0000 07 00000000:00000000 00:00000000 00000000   105        0 1 2 x\n"+
			"   1: 0100007F:2711 0100007F:A000 01 00000000:00000000 00:00000000 00000000  1001        0 2 2 x\n")
	if uid, ok, err := scanProcNet(path, 10000, true, true); !ok || uid != 105 || err != nil {
		t.Fatalf("10000/udp: uid=%d ok=%v err=%v, want 105/true/nil", uid, ok, err)
	}
	if uid, ok, err := scanProcNet(path, 10001, true, true); !ok || uid != 1001 || err != nil {
		t.Fatalf("10001/udp connected: uid=%d ok=%v err=%v, want 1001/true/nil", uid, ok, err)
	}
	// The TCP rule (LISTEN only) would miss both.
	if _, ok, _ := scanProcNet(path, 10000, false, true); ok {
		t.Fatal("st 07 must not count as a TCP listener")
	}
}

// No file is no socket of that family (an IPv6-less kernel has no tcp6). A
// file that can't be read is an error, never "free".
func TestScanProcNet_MissingAndUnreadable(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := scanProcNet(filepath.Join(dir, "tcp6"), 10000, false, true); ok || err != nil {
		t.Fatalf("missing file: ok=%v err=%v, want not found, no error", ok, err)
	}
	unreadable := filepath.Join(dir, "udp")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scanProcNet(unreadable, 10000, true, true); err == nil {
		t.Fatal("an unreadable file must be an error")
	}
	// A file that can't be opened (here ENOTDIR, which root gets too).
	notDir := filepath.Join(dir, "plain")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scanProcNet(filepath.Join(notDir, "tcp"), 10000, false, true); err == nil {
		t.Fatal("a file that can't be opened must be an error")
	}
}

func withProcNetDir(t *testing.T, dir string) {
	t.Helper()
	prev := procNetDir
	procNetDir = dir
	t.Cleanup(func() { procNetDir = prev })
}

func callNetLoopbackOwner(t *testing.T, params string) (netLoopbackOwnerResult, error) {
	t.Helper()
	out, err := netLoopbackOwnerHandler(context.Background(), json.RawMessage(params))
	if err != nil {
		return netLoopbackOwnerResult{}, err
	}
	return out.(netLoopbackOwnerResult), nil
}

func TestNetLoopbackOwnerHandler_ProtocolAndAnyAddress(t *testing.T) {
	dir := t.TempDir()
	withProcNetDir(t, dir)
	// tcp: 10000 LISTENs on a public IP. udp6: 10002 (0x2712) bound on ::.
	writeProcNet(t, dir, "tcp", "   0: 3CEC36B6:2710 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 x\n")
	writeProcNet(t, dir, "udp6", "   0: 00000000000000000000000000000000:2712 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000   0        0 2 2 x\n")

	for _, tc := range []struct {
		params string
		bound  bool
	}{
		{`{"port":10000}`, false}, // loopback only, as before
		{`{"port":10000,"any_address":true}`, true},
		{`{"port":10000,"protocol":"udp","any_address":true}`, false},
		{`{"port":10002,"protocol":"udp","any_address":true}`, true},
		{`{"port":10002,"protocol":"tcp","any_address":true}`, false},
	} {
		got, err := callNetLoopbackOwner(t, tc.params)
		if err != nil || got.Bound != tc.bound {
			t.Errorf("%s: bound=%v err=%v, want %v", tc.params, got.Bound, err, tc.bound)
		}
	}
	if _, err := callNetLoopbackOwner(t, `{"port":10000,"protocol":"sctp"}`); err == nil {
		t.Error("an unknown protocol must be rejected")
	}
}

func TestNetLoopbackOwnerHandler_UnreadableProcIsAnError(t *testing.T) {
	dir := t.TempDir()
	withProcNetDir(t, dir)
	writeProcNet(t, dir, "tcp", "")
	if err := os.Mkdir(filepath.Join(dir, "tcp6"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := callNetLoopbackOwner(t, `{"port":10000,"any_address":true}`); err == nil {
		t.Fatal("a probe that can't read /proc/net/tcp6 must fail, not report the port free")
	}
}
