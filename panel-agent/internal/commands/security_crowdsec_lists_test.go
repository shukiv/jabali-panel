package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/blocklists"
)

// listsFake stands in for cscli, nft and the network. deletes lists each
// decision reason cscli was asked to delete; scripts holds each nft script
// applied.
type listsFake struct {
	mu      sync.Mutex
	bodies  map[string]string // URL -> body; missing = download error
	fetched []string
	deletes []string
	scripts []string
	// allowlists is what cscli allowlists list prints; allowlistsErr makes
	// it fail. nftErr makes nft -f fail.
	allowlists    string
	allowlistsErr bool
	nftErr        error
	// noCrawlerDefaults leaves the crawler files out of bodies, so their
	// downloads fail.
	noCrawlerDefaults bool
}

func installListsFake(t *testing.T, f *listsFake, allowlists string) {
	t.Helper()
	f.allowlists = allowlists
	prevFetch, prevAddrs, prevExec, prevState, prevCrawlers := listFetch, listInterfaceAddrs, execCommandContext, listsCustomState, listsCrawlerCache
	prevEntries, prevNftPath, prevApply := listsEntriesState, listsNftPath, listNftApply
	t.Cleanup(func() {
		listFetch, listInterfaceAddrs, execCommandContext, listsCustomState, listsCrawlerCache = prevFetch, prevAddrs, prevExec, prevState, prevCrawlers
		listsEntriesState, listsNftPath, listNftApply = prevEntries, prevNftPath, prevApply
	})
	dir := t.TempDir()
	listsCustomState = filepath.Join(dir, "crowdsec-custom-lists.json")
	listsCrawlerCache = filepath.Join(dir, "crowdsec-crawler-ranges.json")
	listsEntriesState = filepath.Join(dir, "crowdsec-lists.json")
	listsNftPath = filepath.Join(dir, "crowdsec-lists.nft")
	listNftApply = func(_ context.Context, path string) error {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("nft script: %v", err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.scripts = append(f.scripts, string(raw))
		return f.nftErr
	}
	// Each crawler file holds one documentation range unless the test says
	// otherwise; a test drops a URL from bodies to make it fail.
	for _, u := range listCrawlerURLs {
		if _, ok := f.bodies[u]; !ok && !f.noCrawlerDefaults {
			f.bodies[u] = `{"prefixes":[{"ipv4Prefix":"198.51.100.0/24"}]}`
		}
	}
	listFetch = func(_ context.Context, url string) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.fetched = append(f.fetched, url)
		b, ok := f.bodies[url]
		if !ok {
			return nil, errors.New("connection refused")
		}
		return []byte(b), nil
	}
	listInterfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("46.4.1.10"), Mask: net.CIDRMask(26, 32)},
		}, nil
	}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		joined := strings.Join(args, " ")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasPrefix(joined, "allowlists list"):
			if f.allowlistsErr {
				return exec.CommandContext(ctx, "false")
			}
			return exec.CommandContext(ctx, "printf", "%s", f.allowlists)
		case strings.HasPrefix(joined, "allowlists inspect"), strings.HasPrefix(joined, "allowlists add"),
			strings.HasPrefix(joined, "allowlists remove"):
			return exec.CommandContext(ctx, "true")
		case strings.HasPrefix(joined, "decisions delete"):
			f.deletes = append(f.deletes, args[len(args)-1])
			return exec.CommandContext(ctx, "true")
		}
		t.Errorf("unexpected command %s %s", name, joined)
		return exec.CommandContext(ctx, "false")
	}
}

// stored is what list key bans, as the last run kept it.
func (f *listsFake) stored(key string) []string {
	return readListsState().Lists[key].Entries
}

// banned is every element the last nft script put in the table, without its
// timeout.
func (f *listsFake) banned() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.scripts) == 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(f.scripts[len(f.scripts)-1], "\n") {
		_, els, ok := strings.Cut(line, "{ ")
		if !strings.HasPrefix(line, "add element ") || !ok {
			continue
		}
		for _, el := range strings.Split(strings.TrimSuffix(els, " }"), ", ") {
			addr, _, _ := strings.Cut(el, " timeout ")
			out = append(out, addr)
		}
	}
	return out
}

