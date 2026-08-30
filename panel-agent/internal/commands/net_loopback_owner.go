// net.loopback_listener_uid (GH #1401 follow-up): report whether a TCP port is
// already LISTENing on the loopback path and, if so, the lowest owning uid.
//
// The reverse-proxy feature lets a tenant point a public domain at
// 127.0.0.1:<port>. A panel-side constant denylist blocks the KNOWN jabali
// infra ports, but that list can drift as new services are added. This verb is
// the drift-proof backstop: the panel refuses a tenant-chosen port that is
// already held by a SYSTEM/SERVICE process (uid < 1000 = below the tenant uid
// floor) before it writes the proxy_pass — so a domain can never be aimed at
// the panel, a database, the mail stack, etc. even if the constant list misses
// a new one. A port bound by the tenant's own app (uid >= 1000), or not bound
// at all (the app simply isn't started yet), is allowed.
//
// A listener reachable via 127.0.0.1:<port> is one bound to the loopback
// address, OR to the any-address (0.0.0.0 / ::) which also answers on loopback.
// A listener bound only to a specific public IP is NOT reachable on 127.0.0.1
// and is ignored.
package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type netLoopbackOwnerParams struct {
	Port int `json:"port"`
}

type netLoopbackOwnerResult struct {
	Bound bool `json:"bound"`
	UID   int  `json:"uid"` // lowest owning uid when bound; -1 when not bound
}

// tcpStateListen is the /proc/net/tcp st value for LISTEN.
const tcpStateListen = "0A"

func netLoopbackOwnerHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p netLoopbackOwnerParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "malformed JSON body"}
	}
	if p.Port < 1 || p.Port > 65535 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "port must be 1-65535"}
	}
	uid, bound := lowestLoopbackListenerUID(p.Port)
	if !bound {
		return netLoopbackOwnerResult{Bound: false, UID: -1}, nil
	}
	return netLoopbackOwnerResult{Bound: true, UID: uid}, nil
}

// lowestLoopbackListenerUID scans /proc/net/tcp + tcp6 for a LISTEN socket on
// `port` reachable via loopback and returns the lowest owning uid found.
func lowestLoopbackListenerUID(port int) (int, bool) {
	best := -1
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if uid, ok := scanProcNetTCP(path, port); ok && (best == -1 || uid < best) {
			best = uid
		}
	}
	if best == -1 {
		return 0, false
	}
	return best, true
}

// scanProcNetTCP returns the lowest uid of any LISTEN socket in `path` whose
// local port == port and whose local address is loopback or the any-address.
func scanProcNetTCP(path string, port int) (int, bool) {
	f, err := os.Open(path) //nolint:gosec // fixed procfs path
	if err != nil {
		return 0, false
	}
	defer f.Close()
	best := -1
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := true
	for sc.Scan() {
		if first { // header row
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		if fields[3] != tcpStateListen {
			continue
		}
		addr, lport, ok := splitHexAddrPort(fields[1])
		if !ok || lport != port {
			continue
		}
		if !isLoopbackOrAnyHex(addr) {
			continue
		}
		uid, err := strconv.Atoi(fields[7])
		if err != nil {
			continue
		}
		if best == -1 || uid < best {
			best = uid
		}
	}
	if best == -1 {
		return 0, false
	}
	return best, true
}

// splitHexAddrPort splits a /proc/net/tcp "HEXADDR:HEXPORT" local_address into
// the hex address and the decimal port.
func splitHexAddrPort(s string) (string, int, bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", 0, false
	}
	port, err := strconv.ParseInt(s[i+1:], 16, 32)
	if err != nil {
		return "", 0, false
	}
	return s[:i], int(port), true
}

// isLoopbackOrAnyHex reports whether a /proc/net/tcp hex local address is a
// loopback or any-address (both answer on 127.0.0.1/::1). IPv4 (8 hex chars):
// 0100007F = 127.0.0.1, 00000000 = 0.0.0.0. IPv6 (32 hex chars): the ::1
// loopback (…01000000) and :: any (all zero); mapped ::ffff:127.0.0.1 and
// mapped-any are covered too.
func isLoopbackOrAnyHex(addr string) bool {
	switch len(addr) {
	case 8: // IPv4
		return addr == "0100007F" || addr == "00000000"
	case 32: // IPv6
		if addr == strings.Repeat("0", 32) { // ::
			return true
		}
		if strings.EqualFold(addr, "00000000000000000000000001000000") { // ::1
			return true
		}
		// ::ffff:127.0.0.1 (v4-mapped loopback) and ::ffff:0.0.0.0 (v4-mapped any)
		if strings.EqualFold(addr, "0000000000000000FFFF00000100007F") {
			return true
		}
		if strings.EqualFold(addr, "0000000000000000FFFF000000000000") {
			return true
		}
	}
	return false
}

func init() {
	Default.Register("net.loopback_listener_uid", netLoopbackOwnerHandler)
}
