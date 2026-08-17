package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// migration.http_probe — post-migration health check. For each migrated
// domain, fetch the homepage THROUGH THE LOCAL nginx (curl --resolve pins
// the name to 127.0.0.1) so the real vhost + the user's PHP-FPM pool run.
// A migrated WP site that survived on the source via a persistent object
// cache can fatal on jabali (no object cache) over malformed option data —
// this surfaces it (a 500) at migration time instead of from the customer.
//
// Waits (up to ~90s, re-resolving the listen IP each round) for connection-
// refused / 502 / 503 because the vhost, its managed IP, and the per-user FPM
// pool converge on the reconciler's next tick after a restore; it records the
// site's first REAL HTTP response so a genuine app crash (5xx) is caught rather
// than lost behind an early "not up yet" 0. Domains are probed in parallel.

type migrationHTTPProbeParams struct {
	Domains []string `json:"domains"`
}

type httpProbeResult struct {
	Domain string `json:"domain"`
	Status int    `json:"status"`
	OK     bool   `json:"ok"`
	Note   string `json:"note,omitempty"`
}

type migrationHTTPProbeResponse struct {
	Results []httpProbeResult `json:"results"`
}

// probeDomainRe bounds what reaches curl's --resolve / URL args.
var probeDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}\.)+[a-z]{2,63}$`)

// probeTargetIP is the IP curl --resolve pins the migrated domain to so the
// request hits THIS box's nginx (the domain's real DNS may still point at
// the source server mid-migration). It is the box's primary IPv4 — nginx
// vhosts bind a managed IP (M24), not 127.0.0.1, so resolving to loopback
// gets connection-refused. Computed once.
var probeTargetIP = func() string {
	out, err := execCommand("ip", "-4", "route", "get", "1.1.1.1").Output()
	if err == nil {
		// "1.1.1.1 via X dev Y src <IP> ..." — take the token after "src".
		fields := strings.Fields(string(out))
		for i, f := range fields {
			if f == "src" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	// JAB-31 follow-up: a box with no default route to 1.1.1.1 (isolated test
	// VM) previously fell through to 127.0.0.1, which every managed-IP vhost
	// (M24 `listen <IP>:443`) refuses → false connection-refused. Fall back to
	// the first non-loopback global IPv4 instead.
	if hi, herr := execCommand("hostname", "-I").Output(); herr == nil {
		for _, tok := range strings.Fields(string(hi)) {
			if strings.Contains(tok, ".") && !strings.HasPrefix(tok, "127.") {
				return tok
			}
		}
	}
	return "127.0.0.1"
}()

// nginxSitesDir is a var so tests can point vhostListenIP at a fixture dir.
var nginxSitesDir = "/etc/nginx/sites-available"

// vhostListenRe extracts the IPv4 from a rendered `listen <IP>:443` directive.
var vhostListenRe = regexp.MustCompile(`(?m)^\s*listen\s+(\d{1,3}(?:\.\d{1,3}){3}):443\b`)

// vhostListenIP returns the specific IPv4 a domain's rendered nginx vhost binds
// (M24 per-domain listen IP), or "" when it listens on all addresses. The probe
// must curl --resolve to THIS ip: a vhost bound to a managed IP refuses a
// connection to any other address, which produced false 0-status health
// failures inside a `done` migration (JAB-31 follow-up).
func vhostListenIP(domain string) string {
	b, err := os.ReadFile(filepath.Join(nginxSitesDir, domain+".conf"))
	if err != nil {
		return ""
	}
	if m := vhostListenRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

var runProbeCurl = func(ctx context.Context, domain, ip string) int {
	args := []string{
		"-s", "-o", "/dev/null", "-w", "%{http_code}",
		"-k", "-L", "--max-time", "15",
		"--resolve", domain + ":443:" + ip,
		"--resolve", domain + ":80:" + ip,
		"https://" + domain + "/",
	}
	out, _ := execCommandContext(ctx, "curl", args...).Output()
	code, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return code // 0 on connection failure / timeout
}

var probeSleep = func() { time.Sleep(5 * time.Second) }

func probeOneDomain(ctx context.Context, domain string) httpProbeResult {
	// JAB-42 (follow-up): right after a restore the domain's nginx vhost, its
	// managed listen IP (M24), and the per-user FPM pool all converge on the
	// reconciler's NEXT tick — which is often past a short window. A probe that
	// gives up early gets 0/connection-refused on every domain and can't tell
	// "not up yet" from "crashed", so it never catches a real 5xx. Wait up to
	// ~90s for the site to return ANY HTTP response, RE-RESOLVING the vhost listen
	// IP each round (the .conf file materialises mid-wait), then record that real
	// state: a genuine 5xx is caught (degrade), while a site that truly never
	// answers stays a 0 warning (not a hard degrade — see migrate_run_cmd.go).
	const probeAttempts = 18 // ~90s of 5s backoff
	var code int
	for attempt := 0; attempt < probeAttempts; attempt++ {
		// Resolve to the domain's actual vhost listen IP; fall back to the box's
		// primary IPv4 when the vhost listens on all addresses (JAB-31 follow-up).
		ip := vhostListenIP(domain)
		if ip == "" {
			ip = probeTargetIP
		}
		code = runProbeCurl(ctx, domain, ip)
		// Any real HTTP response (2xx/3xx/4xx/5xx) is the site's true state — stop.
		// 0 = not reachable yet (vhost/IP not bound); 502/503 = FPM warming up.
		if code != 0 && code != 502 && code != 503 {
			break
		}
		if attempt < probeAttempts-1 {
			probeSleep()
		}
	}
	res := httpProbeResult{Domain: domain, Status: code, OK: code >= 200 && code < 400}
	switch {
	case code == 0:
		res.Note = "no response (connection refused / timeout)"
	case code >= 500:
		res.Note = "server error — likely a crashing app (e.g. WP fatal on imported data)"
	case code >= 400:
		res.Note = "client error"
	}
	return res
}

func migrationHTTPProbeHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p migrationHTTPProbeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	// Probe domains in parallel — each waits up to ~90s for convergence, so a
	// sequential loop would multiply the wall-clock past the caller's timeout
	// for a multi-domain account (JAB-42 follow-up).
	results := make([]httpProbeResult, len(p.Domains))
	var wg sync.WaitGroup
	for i, raw := range p.Domains {
		d := strings.TrimSpace(strings.ToLower(raw))
		if !probeDomainRe.MatchString(d) {
			results[i] = httpProbeResult{Domain: d, Status: 0, OK: false, Note: "skipped: invalid domain"}
			continue
		}
		wg.Add(1)
		go func(i int, d string) {
			defer wg.Done()
			results[i] = probeOneDomain(ctx, d)
		}(i, d)
	}
	wg.Wait()
	return migrationHTTPProbeResponse{Results: results}, nil
}

func init() {
	Default.Register("migration.http_probe", migrationHTTPProbeHandler)
}