const level1URL = "https://iplists.firehol.org/files/firehol_level1.netset"
const level2URL = "https://iplists.firehol.org/files/firehol_level2.netset"

// The list's private, loopback, multicast and over-wide ranges, the server's
// own address, the panel's protected address and allowlist entries are never
// banned.
func TestRunLists_DropsReservedAndProtectedAddresses(t *testing.T) {
	f := &listsFake{bodies: map[string]string{level1URL: strings.Join([]string{
		"# firehol_level1", "",
		"0.0.0.0/8", "10.0.0.0/8", "127.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/3",
		"1.0.0.0/7",        // wider than /8
		"46.4.1.0/24",      // holds the server's interface address
		"185.10.20.30",     // the panel's public IP
		"91.198.174.200",   // inside an allowlisted range
		"5.188.10.0/24",    // kept
		"45.155.205.233",   // kept
		"::1", "fe80::/10", // IPv6 reserved
		"2a0e:1c00::/28",   // wider than /29
		"2a06:e480::/29",   // kept: a whole provider allocation, as on Spamhaus's IPv6 DROP
		"2a0e:1c80:1::/48", // kept
	}, "\n")}}
	installListsFake(t, f, `[{"name":"jabali-admin-allowlist","items":[{"value":"91.198.174.192/27"}]}]`)

	res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1"}, Protect: []string{"185.10.20.30"}})

	got := f.stored("firehol_level1")
	want := []string{"5.188.10.0/24", "45.155.205.233/32", "2a06:e480::/29", "2a0e:1c80:1::/48"}
	if !slices.Equal(got, want) {
		t.Fatalf("kept %v\nwant  %v", got, want)
	}
	if got := f.banned(); !slices.Equal(got, want) {
		t.Fatalf("firewall bans %v\nwant          %v", got, want)
	}
	r := res[0]
	if r.Key != "firehol_level1" || r.Imported != 4 || r.DroppedReserved != 10 || r.DroppedProtected != 3 || r.Error != "" {
		t.Fatalf("result %+v: want 4 imported, 10 reserved, 3 protected", r)
	}
}

// A list that is off is not downloaded and its bans leave the firewall at
// once. The decisions an older agent imported for it are deleted; those of a
// list that is on are left to expire.
func TestRunLists_ListTurnedOffLosesItsBans(t *testing.T) {
	f := &listsFake{bodies: map[string]string{level1URL: "5.188.10.0/24\n"}}
	installListsFake(t, f, `[]`)
	runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1"}})
	if slices.Contains(f.deletes, "jabali-list:firehol_level1") {
		t.Fatalf("deleted the old decisions of level 1, which is on: %v", f.deletes)
	}
	if got := f.banned(); !slices.Equal(got, []string{"5.188.10.0/24"}) {
		t.Fatalf("firewall bans %v, want level 1's", got)
	}

	f.fetched, f.deletes = nil, nil
	res := runLists(context.Background(), listsApplyParams{})
	if len(f.fetched) != 0 {
		t.Fatalf("downloaded %v with every list off", f.fetched)
	}
	var want []string
	for _, l := range blocklists.Catalog {
		want = append(want, "jabali-list:"+l.Key)
	}
	if !slices.Equal(f.deletes, want) {
		t.Fatalf("deleted %v, want %v", f.deletes, want)
	}
	if got := f.banned(); len(got) != 0 {
		t.Fatalf("firewall still bans %v", got)
	}
	if last := f.scripts[len(f.scripts)-1]; !strings.Contains(last, "delete table inet jabali_lists") {
		t.Fatalf("last script does not remove the table:\n%s", last)
	}
	for _, r := range res {
		if r.Enabled || r.Error != "" {
			t.Fatalf("result %+v: want off, no error", r)
		}
	}
}

