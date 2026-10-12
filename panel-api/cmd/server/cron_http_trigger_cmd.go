package main

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

// newCronHTTPTriggerCmd — `jabali cron http-trigger <url>`.
//
// The rebind-safe runtime half of the GH #400 Part B http-trigger crons.
// A constrained curl/wget self-domain cron is stored validated (static
// gate in cronvalidate.ValidateHTTPTrigger), and its systemd unit's
// ExecStart is this command, NOT the raw curl/wget. Running here lets us:
//
//   - resolve the host ONCE, reject any non-routable IP (loopback /
//     link-local / RFC1918 / ULA / 169.254.169.254 metadata), and
//   - pin the validated IP via `curl --resolve host:port:ip` so curl
//     never re-resolves — closing the TOCTOU DNS-rebind window between
//     our check and curl's connect.
//
// Runs as the TENANT uid (systemd-user timer / systemd-run). It MUST NOT
// touch the DB or load secrets — no PreRunE. It only resolves + fetches a
// URL to /dev/null. (Not a privilege boundary: a php cron already has open
// outbound HTTP today; this just keeps the curl/wget path minimal.)
func newCronHTTPTriggerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "http-trigger <url>",
		Short: "Fetch a self-domain URL with SSRF guard + rebind-safe IP pinning (internal cron exec helper)",
		Long: `Internal helper invoked as the ExecStart of a migrated curl/wget cron.
Resolves the URL host, refuses to connect to any loopback / link-local /
RFC1918 / ULA / cloud-metadata address, pins the validated IP so DNS cannot
rebind, then issues a hardened curl GET discarding the body. Exit 0 = the
endpoint was reached (HTTP <400); non-zero = blocked host, DNS failure, or
HTTP error.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true, // invoked by systemd; usage noise is pointless
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 45*time.Second)
			defer cancel()
			return runCronHTTPTrigger(ctx, args[0])
		},
	}
}

func runCronHTTPTrigger(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("url has no host")
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if port != "80" && port != "443" {
		return fmt.Errorf("only ports 80/443 allowed, got %q", port)
	}

	// Resolve A + AAAA. Reject if resolution fails OR if ANY resolved
	// address is non-routable (a domain split between a public and a
	// private/loopback IP is exactly the rebind/SSRF shape we refuse).
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("resolve %s: no addresses", host)
	}
	// Validate EVERY resolved address first (a domain split between a public
	// and a private/loopback IP is exactly the rebind/SSRF shape we refuse),
	// then split by family.
	var v4, v6 []string
	for _, a := range addrs {
		if cronvalidate.IsBlockedIP(a.IP) {
			return fmt.Errorf("refusing %s: resolves to non-routable address %s (SSRF guard)", host, a.IP)
		}
		if a.IP.To4() != nil {
			v4 = append(v4, a.IP.String())
		} else {
			v6 = append(v6, "["+a.IP.String()+"]") // bracket IPv6 for --resolve
		}
	}

	// Pin a SINGLE family and force curl to it. Pinning both families in one
	// --resolve list makes curl prefer IPv6 and NOT fall back to the pinned
	// IPv4 when v6 egress is broken (the common case on hosting boxes) — the
	// trigger then fails despite a perfectly reachable v4. Prefer IPv4 (near
	// universal egress); fall to v6 only when there is no A record. Either
	// way curl connects to a validated, pinned IP, so rebind stays closed.
	famFlag := "--ipv4"
	pinIPs := v4
	if len(pinIPs) == 0 {
		famFlag = "--ipv6"
		pinIPs = v6
	}

	// Fixed, hardened curl GET. --resolve pins the validated IPs so curl
	// does not re-resolve (no rebind). --proto restricts the scheme,
	// --max-redirs 0 blocks redirect-based SSRF, -f fails on HTTP >=400,
	// -o /dev/null discards the body.
	cargs := []string{
		famFlag,
		"--proto", "=http,https",
		"--max-redirs", "0",
		"--max-time", "30",
		"--silent", "--show-error", "--fail",
		"--output", "/dev/null",
	}
	for _, ip := range pinIPs {
		cargs = append(cargs, "--resolve", fmt.Sprintf("%s:%s:%s", host, port, ip))
	}
	cargs = append(cargs, rawURL)

	c := exec.CommandContext(ctx, "curl", cargs...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("curl %s failed: %w", rawURL, err)
	}
	return nil
}
