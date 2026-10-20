// security.crowdsec.lists.apply / .status — the admin's extra IP
// blocklists (internal/blocklists), on top of CrowdSec's community list.
//
// apply takes the lists that should be on and returns at once; a background
// run then does the work, so a slow download never holds the panel's
// reconciler. For each list in the catalog:
//
//   - off: its decisions are deleted, so turning a list off lifts its bans
//     at once instead of when they expire.
//   - on: it is downloaded from the catalog URL (never a URL the panel
//     sends), each line is checked to be one IP or CIDR, and an entry is
//     dropped when it is a private or reserved range, wider than /8 (IPv4)
//     or /32 (IPv6), or overlaps an address that must stay reachable: the
//     server's own addresses, the ones the panel sends, and every CrowdSec
//     allowlist entry. The list's old decisions are then replaced by the
//     new ones. A list that fails to download or does not look like an IP
//     list keeps its old decisions until they expire.
//
// FireHOL level 1 carries 0.0.0.0/8, 10.0.0.0/8, 127.0.0.0/8, 172.16.0.0/12,
// 192.168.0.0/16 and 224.0.0.0/3 (fullbogons); without the filter, those
// would ban loopback, docker and LAN traffic on every port.
//
// Decisions last listDecisionDuration, twice the panel's daily refresh, so
// a host whose panel is down for a day keeps its lists. A refresh replaces
// a list's decisions, so an address the list dropped is unbanned then.
package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/blocklists"
)

const (
	listFetchTimeout     = 90 * time.Second
	listMaxBytes         = 8 << 20
	listMaxEntries       = 50000
	listsMaxTotal        = 80000 // the Blocklists tab counts at most 100000 per origin
	listDecisionDuration = "48h"
	listsRunTimeout      = 20 * time.Minute
)

// listReserved are the ranges no list may ban: private, loopback,
// link-local, shared, documentation, multicast and reserved space.
var listReserved = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "100::/64",
	"2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

// Seams for tests.
var (
	listFetch = func(ctx context.Context, url string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, listFetchTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "jabali-agent (blocklist refresh)")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, listMaxBytes+1))
		if err != nil {
			return nil, err
		}
		if len(body) > listMaxBytes {
			return nil, fmt.Errorf("larger than %d MiB", listMaxBytes>>20)
		}
		return body, nil
	}
	listInterfaceAddrs = net.InterfaceAddrs
)

type listsApplyParams struct {
	Enabled []string `json:"enabled"`
	Protect []string `json:"protect"`
}

type listsApplyResponse struct {
	Queued bool `json:"queued"`
}

// listResult is what the last run did with one list.
type listResult struct {
	Key              string `json:"key"`
	Enabled          bool   `json:"enabled"`
	At               string `json:"at"`
	Fetched          int    `json:"fetched"`
	Imported         int    `json:"imported"`
	DroppedReserved  int    `json:"dropped_reserved"`
	DroppedProtected int    `json:"dropped_protected"`
	DroppedDuplicate int    `json:"dropped_duplicate"`
	DroppedOverCap   int    `json:"dropped_over_cap"`
	Invalid          int    `json:"invalid"`
	Error            string `json:"error,omitempty"`
}

type listsStatusResponse struct {
	Running bool         `json:"running"`
	LastRun string       `json:"last_run,omitempty"`
	Lists   []listResult `json:"lists"`
}

var (
	listsMu      sync.Mutex
	listsWant    *listsApplyParams
	listsRunning bool
	listsLast    = listsStatusResponse{Lists: []listResult{}}
	// listsDone is signalled when a background run ends (tests wait on it).
	listsDone = make(chan struct{}, 1)
)

func listsApplyHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p listsApplyParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, csInvalidArg("failed to parse params: " + err.Error())
		}
	}
	enabled, err := blocklists.Normalize(p.Enabled)
	if err != nil {
		return nil, csInvalidArg(err.Error())
	}
	if _, err := parseProtected(p.Protect); err != nil {
		return nil, csInvalidArg(err.Error())
	}
	want := &listsApplyParams{Enabled: enabled, Protect: p.Protect}

	listsMu.Lock()
	listsWant = want
	start := !listsRunning
	listsRunning = true
	listsMu.Unlock()
	if start {
		go listsWorker()
	}
	return listsApplyResponse{Queued: true}, nil
}

// listsWorker runs until no newer request is waiting, so a list turned off
// while a run is downloading is acted on right after it.
func listsWorker() {
	for {
		listsMu.Lock()
		p := listsWant
		listsWant = nil
		if p == nil {
			listsRunning = false
			listsMu.Unlock()
			select {
			case listsDone <- struct{}{}:
			default:
			}
			return
		}
		listsMu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), listsRunTimeout)
		results := runLists(ctx, *p)
		cancel()

		listsMu.Lock()
		listsLast = listsStatusResponse{LastRun: time.Now().UTC().Format(time.RFC3339), Lists: results}
		listsMu.Unlock()
	}
}

func listsStatusHandler(context.Context, json.RawMessage) (any, error) {
	listsMu.Lock()
	defer listsMu.Unlock()
	out := listsLast
	out.Running = listsRunning
	out.Lists = append([]listResult{}, listsLast.Lists...)
	return out, nil
}

// runLists does one pass over the catalog and reports each list.
func runLists(ctx context.Context, p listsApplyParams) []listResult {
	on := map[string]bool{}
	for _, k := range p.Enabled {
		on[k] = true
	}
	protect := listProtected(ctx, p.Protect)
	seen := map[netip.Prefix]bool{}
	total := 0
	now := time.Now().UTC().Format(time.RFC3339)
	results := make([]listResult, 0, len(blocklists.Catalog))
	for _, l := range blocklists.Catalog {
		res := listResult{Key: l.Key, Enabled: on[l.Key], At: now}
		if !on[l.Key] {
			if err := deleteListDecisions(ctx, l.Key); err != nil {
				res.Error = err.Error()
			}
			results = append(results, res)
			continue
		}
		body, err := listFetch(ctx, l.URL)
		if err != nil {
			res.Error = "download failed: " + err.Error()
			results = append(results, res)
			continue
		}
		entries, invalid, err := parseIPList(body)
		res.Fetched, res.Invalid = len(entries), invalid
		if err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		keep := make([]netip.Prefix, 0, len(entries))
		for _, e := range entries {
			switch {
			case listIsReserved(e):
				res.DroppedReserved++
			case overlapsAny(e, protect):
				res.DroppedProtected++
			case seen[e]:
				res.DroppedDuplicate++
			case len(keep) >= listMaxEntries || total+len(keep) >= listsMaxTotal:
				res.DroppedOverCap++
			default:
				seen[e] = true
				keep = append(keep, e)
			}
		}
		if err := deleteListDecisions(ctx, l.Key); err != nil {
			res.Error = err.Error()
			results = append(results, res)
			continue
		}
		if len(keep) > 0 {
			if err := importListDecisions(ctx, l.Key, keep); err != nil {
				res.Error = err.Error()
				results = append(results, res)
				continue
			}
		}
		res.Imported = len(keep)
		total += len(keep)
		results = append(results, res)
	}
	return results
}

// parseIPList reads one IP or CIDR per line; blank lines and # comments are
// skipped. A file where more than 1% of the lines (plus 10) are something
// else is not an IP list (an error page, a changed format) and is refused
// whole.
func parseIPList(body []byte) ([]netip.Prefix, int, error) {
	var out []netip.Prefix
	invalid := 0
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 4096), 4096)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pfx, ok := parseIPOrPrefix(line)
		if !ok {
			invalid++
			continue
		}
		out = append(out, pfx)
	}
	if err := sc.Err(); err != nil {
		return nil, invalid, fmt.Errorf("read list: %v", err)
	}
	if len(out) == 0 {
		return nil, invalid, fmt.Errorf("no addresses in the list")
	}
	if invalid > len(out)/100+10 {
		return nil, invalid, fmt.Errorf("%d lines are not an IP or CIDR; not an IP list", invalid)
	}
	return out, invalid, nil
}