// A list that cannot be downloaded, or is not an IP list, keeps the bans it
// has; the other lists go on.
func TestRunLists_FailedDownloadKeepsOldBans(t *testing.T) {
	f := &listsFake{bodies: map[string]string{level1URL: "5.188.10.0/24\n", level2URL: "80.94.95.81\n"}}
	installListsFake(t, f, `[]`)
	both := listsApplyParams{Enabled: []string{"firehol_level1", "firehol_level2"}}
	runLists(context.Background(), both)
	before := readListsState()

	delete(f.bodies, level1URL)
	f.bodies[level2URL] = "<html><body>\n" + strings.Repeat("<p>Service unavailable</p>\n", 20) + "1.2.3.4\n</body></html>"
	res := runLists(context.Background(), both)
	if !strings.Contains(res[0].Error, "download failed") || !strings.Contains(res[1].Error, "not an IP list") {
		t.Fatalf("results %+v / %+v: want a download error and a not-an-IP-list error", res[0], res[1])
	}
	after := readListsState()
	for _, k := range []string{"firehol_level1", "firehol_level2"} {
		if !after.Lists[k].At.Equal(before.Lists[k].At) || !slices.Equal(after.Lists[k].Entries, before.Lists[k].Entries) {
			t.Fatalf("%s: kept %+v, want the earlier download %+v", k, after.Lists[k], before.Lists[k])
		}
	}
	if got := f.banned(); !slices.Equal(got, []string{"5.188.10.0/24", "80.94.95.81/32"}) {
		t.Fatalf("firewall bans %v, want both lists' earlier bans", got)
	}
}

// An address on two lists is kept once, by the first.
func TestRunLists_AddressOnTwoListsImportedOnce(t *testing.T) {
	f := &listsFake{bodies: map[string]string{
		level1URL: "5.188.10.0/24\n45.155.205.233\n",
		level2URL: "45.155.205.233\n80.94.95.81\n",
	}}
	installListsFake(t, f, `[]`)
	res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1", "firehol_level2"}})
	if got := f.stored("firehol_level2"); !slices.Equal(got, []string{"80.94.95.81/32"}) {
		t.Fatalf("level 2 kept %v, want only 80.94.95.81", got)
	}
	if res[1].DroppedDuplicate != 1 {
		t.Fatalf("level 2 result %+v: want 1 duplicate", res[1])
	}
}

func TestParseIPList(t *testing.T) {
	got, invalid, err := parseIPList([]byte("# c\n\n1.2.3.4\n5.6.7.8/24\n2a0e::1\n::ffff:9.9.9.9\nnot-an-ip\n"))
	if err != nil {
		t.Fatal(err)
	}
	if invalid != 1 || len(got) != 4 {
		t.Fatalf("got %v, %d invalid; want 4 entries, 1 invalid", got, invalid)
	}
	if got[1].String() != "5.6.7.0/24" || got[3].String() != "9.9.9.9/32" {
		t.Fatalf("got %v: want masked ranges and unmapped IPv4", got)
	}
	if _, _, err := parseIPList([]byte("# only comments\n")); err == nil {
		t.Fatal("an empty list was accepted")
	}
}

func TestListsApply_RefusesBadInput(t *testing.T) {
	for _, raw := range []string{
		`{"enabled":["https://evil.example/list.txt"]}`,
		`{"enabled":["firehol_level1"],"protect":["1.2.3.4; rm -rf /"]}`,
		`{"enabled":`,
	} {
		if _, err := listsApplyHandler(context.Background(), json.RawMessage(raw)); err == nil {
			t.Errorf("apply(%s) accepted", raw)
		}
	}
}

// apply returns before the run; status reports the run once it is done.
func TestListsApply_RunsInBackgroundAndReports(t *testing.T) {
	f := &listsFake{bodies: map[string]string{level1URL: "45.155.205.233\n"}}
	installListsFake(t, f, `[]`)
	select {
	case <-listsDone:
	default:
	}
	out, err := listsApplyHandler(context.Background(), json.RawMessage(`{"enabled":["firehol_level1"]}`))
	if err != nil || !out.(listsApplyResponse).Queued {
		t.Fatalf("apply = %v, %v; want queued", out, err)
	}
	select {
	case <-listsDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the background run did not finish")
	}
	st, _ := listsStatusHandler(context.Background(), nil)
	s := st.(listsStatusResponse)
	if s.Running || s.LastRun == "" || len(s.Lists) != len(blocklists.Catalog) || s.Lists[0].Imported != 1 {
		t.Fatalf("status %+v: want a finished run with level 1 imported", s)
	}
}

