package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemNetworkParams toggles loopback inclusion for debug. Default is
// to skip lo because operators don't care about it on the server-status
// page.
type systemNetworkParams struct {
	IncludeLoopback bool `json:"include_loopback,omitempty"`
}

// NetworkInterface is the per-iface row in system.network's response.
// Rates are computed against the previous sample observed by THIS
// agent process; the first call after agent boot reports rates=0 with
// warming_up=true so the UI can render "—" rather than misleading 0.
type NetworkInterface struct {
	Iface     string   `json:"iface"`
	State     string   `json:"state"` // "UP" | "DOWN"
	MAC       string   `json:"mac,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
	IPv4      []string `json:"ipv4"`
	IPv6      []string `json:"ipv6"`
	RXBps     uint64   `json:"rx_bps"`
	TXBps     uint64   `json:"tx_bps"`
	RXPps     uint64   `json:"rx_pps"`
	TXPps     uint64   `json:"tx_pps"`
	RXErrors  uint64   `json:"rx_errors"`
	TXErrors  uint64   `json:"tx_errors"`
	WarmingUp bool     `json:"warming_up"`
}

type SystemNetworkResponse struct {
	Interfaces []NetworkInterface `json:"interfaces"`
	AsOf       string             `json:"as_of"`
}

// netDevSample captures one /proc/net/dev sample for delta calculation.
type netDevSample struct {
	rxBytes   uint64
	txBytes   uint64
	rxPackets uint64
	txPackets uint64
	rxErrors  uint64
	txErrors  uint64
	at        time.Time
}

// netDevCache holds the last sample per-interface. nowFn + procNetDev
// are vars so tests can substitute fixtures + clock.
var (
	netDevCache    = map[string]netDevSample{}
	netDevCacheMu  sync.Mutex
	netDevPath     = "/proc/net/dev"
	netDevNowFn    = time.Now
)

func systemNetworkHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p systemNetworkParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
		}
	}

	data, err := os.ReadFile(netDevPath)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read %s: %v", netDevPath, err)}
	}
	now := netDevNowFn()
	parsed := parseProcNetDev(string(data))

	// IP / MAC / state via stdlib net.Interfaces — no shell-out needed.
	ifaceMeta := loadInterfaceMeta()

	out := SystemNetworkResponse{
		AsOf:       now.UTC().Format(time.RFC3339Nano),
		Interfaces: make([]NetworkInterface, 0, len(parsed)),
	}

	netDevCacheMu.Lock()
	defer netDevCacheMu.Unlock()

	for _, sample := range parsed {
		if !p.IncludeLoopback && sample.iface == "lo" {
			continue
		}
		row := NetworkInterface{
			Iface:    sample.iface,
			IPv4:     []string{},
			IPv6:     []string{},
			RXErrors: sample.rxErrors,
			TXErrors: sample.txErrors,
		}
		if meta, ok := ifaceMeta[sample.iface]; ok {
			row.State = meta.state
			row.MAC = meta.mac
			row.MTU = meta.mtu
			row.IPv4 = meta.ipv4
			row.IPv6 = meta.ipv6
		} else {
			row.State = "UNKNOWN"
		}
		// Rates from delta vs. the previous observation. First call after
		// agent boot has no prior sample → warming_up=true so the UI
		// doesn't pretend a 5s flat-zero is real.
		prev, hasPrev := netDevCache[sample.iface]
		if !hasPrev {
			row.WarmingUp = true
		} else {
			elapsed := now.Sub(prev.at).Seconds()
			if elapsed > 0 {
				row.RXBps = rateDelta(sample.rxBytes, prev.rxBytes, elapsed)
				row.TXBps = rateDelta(sample.txBytes, prev.txBytes, elapsed)
				row.RXPps = rateDelta(sample.rxPackets, prev.rxPackets, elapsed)
				row.TXPps = rateDelta(sample.txPackets, prev.txPackets, elapsed)
			}
		}
		netDevCache[sample.iface] = netDevSample{
			rxBytes:   sample.rxBytes,
			txBytes:   sample.txBytes,
			rxPackets: sample.rxPackets,
			txPackets: sample.txPackets,
			rxErrors:  sample.rxErrors,
			txErrors:  sample.txErrors,
			at:        now,
		}
		out.Interfaces = append(out.Interfaces, row)
	}

	return out, nil
}

// rateDelta is an unsigned subtraction guarded against counter resets
// (NIC reset, kernel rollover, agent unable to read for a while). On
// reset the previous sample > current → result is 0 instead of a huge
// negative-cast-to-uint64 number that would render as exabytes/sec.
func rateDelta(curr, prev uint64, secs float64) uint64 {
	if curr < prev {
		return 0
	}
	return uint64(float64(curr-prev) / secs)
}

type procNetDevRow struct {
	iface     string
	rxBytes   uint64
	rxPackets uint64
	rxErrors  uint64
	rxDrop    uint64
	txBytes   uint64
	txPackets uint64
	txErrors  uint64
	txDrop    uint64
}

// parseProcNetDev reads /proc/net/dev. Format (after the two header
// lines):
//
//	iface: rxBytes rxPackets rxErrs ... txBytes txPackets txErrs ...
//
// We only need bytes/packets/errors columns; everything else is
// ignored. Unknown malformed rows are skipped (don't fail the whole
// command on one weird line).
func parseProcNetDev(content string) []procNetDevRow {
	var out []procNetDevRow
	for _, line := range strings.Split(content, "\n") {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colon])
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 16 {
			continue
		}
		row := procNetDevRow{iface: name}
		row.rxBytes, _ = strconv.ParseUint(fields[0], 10, 64)
		row.rxPackets, _ = strconv.ParseUint(fields[1], 10, 64)
		row.rxErrors, _ = strconv.ParseUint(fields[2], 10, 64)
		row.rxDrop, _ = strconv.ParseUint(fields[3], 10, 64)
		row.txBytes, _ = strconv.ParseUint(fields[8], 10, 64)
		row.txPackets, _ = strconv.ParseUint(fields[9], 10, 64)
		row.txErrors, _ = strconv.ParseUint(fields[10], 10, 64)
		row.txDrop, _ = strconv.ParseUint(fields[11], 10, 64)
		out = append(out, row)
	}
	return out
}

type ifaceMeta struct {
	state string
	mac   string
	mtu   int
	ipv4  []string
	ipv6  []string
}

// loadInterfaceMeta uses net.Interfaces — pure stdlib, no shell-out.
// Returns empty map on error (caller falls back to UNKNOWN state).
func loadInterfaceMeta() map[string]ifaceMeta {
	out := map[string]ifaceMeta{}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, ifi := range ifaces {
		meta := ifaceMeta{
			mac:  ifi.HardwareAddr.String(),
			mtu:  ifi.MTU,
			ipv4: []string{},
			ipv6: []string{},
		}
		if ifi.Flags&net.FlagUp != 0 {
			meta.state = "UP"
		} else {
			meta.state = "DOWN"
		}
		addrs, err := ifi.Addrs()
		if err == nil {
			for _, addr := range addrs {
				ip := addrIP(addr)
				if ip == nil {
					continue
				}
				if ip.To4() != nil {
					meta.ipv4 = append(meta.ipv4, ip.String())
				} else {
					meta.ipv6 = append(meta.ipv6, ip.String())
				}
			}
		}
		out[ifi.Name] = meta
	}
	return out
}

func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	}
	return nil
}

func init() {
	Default.Register("system.network", systemNetworkHandler)
}
