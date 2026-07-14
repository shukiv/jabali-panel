package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// netTelemetryParams tunes the sampling window. The collector reads
// /proc/net/dev twice, WindowSeconds apart, and reports the aggregate WAN
// throughput + packet loss over that window. Unlike system.network (which
// keeps per-iface state across calls and needs a warm-up call), this is
// fully self-contained, so a single stateless request — e.g. the public
// automation status API — gets real numbers on the first hit.
type netTelemetryParams struct {
	WindowSeconds int `json:"window_seconds,omitempty"`
}

// NetTelemetry is the aggregated WAN telemetry over one sampling window.
// Rates are BYTES per second, consistent with system.network's rx_bps /
// tx_bps (also byte-rates). PacketLossPct is derived from interface rx
// error+drop counters over the window — it reflects NIC/driver loss, NOT
// ICMP path loss (the collector issues no egress ping).
type NetTelemetry struct {
	DownloadBps   uint64  `json:"download_bps"`
	UploadBps     uint64  `json:"upload_bps"`
	PacketLossPct float64 `json:"packet_loss_pct"`
	WindowSeconds int     `json:"window_seconds"`
	SampledAt     string  `json:"sampled_at"`
}

const (
	netTelDefaultWindow = 3
	netTelMinWindow     = 1
	netTelMaxWindow     = 10
	netTelCacheTTL      = 2 * time.Second
)

var (
	// Single-flight guard: the mutex is held across the whole sampling
	// window so two concurrent callers never run overlapping windows. A
	// short result cache collapses back-to-back polls onto one sample
	// rather than blocking each caller for a fresh window.
	netTelMu     sync.Mutex
	netTelLast   *NetTelemetry
	netTelLastAt time.Time

	// vars for tests: fixture reader, clock, and a ctx-aware wait.
	netTelReadDev = func() (string, error) {
		b, err := os.ReadFile(netDevPath)
		return string(b), err
	}
	netTelNowFn = time.Now
	netTelWait  = func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
)

func systemNetTelemetryHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p netTelemetryParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
		}
	}
	window := p.WindowSeconds
	switch {
	case window == 0:
		window = netTelDefaultWindow
	case window < netTelMinWindow:
		window = netTelMinWindow
	case window > netTelMaxWindow:
		window = netTelMaxWindow
	}

	netTelMu.Lock()
	defer netTelMu.Unlock()

	// A very-recent sample is reused rather than re-blocking for another
	// window — cheap protection against a burst of status polls.
	if netTelLast != nil && netTelNowFn().Sub(netTelLastAt) < netTelCacheTTL {
		cached := *netTelLast
		return cached, nil
	}

	first, err := netTelReadDev()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read %s: %v", netDevPath, err)}
	}
	start := netTelNowFn()
	if err := netTelWait(ctx, time.Duration(window)*time.Second); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("sampling interrupted: %v", err)}
	}
	second, err := netTelReadDev()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("read %s: %v", netDevPath, err)}
	}
	end := netTelNowFn()

	elapsed := end.Sub(start).Seconds()
	if elapsed <= 0 {
		elapsed = float64(window)
	}

	out := aggregateNetTelemetry(parseProcNetDev(first), parseProcNetDev(second), elapsed, window)
	out.SampledAt = end.UTC().Format(time.RFC3339)

	netTelLast = &out
	netTelLastAt = end
	return out, nil
}

// aggregateNetTelemetry sums the byte/packet/error/drop deltas across every
// non-loopback interface between two /proc/net/dev samples. Counter resets
// (NIC reset, rollover) collapse to 0 via the guarded subtraction rather
// than casting a negative to a giant uint64.
func aggregateNetTelemetry(before, after []procNetDevRow, elapsed float64, window int) NetTelemetry {
	prev := make(map[string]procNetDevRow, len(before))
	for _, r := range before {
		prev[r.iface] = r
	}
	var rxBytes, txBytes, rxPackets, lostPackets uint64
	for _, cur := range after {
		if cur.iface == "lo" {
			continue
		}
		p, ok := prev[cur.iface]
		if !ok {
			continue // iface appeared mid-window; no trustworthy delta
		}
		rxBytes += subCounter(cur.rxBytes, p.rxBytes)
		txBytes += subCounter(cur.txBytes, p.txBytes)
		rxPackets += subCounter(cur.rxPackets, p.rxPackets)
		lostPackets += subCounter(cur.rxErrors, p.rxErrors) + subCounter(cur.rxDrop, p.rxDrop)
	}
	out := NetTelemetry{
		DownloadBps:   uint64(float64(rxBytes) / elapsed),
		UploadBps:     uint64(float64(txBytes) / elapsed),
		WindowSeconds: window,
	}
	// Loss over the window = lost / (received + lost). Zero traffic → 0.
	if total := rxPackets + lostPackets; total > 0 {
		out.PacketLossPct = 100 * float64(lostPackets) / float64(total)
	}
	return out
}

// subCounter is rateDelta's sibling without the divide — guards against
// counter resets by returning 0 when the counter went backwards.
func subCounter(curr, prev uint64) uint64 {
	if curr < prev {
		return 0
	}
	return curr - prev
}

func init() {
	Default.Register("system.net_telemetry", systemNetTelemetryHandler)
}