// A custom list is fetched from its URL and banned under its own key; once
// the admin removes the URL, the next run lifts its bans.
func TestRunLists_CustomListBannedThenRemoved(t *testing.T) {
	const u = "https://lists.example.org/drop.txt"
	f := &listsFake{bodies: map[string]string{u: "203.0.114.9\n198.51.100.7\n10.0.0.0/8\n# comment\n"}}
	installListsFake(t, f, "[]")
	key := blocklists.CustomKey(u)

	res := runLists(context.Background(), listsApplyParams{Custom: []string{u}})
	last := res[len(res)-1]
	if last.Key != key || last.URL != u || last.Imported != 1 || last.DroppedReserved != 2 || last.Error != "" {
		t.Fatalf("custom result = %+v", last)
	}
	if got := f.banned(); !slices.Equal(got, []string{"203.0.114.9/32"}) {
		t.Fatalf("firewall bans %v", got)
	}

	runLists(context.Background(), listsApplyParams{})
	if got := f.banned(); len(got) != 0 {
		t.Fatalf("removing the URL left %v banned", got)
	}
	if _, ok := readListsState().Lists[key]; ok {
		t.Fatalf("state still holds %s", key)
	}
}

// The decisions an older agent imported for a custom list stay while its URL
// is on, and are deleted once the admin removes it.
func TestRunLists_OldCustomDecisionsDeletedWhenRemoved(t *testing.T) {
	const u = "https://lists.example.org/drop.txt"
	f := &listsFake{bodies: map[string]string{u: "203.0.114.9\n"}}
	installListsFake(t, f, "[]")
	key := blocklists.CustomKey(u)
	writeCustomListKeys([]string{key})

	runLists(context.Background(), listsApplyParams{Custom: []string{u}})
	if slices.Contains(f.deletes, blocklists.Reason(key)) {
		t.Fatalf("deleted the decisions of a custom list that is on: %v", f.deletes)
	}
	if keys := readCustomListKeys(); !slices.Equal(keys, []string{key}) {
		t.Fatalf("state holds %v, want %s kept for later", keys, key)
	}

	runLists(context.Background(), listsApplyParams{})
	if !slices.Contains(f.deletes, blocklists.Reason(key)) {
		t.Fatalf("removing the URL did not delete its old decisions: deletes %v", f.deletes)
	}
	if keys := readCustomListKeys(); len(keys) != 0 {
		t.Fatalf("state still holds %v", keys)
	}
}

func TestListsApply_RefusesBadCustomURL(t *testing.T) {
	for _, u := range []string{"http://lists.example.org/x", "https://127.0.0.1/x", "https://localhost/x"} {
		raw, _ := json.Marshal(listsApplyParams{Custom: []string{u}})
		if _, err := listsApplyHandler(context.Background(), raw); err == nil {
			t.Errorf("apply accepted %q", u)
		}
	}
}

// A list may be downloaded only from a public address that is not this
// server's.
func TestListFetchAllowed(t *testing.T) {
	own := []netip.Prefix{netip.MustParsePrefix("46.4.1.10/32")}
	for s, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true, "203.0.114.9": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "224.0.0.1": false,
		"::1": false, "fd00::1": false, "fe80::1": false, "::ffff:127.0.0.1": false,
		"46.4.1.10": false,
	} {
		if got := listFetchAllowed(netip.MustParseAddr(s), own); got != want {
			t.Errorf("listFetchAllowed(%s) = %v, want %v", s, got, want)
		}
	}
}

