// security.crowdsec.lists.apply / .status — the admin's extra IP
// blocklists (internal/blocklists), on top of CrowdSec's community list.
//
// apply takes the lists that should be on and returns at once; a background
// run then does the work, so a slow download never holds the panel's
// reconciler. For each list in the catalog:
//
//   - off: it is left out of the firewall, so turning a list off lifts its
//     bans at once instead of when they expire.
//   - on: it is downloaded from the catalog URL, each line is checked to be
//     one IP or CIDR, and an entry is
//     dropped when it is a private or reserved range, wider than /8 (IPv4)
//     or /29 (IPv6), or overlaps an address that must stay reachable: the
//     server's own addresses, the ones the panel sends, every CrowdSec
//     allowlist entry, and the addresses Google's and Bing's crawlers use
//     (listCrawlerURLs). What is left replaces the list's bans. A list that
//     fails to download or does not look like an IP list keeps its last bans
//     until they expire.
//
// The bans go into the agent's own nftables table, not CrowdSec decisions:
// see security_crowdsec_lists_nft.go. An older agent imported them as
// decisions (reason "jabali-list:<key>"). Each run deletes those of a list
// that is off. Those of a list that is on are left to expire, as the table
// bans the same addresses: deleting them all at once on the update would
// stall the firewall bouncer for minutes.
//
// The admin's custom lists (blocklists.NormalizeCustom URLs) go through the
// same steps after the catalog. A custom list is named by its URL
// (blocklists.CustomKey); listsCustomState keeps the custom keys an older
// agent imported, so a URL the admin removes has those decisions deleted.
//
// Every download, catalog or custom, goes through listHTTPClient: https only,
// no proxy, at most 5 redirects, each https, and a host that resolves to a
// private, loopback, link-local or reserved address, or one of this server's,
// is refused when the connection is made (listDial), so a redirect or a
// changed DNS answer cannot point it inside.
//
// FireHOL level 1 carries 0.0.0.0/8, 10.0.0.0/8, 127.0.0.0/8, 172.16.0.0/12,
// 192.168.0.0/16 and 224.0.0.0/3 (fullbogons); without the filter, those
// would ban loopback, docker and LAN traffic on every port.
//
// Bans last listLifetime from their list's download, twice the panel's daily
// refresh, so a host whose panel is down for a day keeps its lists. A refresh
// replaces a list's bans, so an address the list dropped is unbanned then.
package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/blocklists"
)

const (
	listFetchTimeout = 90 * time.Second
	listMaxBytes     = 8 << 20
	listMaxEntries   = 50000
	listsMaxTotal    = 80000
	listsRunTimeout  = 20 * time.Minute
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

// listHTTPClient downloads every list. See the package comment.
var listHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           listDial,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ForceAttemptHTTP2:     true,
	},
	CheckRedirect: listCheckRedirect,
}

// listDenyKey carries a run's protected addresses (the panel's public IPs,
// which behind NAT are on no interface, and the allowlists) to listDial.
type listDenyKey struct{}

// listDial resolves the host itself and connects only when every address it
// resolves to is one a list may come from, to one of those addresses, so a
// second lookup cannot answer differently.
func listDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	own := listOwnAddrs()
	if deny, ok := ctx.Value(listDenyKey{}).([]netip.Prefix); ok {
		own = append(own, deny...)
	}
	for _, ip := range ips {
		if !listFetchAllowed(ip, own) {
			return nil, fmt.Errorf("%s resolves to %s, which a list may not be downloaded from", host, ip.Unmap())
		}
	}
	d := &net.Dialer{Timeout: 15 * time.Second}
	var lastErr error
	for _, ip := range ips {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("%s has no address", host)
	}
	return nil, lastErr
}

// listFetchAllowed is true for a public address that is not this server's.
func listFetchAllowed(ip netip.Addr, own []netip.Prefix) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	p := netip.PrefixFrom(ip, ip.BitLen())
	return !overlapsAny(p, listReserved) && !overlapsAny(p, own)
}

// listOwnAddrs is this server's interface addresses.
func listOwnAddrs() []netip.Prefix {
	var out []netip.Prefix
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
	return out
}

// listCheckRedirect follows at most 5 redirects, each to an https URL; the
// dial check above applies to each.
func listCheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" {
		return fmt.Errorf("redirect to a URL that is not https")
	}
	return nil
}

