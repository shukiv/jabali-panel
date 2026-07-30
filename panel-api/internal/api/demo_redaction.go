package api

import (
	"net"
	"sync/atomic"
)

// demo_redaction.go — JAB-179. The demo build is write-block only and applied
// NO redaction to GET responses, so the public demo leaked real third-party
// visitor IPs (Users → Sessions), the host fingerprint + exact software
// versions (Server Status), and infra paths. This is a small centralized layer
// so every sensitive handler redacts through the same helpers and a new
// endpoint that forgets to is the only way to regress.
//
// The flag is process-global (set once at startup from cfg.Demo.Enabled) rather
// than threaded through every handler config, so read-side handlers can redact
// without a config change. Off in production => every helper is a pass-through.

var demoRedactionOn atomic.Bool

// SetDemoRedaction enables/disables demo GET redaction. Called once from app
// startup with cfg.Demo.Enabled.
func SetDemoRedaction(on bool) { demoRedactionOn.Store(on) }

// DemoRedactionEnabled reports whether demo redaction is active.
func DemoRedactionEnabled() bool { return demoRedactionOn.Load() }

// Documentation-range placeholders (RFC 5737 / RFC 3849) — plausible but fake.
const (
	demoPlaceholderIPv4 = "203.0.113.10"
	demoPlaceholderIPv6 = "2001:db8::10"
	demoRedactedText    = "redacted"
)

// demoMaskIP replaces a real IP with a documentation-range placeholder when
// demo redaction is on. Preserves the v4/v6 shape; empty stays empty; a value
// that doesn't parse as an IP is redacted to the v4 placeholder (fail closed).
func demoMaskIP(ip string) string {
	if !demoRedactionOn.Load() || ip == "" {
		return ip
	}
	parsed := net.ParseIP(ip)
	if parsed != nil && parsed.To4() == nil {
		return demoPlaceholderIPv6
	}
	return demoPlaceholderIPv4
}

// demoRedact replaces an arbitrary sensitive string (user-agent, hostname,
// version, path) with a fixed placeholder when demo redaction is on. Empty
// stays empty so "unset" still reads as unset.
func demoRedact(s string) string {
	if !demoRedactionOn.Load() || s == "" {
		return s
	}
	return demoRedactedText
}
