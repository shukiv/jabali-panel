package commands

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// twoSampleDev returns a reader that hands out `first` then `second` on
// successive calls, so the collector sees a real delta without touching
// the host's /proc/net/dev.
func twoSampleDev(first, second string) func() (string, error) {
	n := 0
	return func() (string, error) {
		n++
		if n == 1 {
			return first, nil
		}
		return second, nil
	}
}

// restoreNetTel snapshots the package-level sampling vars and clears the
// single-flight cache so each test starts clean.
func restoreNetTel(t *testing.T) {
	t.Helper()
	origRead, origNow, origWait := netTelReadDev, netTelNowFn, netTelWait
	netTelMu.Lock()
	netTelLast, netTelLastAt = nil, time.Time{}
	netTelMu.Unlock()
	t.Cleanup(func() {
		netTelReadDev, netTelNowFn, netTelWait = origRead, origNow, origWait
		netTelMu.Lock()
		netTelLast, netTelLastAt = nil, time.Time{}
		netTelMu.Unlock()
	})
}

// A /proc/net/dev line has: iface: rxBytes rxPackets rxErrs rxDrop fifo
// frame compressed multicast txBytes txPackets txErrs txDrop ...
// (16 numeric columns). Only the columns we read need to be realistic.
func devLine(iface string, rxBytes, rxPkts, rxErr, rxDrop, txBytes, txPkts uint64) string {
	return iface + ": " +
		u(rxBytes) + " " + u(rxPkts) + " " + u(rxErr) + " " + u(rxDrop) +
		" 0 0 0 0 " +
		u(txBytes) + " " + u(txPkts) + " 0 0 0 0 0 0\n"
}

func u(v uint64) string { b, _ := json.Marshal(v); return string(b) }

func TestNetTelemetry_ComputesRatesAndLoss(t *testing.T) {
	restoreNetTel(t)

	header := "Inter-|   Receive ...\n face |bytes ...\n"
	// lo must be excluded from the aggregate; eth0 carries the traffic.
	first := header +
		devLine("lo", 1000, 10, 0, 0, 1000, 10) +
		devLine("eth0", 1_000_000, 1000, 5, 5, 500_000, 800)
	second := header +
		devLine("lo", 2000, 20, 0, 0, 2000, 20) +
		devLine("eth0", 1_030_000, 1090, 6, 9, 515_000, 830)
	// eth0 window deltas: rx bytes 30_000, tx 15_000, rx pkts 90,
	// lost = (6-5)+(9-5) = 5. Over a 3s window.
	netTelReadDev = twoSampleDev(first, second)
	// Deterministic 3-second window via a fake clock.
	base := time.Unix(1_700_000_000, 0)
	calls := 0
	netTelNowFn = func() time.Time {
		// first=start, second=end(+3s); reused for cache checks.
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(3 * time.Second)
	}
	netTelWait = func(_ context.Context, _ time.Duration) error { return nil }

	raw, err := systemNetTelemetryHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	got := raw.(NetTelemetry)
	if got.WindowSeconds != netTelDefaultWindow {
		t.Errorf("window = %d, want %d", got.WindowSeconds, netTelDefaultWindow)
	}
	if got.DownloadBps != 10_000 { // 30_000 bytes / 3s
		t.Errorf("download_bps = %d, want 10000", got.DownloadBps)
	}
	if got.UploadBps != 5_000 { // 15_000 bytes / 3s
		t.Errorf("upload_bps = %d, want 5000", got.UploadBps)
	}
	// loss = 5 / (90 + 5) * 100 ≈ 5.263%
	if got.PacketLossPct < 5.2 || got.PacketLossPct > 5.3 {
		t.Errorf("packet_loss_pct = %v, want ~5.26", got.PacketLossPct)
	}
	if got.SampledAt == "" {
		t.Error("sampled_at must be set")
	}
}

func TestNetTelemetry_CounterResetClampsToZero(t *testing.T) {
	restoreNetTel(t)
	header := "h\nh\n"
	// second < first (NIC reset) → deltas clamp to 0, not a huge uint.
	first := header + devLine("eth0", 5_000_000, 5000, 0, 0, 5_000_000, 5000)
	second := header + devLine("eth0", 10, 1, 0, 0, 10, 1)
	netTelReadDev = twoSampleDev(first, second)
	base := time.Unix(1_700_000_000, 0)
	first_call := true
	netTelNowFn = func() time.Time {
		if first_call {
			first_call = false
			return base
		}
		return base.Add(3 * time.Second)
	}
	netTelWait = func(_ context.Context, _ time.Duration) error { return nil }

	raw, err := systemNetTelemetryHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	got := raw.(NetTelemetry)
	if got.DownloadBps != 0 || got.UploadBps != 0 {
		t.Errorf("reset should clamp rates to 0, got dl=%d ul=%d", got.DownloadBps, got.UploadBps)
	}
	if got.PacketLossPct != 0 {
		t.Errorf("reset should give 0 loss, got %v", got.PacketLossPct)
	}
}

func TestNetTelemetry_WindowClamped(t *testing.T) {
	restoreNetTel(t)
	netTelReadDev = twoSampleDev("h\nh\n", "h\nh\n")
	netTelWait = func(_ context.Context, _ time.Duration) error { return nil }

	raw, err := systemNetTelemetryHandler(context.Background(), json.RawMessage(`{"window_seconds":999}`))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if got := raw.(NetTelemetry).WindowSeconds; got != netTelMaxWindow {
		t.Errorf("window clamped to %d, want %d", got, netTelMaxWindow)
	}
}