func parseIPOrPrefix(s string) (netip.Prefix, bool) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		return p.Masked(), true
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, false
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), true
}

// listIsReserved is true for an entry that is, or overlaps, reserved space,
// or that is wider than /8 (IPv4) or /32 (IPv6).
func listIsReserved(p netip.Prefix) bool {
	if p.Addr().Is4() && p.Bits() < 8 || p.Addr().Is6() && p.Bits() < 32 {
		return true
	}
	return overlapsAny(p, listReserved)
}

func overlapsAny(p netip.Prefix, set []netip.Prefix) bool {
	for _, q := range set {
		if p.Overlaps(q) {
			return true
		}
	}
	return false
}

// parseProtected checks the panel's protected addresses (IP or CIDR each).
func parseProtected(in []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, ok := parseIPOrPrefix(s)
		if !ok {
			return nil, fmt.Errorf("protect: %q is not an IP or CIDR", s)
		}
		out = append(out, p)
	}
	return out, nil
}

// listProtected is every address a list must not ban: the panel's, the
// server's own interface addresses, and the CrowdSec allowlists' entries.
// A source that cannot be read is skipped; the reserved-range filter still
// covers loopback and private space.
func listProtected(ctx context.Context, fromPanel []string) []netip.Prefix {
	out, _ := parseProtected(fromPanel)
	if addrs, err := listInterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip, ok := netip.AddrFromSlice(ipn.IP); ok {
					ip = ip.Unmap()
					out = append(out, netip.PrefixFrom(ip, ip.BitLen()))
				}
			}
		}
	}
	if raw, err := runCscliJSON(ctx, "allowlists", "list"); err == nil {
		var lists []struct {
			Items []struct {
				Value string `json:"value"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &lists) == nil {
			for _, l := range lists {
				for _, it := range l.Items {
					if p, ok := parseIPOrPrefix(strings.TrimSpace(it.Value)); ok {
						out = append(out, p)
					}
				}
			}
		}
	}
	return out
}

// deleteListDecisions removes every decision list key imported.
func deleteListDecisions(ctx context.Context, key string) error {
	cmd := execCommandContext(ctx, "cscli", "decisions", "delete",
		"--origin", "cscli-import", "--scenario", blocklists.Reason(key))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove the list's bans: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

type listDecision struct {
	Duration string `json:"duration"`
	Reason   string `json:"reason"`
	Scope    string `json:"scope"`
	Type     string `json:"type"`
	Value    string `json:"value"`
}

// importListDecisions bans entries as list key's decisions.
func importListDecisions(ctx context.Context, key string, entries []netip.Prefix) error {
	decisions := make([]listDecision, 0, len(entries))
	for _, e := range entries {
		d := listDecision{Duration: listDecisionDuration, Reason: blocklists.Reason(key), Type: "ban"}
		if e.IsSingleIP() {
			d.Scope, d.Value = "ip", e.Addr().String()
		} else {
			d.Scope, d.Value = "range", e.String()
		}
		decisions = append(decisions, d)
	}
	data, err := json.Marshal(decisions)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "jabali-list-")
	if err != nil {
		return fmt.Errorf("import: %v", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, key+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("import: %v", err)
	}
	cmd := execCommandContext(ctx, "cscli", "decisions", "import", "-i", path, "--format", "json")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("import the list: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func init() {
	Default.Register("security.crowdsec.lists.apply", listsApplyHandler)
	Default.Register("security.crowdsec.lists.status", listsStatusHandler)
}
