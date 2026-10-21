// The extra blocklists' bans live in the agent's own nftables table, not in
// CrowdSec decisions.
//
// crowdsec-firewall-bouncer's nftables mode keeps only the address of a range
// decision (it cuts the "/len"), so each network on a list banned one address
// on every port but the websites. Deleting a large list's decisions also made
// the bouncer retry them one by one for minutes, during which no new ban of
// any kind reached the firewall, and some stayed in its sets. The lists
// instead go into table inet jabali_lists: interval sets that hold whole
// networks, dropped on input and forward, replaced in one nft transaction.
//
// listsEntriesState keeps each list's filtered entries and when they were
// downloaded. A render bans them until listLifetime after that download, as
// the 48 h decisions did, so a list that fails to download keeps its bans
// until then. renderLists runs after each run, when the agent starts (an
// nftables table does not survive a reboot) and after an allowlist entry is
// added or removed, which then takes effect at once.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	listsNftTable = "jabali_lists"
	listLifetime  = 48 * time.Hour
	// listNftChunk is the most elements one add element statement holds.
	listNftChunk = 2000
)

var (
	listsEntriesState = "/var/lib/jabali/crowdsec-lists.json"
	listsNftPath      = "/var/lib/jabali/crowdsec-lists.nft"
	// listNftApply loads the nft script at path; a seam for tests.
	listNftApply = func(ctx context.Context, path string) error {
		out, err := execCommandContext(ctx, "nft", "-f", path).CombinedOutput()
		if err != nil {
			return fmt.Errorf("nft -f: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	listsRenderMu      sync.Mutex
	listsRerenderCh    = make(chan struct{}, 1)
	listsRerenderOnce  sync.Once
	listsRerenderDelay = 2 * time.Second
	listsStartRetry    = 30 * time.Second
	listsStartTries    = 20
)

// listEntries is what one list bans and when it was downloaded.
type listEntries struct {
	At      time.Time `json:"at"`
	Entries []string  `json:"entries"`
}

// listsState is every list that is on, and the panel's protected addresses
// of the last run.
type listsState struct {
	Protect []string               `json:"protect"`
	Lists   map[string]listEntries `json:"lists"`
}

func readListsState() listsState {
	st := listsState{Lists: map[string]listEntries{}}
	raw, err := os.ReadFile(listsEntriesState)
	if err != nil || json.Unmarshal(raw, &st) != nil || st.Lists == nil {
		return listsState{Lists: map[string]listEntries{}}
	}
	return st
}

func writeListsState(st listsState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeListsFile(listsEntriesState, raw)
}

func writeListsFile(path string, data []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// renderLists replaces the firewall table with the stored lists. The
// protected addresses are left out again, so an allowlist entry added since
// the download is honoured. When the allowlists cannot be read the table is
// left as it is, rather than banning an address that may be on one.
func renderLists(ctx context.Context) error {
	listsRenderMu.Lock()
	defer listsRenderMu.Unlock()
	st := readListsState()
	protect, err := listProtected(ctx, st.Protect)
	if err != nil {
		return err
	}
	script := renderListsRuleset(st.Lists, protect, time.Now())
	if err := writeListsFile(listsNftPath, []byte(script)); err != nil {
		return fmt.Errorf("write %s: %v", listsNftPath, err)
	}
	return listNftApply(ctx, listsNftPath)
}

// renderListsRuleset is the nft script that replaces table inet jabali_lists
// with the lists' entries, each timing out listLifetime after its list's
// download. An entry inside a wider one is left out, since nft refuses
// overlapping intervals; one on two lists keeps the later timeout. With
// nothing to ban the script only removes the table.
func renderListsRuleset(lists map[string]listEntries, protect []netip.Prefix, now time.Time) string {
	left := map[netip.Prefix]time.Duration{}
	for _, l := range lists {
		ttl := l.At.Add(listLifetime).Sub(now).Truncate(time.Second)
		if ttl < time.Second {
			continue
		}
		for _, s := range l.Entries {
			p, ok := parseIPOrPrefix(s)
			if !ok || listIsReserved(p) || overlapsAny(p, protect) {
				continue
			}
			if ttl > left[p] {
				left[p] = ttl
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table inet %[1]s\ndelete table inet %[1]s\n", listsNftTable)
	if len(left) == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, `table inet %s {
	set v4 {
		type ipv4_addr
		flags interval, timeout
	}
	set v6 {
		type ipv6_addr
		flags interval, timeout
	}
	chain input {
		type filter hook input priority -10; policy accept;
		ip saddr @v4 drop
		ip6 saddr @v6 drop
	}
	chain forward {
		type filter hook forward priority -10; policy accept;
		ip saddr @v4 drop
		ip6 saddr @v6 drop
	}
}
`, listsNftTable)
	for _, set := range []struct {
		name string
		v4   bool
	}{{"v4", true}, {"v6", false}} {
		els := outermost(left, set.v4)
		for i := 0; i < len(els); i += listNftChunk {
			end := min(i+listNftChunk, len(els))
			parts := make([]string, 0, end-i)
			for _, p := range els[i:end] {
				parts = append(parts, fmt.Sprintf("%s timeout %ds", p, int64(left[p]/time.Second)))
			}
			fmt.Fprintf(&b, "add element inet %s %s { %s }\n", listsNftTable, set.name, strings.Join(parts, ", "))
		}
	}
	return b.String()
}

// outermost is left's prefixes of one family, sorted, without any that lies
// inside another; the outer one takes the longer timeout.
func outermost(left map[netip.Prefix]time.Duration, v4 bool) []netip.Prefix {
	var ps []netip.Prefix
	for p := range left {
		if p.Addr().Is4() == v4 {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(i, j int) bool {
		if c := ps[i].Addr().Compare(ps[j].Addr()); c != 0 {
			return c < 0
		}
		return ps[i].Bits() < ps[j].Bits()
	})
	out := ps[:0]
	for _, p := range ps {
		// Sorted this way, a prefix inside another comes right after the
		// last one kept, which is the only one that can hold it.
		if n := len(out); n > 0 && out[n-1].Contains(p.Addr()) {
			if left[p] > left[out[n-1]] {
				left[out[n-1]] = left[p]
			}
			continue
		}
		out = append(out, p)
	}
	return out
}

// StartCrowdsecLists puts the stored lists back into the firewall when the
// agent starts, as an nftables table does not survive a reboot. CrowdSec may
// start after the agent, so a failed render is tried again every
// listsStartRetry, up to listsStartTries times.
func StartCrowdsecLists(ctx context.Context) {
	if _, err := os.Stat(listsEntriesState); err != nil {
		return
	}
	go func() {
		for i := 0; i < listsStartTries; i++ {
			rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			err := renderLists(rctx)
			cancel()
			if err == nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(listsStartRetry):
			}
		}
	}()
}

// requestListsRerender renders the lists again shortly after an allowlist
// change, so an address just allowlisted is let in at once, and one just
// removed is banned again. Changes in quick succession (a country sync) make
// one render.
func requestListsRerender() {
	if _, err := os.Stat(listsEntriesState); err != nil {
		return
	}
	listsRerenderOnce.Do(func() { go listsRerenderLoop() })
	select {
	case listsRerenderCh <- struct{}{}:
	default:
	}
}

func listsRerenderLoop() {
	for range listsRerenderCh {
		time.Sleep(listsRerenderDelay)
		select {
		case <-listsRerenderCh:
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_ = renderLists(ctx)
		cancel()
	}
}