// listsCustomState holds the custom list keys an older agent may have left
// CrowdSec decisions for.
var listsCustomState = "/var/lib/jabali/crowdsec-custom-lists.json"

// listCrawlerURLs are the files where Google and Bing publish the addresses
// their crawlers and fetchers come from. No list bans those addresses, so
// search engines always reach the sites: the attack reports behind FireHOL
// level 2 and 3 carry some Googlebot and Bingbot addresses on most days.
var listCrawlerURLs = []string{
	"https://developers.google.com/static/crawling/ipranges/common-crawlers.json",
	"https://developers.google.com/static/crawling/ipranges/special-crawlers.json",
	"https://developers.google.com/static/crawling/ipranges/user-triggered-fetchers.json",
	"https://developers.google.com/static/crawling/ipranges/user-triggered-fetchers-google.json",
	"https://www.bing.com/toolbox/bingbot.json",
}

// listsCrawlerCache keeps the last good copy of each listCrawlerURLs file,
// for a run in which one cannot be downloaded.
var listsCrawlerCache = "/var/lib/jabali/crowdsec-crawler-ranges.json"

// Seams for tests.
var (
	listFetch = func(ctx context.Context, url string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, listFetchTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if req.URL.Scheme != "https" {
			return nil, fmt.Errorf("not an https URL")
		}
		req.Header.Set("User-Agent", "jabali-agent (blocklist refresh)")
		resp, err := listHTTPClient.Do(req)
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
	Custom  []string `json:"custom"`
	Protect []string `json:"protect"`
}

type listsApplyResponse struct {
	Queued bool `json:"queued"`
}

// listResult is what the last run did with one list.
type listResult struct {
	Key              string `json:"key"`
	URL              string `json:"url,omitempty"` // custom lists only
	Enabled          bool   `json:"enabled"`
	At               string `json:"at"`
	Fetched          int    `json:"fetched"`
	Imported         int    `json:"imported"`
	DroppedReserved  int    `json:"dropped_reserved"`
	DroppedProtected int    `json:"dropped_protected"`
	DroppedCrawler   int    `json:"dropped_crawler"`
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
	custom, err := blocklists.NormalizeCustom(p.Custom)
	if err != nil {
		return nil, csInvalidArg(err.Error())
	}
	if _, err := parseProtected(p.Protect); err != nil {
		return nil, csInvalidArg(err.Error())
	}
	want := &listsApplyParams{Enabled: enabled, Custom: custom, Protect: p.Protect}

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

// runLists does one pass over the catalog and the custom lists, puts what the
// lists that are on ban into the firewall, and reports each list.
func runLists(ctx context.Context, p listsApplyParams) []listResult {
	on := map[string]bool{}
	for _, k := range p.Enabled {
		on[k] = true
	}
	// A failure to read the allowlists is reported by the render below,
	// which then leaves the firewall as it is.
	protect, _ := listProtected(ctx, p.Protect)
	ctx = context.WithValue(ctx, listDenyKey{}, protect)
	// Without the crawler addresses no list that is on is refreshed: it
	// keeps its last bans until they expire, rather than new ones that may
	// hold a search engine's address.
	var crawlers []netip.Prefix
	var crawlerErr error
	if len(p.Enabled) > 0 || len(p.Custom) > 0 {
		crawlers, crawlerErr = listCrawlerRanges(ctx)
	}
	prev := readListsState()
	st := listsState{Protect: p.Protect, Lists: map[string]listEntries{}}
	now := time.Now().UTC()
	// refresh downloads a list that is on. One that could not be refreshed
	// keeps the bans it had.
	refresh := func(res listResult, url string, seen map[netip.Prefix]bool, total *int) listResult {
		var keep []netip.Prefix
		if crawlerErr != nil {
			res.Error = "not refreshed, its bans stay until they expire: Google's and Bing's crawler addresses could not be loaded (" + crawlerErr.Error() + ")"
		} else {
			res, keep = runOneList(ctx, res, url, protect, crawlers, seen, total)
		}
		if res.Error != "" {
			if old, ok := prev.Lists[res.Key]; ok {
				st.Lists[res.Key] = old
			}
			return res
		}
		entries := make([]string, 0, len(keep))
		for _, e := range keep {
			entries = append(entries, e.String())
		}
		st.Lists[res.Key] = listEntries{At: now, Entries: entries}
		return res
	}
	seen := map[netip.Prefix]bool{}
	total := 0
	at := now.Format(time.RFC3339)
	results := make([]listResult, 0, len(blocklists.Catalog)+len(p.Custom))
	for _, l := range blocklists.Catalog {
		res := listResult{Key: l.Key, Enabled: on[l.Key], At: at}
		if on[l.Key] {
			res = refresh(res, l.URL, seen, &total)
		} else if err := deleteListDecisions(ctx, l.Key); err != nil {
			res.Error = err.Error()
		}
		results = append(results, res)
	}

	// Custom lists. An old key whose URL is still on, or whose decisions
	// could not be deleted, stays in listsCustomState for a later run.
	wanted := map[string]bool{}
	for _, u := range p.Custom {
		wanted[blocklists.CustomKey(u)] = true
	}
	keepKeys := []string{}
	for _, k := range readCustomListKeys() {
		if wanted[k] || deleteListDecisions(ctx, k) != nil {
			keepKeys = append(keepKeys, k)
		}
	}
	writeCustomListKeys(keepKeys)
	for _, u := range p.Custom {
		res := listResult{Key: blocklists.CustomKey(u), URL: u, Enabled: true, At: at}
		results = append(results, refresh(res, u, seen, &total))
	}

	err := writeListsState(st)
	if err == nil {
		err = renderLists(ctx)
	}
	if err != nil {
		for i := range results {
			if results[i].Enabled && results[i].Error == "" {
				results[i].Error = "the firewall was not updated: " + err.Error()
			}
		}
	}
	return results
}

// runOneList downloads one list that is on and filters it, and returns what
// it bans. res.Error says why when it could not.
func runOneList(ctx context.Context, res listResult, url string, protect, crawlers []netip.Prefix, seen map[netip.Prefix]bool, total *int) (listResult, []netip.Prefix) {
	body, err := listFetch(ctx, url)
	if err != nil {
		res.Error = "download failed: " + err.Error()
		return res, nil
	}
	entries, invalid, err := parseIPList(body)
	res.Fetched, res.Invalid = len(entries), invalid
	if err != nil {
		res.Error = err.Error()
		return res, nil
	}
	keep := make([]netip.Prefix, 0, len(entries))
	for _, e := range entries {
		switch {
		case listIsReserved(e):
			res.DroppedReserved++
		case overlapsAny(e, protect):
			res.DroppedProtected++
		case overlapsAny(e, crawlers):
			res.DroppedCrawler++
		case seen[e]:
			res.DroppedDuplicate++
		case len(keep) >= listMaxEntries || *total+len(keep) >= listsMaxTotal:
			res.DroppedOverCap++
		default:
			seen[e] = true
			keep = append(keep, e)
		}
	}
	res.Imported = len(keep)
	*total += len(keep)
	return res, keep
}

// readCustomListKeys is the custom keys the last run left bans for. A missing
// or unreadable state is none.
func readCustomListKeys() []string {
	raw, err := os.ReadFile(listsCustomState)
	if err != nil {
		return nil
	}
	var keys []string
	if json.Unmarshal(raw, &keys) != nil {
		return nil
	}
	out := keys[:0]
	for _, k := range keys {
		if blocklists.IsCustomKey(k) {
			out = append(out, k)
		}
	}
	return out
}

// writeCustomListKeys replaces the state. Best effort: a failed write only
// means a later removal is not lifted until its bans expire (48 h).
func writeCustomListKeys(keys []string) {
	raw, err := json.Marshal(keys)
	if err != nil {
		return
	}
	tmp := listsCustomState + ".new"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, listsCustomState)
}

// listCrawlerRanges is every address in the listCrawlerURLs files. A file
// that cannot be downloaded or read is taken from its last good copy; with
// no copy either, it is an error.
func listCrawlerRanges(ctx context.Context) ([]netip.Prefix, error) {
	cache := readCrawlerCache()
	changed := false
	var out []netip.Prefix
	for _, u := range listCrawlerURLs {
		got, err := fetchCrawlerRanges(ctx, u)
		if err == nil {
			ss := make([]string, len(got))
			for i, p := range got {
				ss[i] = p.String()
			}
			cache[u] = ss
			changed = true
			out = append(out, got...)
			continue
		}
		cached, ok := cache[u]
		if !ok {
			return nil, fmt.Errorf("%s: %v", u, err)
		}
		for _, s := range cached {
			if p, perr := netip.ParsePrefix(s); perr == nil {
				out = append(out, p)
			}
		}
	}
	if changed {
		writeCrawlerCache(cache)
	}
	return out, nil
}

func fetchCrawlerRanges(ctx context.Context, url string) ([]netip.Prefix, error) {
	body, err := listFetch(ctx, url)
	if err != nil {
		return nil, err
	}
	return parseCrawlerRanges(body)
}

// parseCrawlerRanges reads Google's and Bing's format:
// {"prefixes":[{"ipv4Prefix":"66.249.64.0/27"},{"ipv6Prefix":"2001:4860:4801:10::/64"}]}.
// A file with a bad prefix or none at all is refused whole, so a broken
// download never replaces a good copy.
func parseCrawlerRanges(body []byte) ([]netip.Prefix, error) {
	var f struct {
		Prefixes []struct {
			IPv4 string `json:"ipv4Prefix"`
			IPv6 string `json:"ipv6Prefix"`
		} `json:"prefixes"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, errors.New("not a crawler address file")
	}
	out := make([]netip.Prefix, 0, len(f.Prefixes))
	for _, e := range f.Prefixes {
		s := e.IPv4
		if s == "" {
			s = e.IPv6
		}
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("not a crawler address file: bad prefix %q", s)
		}
		out = append(out, p.Masked())
	}
	if len(out) == 0 {
		return nil, errors.New("not a crawler address file: no prefixes")
	}
	return out, nil
}

// readCrawlerCache is the last good copy of each crawler file, by URL. A
// missing or unreadable cache is empty.
func readCrawlerCache() map[string][]string {
	out := map[string][]string{}
	raw, err := os.ReadFile(listsCrawlerCache)
	if err != nil {
		return out
	}
	if json.Unmarshal(raw, &out) != nil {
		return map[string][]string{}
	}
	return out
}

// writeCrawlerCache replaces the cache. Best effort: a failed write only
// means a later run without the network has no copy to fall back on.
func writeCrawlerCache(cache map[string][]string) {
	raw, err := json.Marshal(cache)
	if err != nil {
		return
	}
	tmp := listsCrawlerCache + ".new"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, listsCrawlerCache)
}

// parseIPList reads one IP or CIDR per line, from the line's first field, so
// a trailing note ("1.10.16.0/20 ; SBL256894", Spamhaus DROP) is skipped;
// blank lines and # or ; comments are skipped too. A file where more than 1%
// of the lines (plus 10) are something else is not an IP list (an error
// page, a changed format) and is refused whole.
func parseIPList(body []byte) ([]netip.Prefix, int, error) {
	var out []netip.Prefix
	invalid := 0
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 4096), 4096)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		field := strings.TrimRight(strings.Fields(line)[0], ";,")
		pfx, ok := parseIPOrPrefix(field)
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
// or that is wider than /8 (IPv4) or /29 (IPv6). /29 is a whole provider's
// IPv6 allocation (RIPE's default), the size of the widest networks on
// Spamhaus's IPv6 DROP list.
func listIsReserved(p netip.Prefix) bool {
	if p.Addr().Is4() && p.Bits() < 8 || p.Addr().Is6() && p.Bits() < 29 {
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
func listProtected(ctx context.Context, fromPanel []string) ([]netip.Prefix, error) {
	out, _ := parseProtected(fromPanel)
	out = append(out, listOwnAddrs()...)
	raw, err := runCscliJSON(ctx, "allowlists", "list")
	if err != nil {
		return out, fmt.Errorf("read the CrowdSec allowlists: %v", err)
	}
	var lists []struct {
		Items []struct {
			Value string `json:"value"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &lists); err != nil {
		return out, fmt.Errorf("read the CrowdSec allowlists: %v", err)
	}
	for _, l := range lists {
		for _, it := range l.Items {
			if p, ok := parseIPOrPrefix(strings.TrimSpace(it.Value)); ok {
				out = append(out, p)
			}
		}
	}
	return out, nil
}

// deleteListDecisions removes every decision an older agent imported for list
// key.
func deleteListDecisions(ctx context.Context, key string) error {
	cmd := execCommandContext(ctx, "cscli", "decisions", "delete",
		"--origin", "cscli-import", "--scenario", blocklists.Reason(key))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove the list's bans: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func init() {
	Default.Register("security.crowdsec.lists.apply", listsApplyHandler)
	Default.Register("security.crowdsec.lists.status", listsStatusHandler)
}