// The dial refuses an inside host before connecting, whatever the URL said.
func TestListDial_RefusesInsideHosts(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "localhost:443", "[::1]:443", "169.254.169.254:443"} {
		c, err := listDial(context.Background(), "tcp", addr)
		if err == nil {
			c.Close()
			t.Errorf("listDial(%s) connected", addr)
			continue
		}
		if !strings.Contains(err.Error(), "may not be downloaded from") {
			t.Errorf("listDial(%s): %v", addr, err)
		}
	}
}

func TestListCheckRedirect(t *testing.T) {
	mk := func(s string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, s, nil)
		return r
	}
	if err := listCheckRedirect(mk("https://lists.example.org/b"), []*http.Request{mk("https://a")}); err != nil {
		t.Errorf("an https redirect was refused: %v", err)
	}
	if err := listCheckRedirect(mk("http://lists.example.org/b"), []*http.Request{mk("https://a")}); err == nil {
		t.Error("a redirect to http was followed")
	}
	five := []*http.Request{mk("https://a"), mk("https://a"), mk("https://a"), mk("https://a"), mk("https://a")}
	if err := listCheckRedirect(mk("https://lists.example.org/b"), five); err == nil {
		t.Error("a sixth redirect was followed")
	}
}

func TestListFetch_RefusesPlainHTTP(t *testing.T) {
	if _, err := listFetch(context.Background(), "http://lists.example.org/x"); err == nil {
		t.Fatal("listFetch downloaded over plain http")
	}
}

// Spamhaus DROP: ; comments and a note after each range.
func TestParseIPList_TrailingNotesAndSemicolonComments(t *testing.T) {
	body := "; Spamhaus DROP List 2026/10/20\n; https://www.spamhaus.org/drop/drop.txt\n1.10.16.0/20 ; SBL256894\n2.56.192.0/22 ; SBL459831\n5.42.92.7\tnote\n"
	got, invalid, err := parseIPList([]byte(body))
	if err != nil || invalid != 0 || len(got) != 3 || got[0].String() != "1.10.16.0/20" || got[2].String() != "5.42.92.7/32" {
		t.Fatalf("parseIPList = %v, %d, %v", got, invalid, err)
	}
}

// The panel's public IP may be on no interface (1:1 NAT); a run's protected
// addresses are refused as download sources too.
func TestListDial_RefusesTheRunsProtectedAddresses(t *testing.T) {
	ctx := context.WithValue(context.Background(), listDenyKey{}, []netip.Prefix{netip.MustParsePrefix("8.8.8.8/32")})
	c, err := listDial(ctx, "tcp", "8.8.8.8:443")
	if err == nil {
		c.Close()
		t.Fatal("listDial connected to a protected address")
	}
	if !strings.Contains(err.Error(), "may not be downloaded from") {
		t.Fatalf("listDial: %v", err)
	}
}

// An address in Google's or Bing's published crawler ranges is never
// banned, from a catalog list or a custom one, so search engines always
// reach the sites. FireHOL level 2 and 3 carry some on most days.
func TestRunLists_NeverBansGoogleOrBingCrawlers(t *testing.T) {
	const custom = "https://lists.example.org/attackers.txt"
	f := &listsFake{bodies: map[string]string{
		listCrawlerURLs[0]: `{"creationTime":"2026-10-20T14:46:18.000000","prefixes":[{"ipv4Prefix":"66.249.64.0/27"},{"ipv6Prefix":"2001:4860:4801:10::/64"}]}`,
		listCrawlerURLs[4]: `{"creationTime":"2024-01-03T10:00:00.121331","prefixes":[{"ipv4Prefix":"157.55.39.0/24"}]}`,
		level2URL: strings.Join([]string{
			"66.249.64.5",          // Googlebot
			"2001:4860:4801:10::1", // Googlebot, IPv6
			"157.55.39.0/24",       // Bingbot
			"157.55.0.0/16",        // holds Bingbot's range
			"80.94.95.81",          // kept
		}, "\n"),
		custom: "66.249.64.9\n45.155.205.233\n",
	}}
	installListsFake(t, f, `[]`)

	res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level2"}, Custom: []string{custom}})

	if got := f.stored("firehol_level2"); !slices.Equal(got, []string{"80.94.95.81/32"}) {
		t.Fatalf("level 2 kept %v, want only 80.94.95.81", got)
	}
	if r := res[1]; r.DroppedCrawler != 4 || r.Imported != 1 || r.Error != "" {
		t.Fatalf("level 2 result %+v: want 4 crawler addresses left out and 1 kept", r)
	}
	if got := f.stored(blocklists.CustomKey(custom)); !slices.Equal(got, []string{"45.155.205.233/32"}) {
		t.Fatalf("custom list kept %v, want only 45.155.205.233", got)
	}
	if got := f.banned(); !slices.Equal(got, []string{"45.155.205.233/32", "80.94.95.81/32"}) {
		t.Fatalf("firewall bans %v", got)
	}
}

