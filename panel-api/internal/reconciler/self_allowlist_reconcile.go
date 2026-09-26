// Package reconciler — JAB-195: keep the host's own public IPs out of the
// CrowdSec AppSec WAF's scoring path.
//
// WordPress makes loopback calls to itself (WooCommerce Action Scheduler,
// wp-cron, Elementor migrations, the theme-editor scrape check). Those requests
// leave the box and come back in via the public IP, so AppSec sees them as
// ordinary inbound traffic and scores them. On a production host they were
// scoring rce:15 / anomaly:18 and getting an inline 403 roughly hourly.
//
// The failure is silent. WooCommerce's queue just stops: delayed order emails,
// webhooks, scheduled actions and image regeneration quietly stop happening,
// and the only evidence is a 403 in the vhost access log and /var/log/crowdsec.
// Nothing surfaces in the panel.
//
// Server-to-self traffic should never be WAF-scored, so this pass keeps the
// host's own public IPs in the jabali-managed allowlist. Doing it on the
// reconciler rather than as a one-off entry means it re-asserts itself if the
// allowlist is cleared and follows the host to a new address when public_ipv4
// changes — an operator hand-entry would silently rot at that point.
package reconciler

import (
	"context"
	"encoding/json"
	"net"
	"strings"
)

// selfAllowlistReason is written as the allowlist entry's description. Kept
// stable and recognisable so an operator reading `cscli allowlists inspect`
// can see why the entry exists and that it is machine-managed.
const selfAllowlistReason = "jabali: server's own public IP (loopback/self-originated traffic, JAB-195)"

// reconcileSelfAllowlist ensures every public IP this host owns is present in
// the jabali-managed CrowdSec allowlist, with no expiration.
//
// Idempotent by construction: it lists first and only adds what is missing, so
// a converged host performs one read per tick and no writes.
func (r *Reconciler) reconcileSelfAllowlist(ctx context.Context) {
	if r.agent == nil || r.serverSettings == nil {
		return
	}
	settings, err := r.settingsGet(ctx)
	if err != nil || settings == nil {
		r.log.Debug("self-allowlist reconcile skipped: server_settings unavailable", "error", err)
		return
	}
	// Security module owns CrowdSec. With it off there is no allowlist to
	// maintain and cscli may not even be installed.
	if !settings.SecurityEnabled {
		return
	}

	want := selfPublicIPs(settings.PublicIPv4, settings.PublicIPv6)
	if len(want) == 0 {
		return
	}

	have, err := r.currentAllowlistValues(ctx)
	if err != nil {
		// A missing allowlist surfaces as an empty list, not an error, so a
		// real error here means cscli is unavailable. Skip quietly and retry
		// next tick rather than spamming the log every interval.
		r.log.Debug("self-allowlist reconcile skipped: cannot read allowlist", "error", err)
		return
	}

	for _, ip := range want {
		if have[ip] {
			continue
		}
		if _, err := r.agent.Call(ctx, "security.crowdsec.allowlists.add", map[string]any{
			"value":  ip,
			"reason": selfAllowlistReason,
			// No expiration: the host does not stop being itself.
		}); err != nil {
			r.log.Warn("self-allowlist add failed; WAF may block this host's own loopback requests",
				"ip", ip, "error", err)
			continue
		}
		r.log.Info("allowlisted this host's own public IP for CrowdSec AppSec", "ip", ip)
	}
}

// currentAllowlistValues returns the values already present, as a set.
func (r *Reconciler) currentAllowlistValues(ctx context.Context) (map[string]bool, error) {
	raw, err := r.agent.Call(ctx, "security.crowdsec.allowlists.list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Items []struct {
			Value string `json:"value"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(resp.Items))
	for _, it := range resp.Items {
		if v := strings.TrimSpace(it.Value); v != "" {
			out[v] = true
		}
	}
	return out, nil
}

// selfPublicIPs normalises the configured addresses into the set worth
// allowlisting. Parsing rather than trusting the column keeps a malformed or
// placeholder value from reaching cscli, where the agent would reject it
// anyway — better to notice here than to log a failed add every tick.
//
// Loopback and unspecified addresses are dropped: they never appear as an
// AppSec source, so allowlisting them is noise that only widens the entry list.
func selfPublicIPs(v4, v6 string) []string {
	seen := make(map[string]bool, 2)
	out := make([]string, 0, 2)
	for _, raw := range []string{v4, v6} {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		ip := net.ParseIP(s)
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		norm := ip.String()
		if seen[norm] {
			continue
		}
		seen[norm] = true
		out = append(out, norm)
	}
	return out
}