// A crawler file that cannot be downloaded, or comes back as something
// else, is taken from the last good copy.
func TestRunLists_CrawlerFileDownUsesLastCopy(t *testing.T) {
	f := &listsFake{bodies: map[string]string{
		listCrawlerURLs[4]: `{"prefixes":[{"ipv4Prefix":"157.55.39.0/24"}]}`,
		level1URL:          "157.55.39.10\n5.188.10.0/24\n",
	}}
	installListsFake(t, f, `[]`)
	runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1"}})

	for _, body := range []string{"", "<html><body>Service unavailable</body></html>"} {
		if body == "" {
			delete(f.bodies, listCrawlerURLs[4])
		} else {
			f.bodies[listCrawlerURLs[4]] = body
		}
		res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1"}})
		if got := f.stored("firehol_level1"); !slices.Equal(got, []string{"5.188.10.0/24"}) {
			t.Fatalf("Bing's file %q: kept %v, want only 5.188.10.0/24", body, got)
		}
		if res[0].DroppedCrawler != 1 || res[0].Error != "" {
			t.Fatalf("Bing's file %q: result %+v, want 1 crawler address left out", body, res[0])
		}
	}
}

// With a crawler file that cannot be read and no earlier copy, no list that
// is on is refreshed: it keeps the bans it had. A list that is off still
// loses its old decisions.
func TestRunLists_NoCrawlerAddressesKeepsOldBans(t *testing.T) {
	f := &listsFake{noCrawlerDefaults: true, bodies: map[string]string{level1URL: "5.188.10.0/24\n"}}
	installListsFake(t, f, `[]`)
	old := listsState{Lists: map[string]listEntries{"firehol_level1": {At: time.Now().Add(-time.Hour).UTC(), Entries: []string{"45.155.205.0/24"}}}}
	if err := writeListsState(old); err != nil {
		t.Fatal(err)
	}

	res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1"}})

	if !strings.Contains(res[0].Error, "crawler addresses could not be loaded") {
		t.Fatalf("level 1 result %+v: want the crawler error", res[0])
	}
	if got := f.banned(); !slices.Equal(got, []string{"45.155.205.0/24"}) {
		t.Fatalf("firewall bans %v, want level 1's earlier bans", got)
	}
	if slices.Contains(f.deletes, "jabali-list:firehol_level1") || !slices.Contains(f.deletes, "jabali-list:firehol_level2") {
		t.Fatalf("deleted %v: want level 2's old decisions only", f.deletes)
	}
}

func TestParseCrawlerRanges(t *testing.T) {
	got, err := parseCrawlerRanges([]byte(`{"prefixes":[{"ipv4Prefix":"66.249.64.1/27"},{"ipv6Prefix":"2001:4860:4801:10::/64"}]}`))
	if err != nil || len(got) != 2 || got[0].String() != "66.249.64.0/27" || got[1].String() != "2001:4860:4801:10::/64" {
		t.Fatalf("parseCrawlerRanges = %v, %v", got, err)
	}
	for _, bad := range []string{`{"prefixes":[]}`, `<html></html>`, `{"prefixes":[{"ipv4Prefix":"nope"}]}`, `{"prefixes":[{}]}`} {
		if _, err := parseCrawlerRanges([]byte(bad)); err == nil {
			t.Errorf("parseCrawlerRanges(%s): want an error", bad)
		}
	}
}
