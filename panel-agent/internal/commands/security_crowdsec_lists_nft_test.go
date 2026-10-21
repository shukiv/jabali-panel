package commands

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

// Each list's entries time out listLifetime after its download; one inside
// a wider one is left out and the wider one takes the later timeout; an
// expired list, a reserved entry and a protected one are left out.
func TestRenderListsRuleset(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	lists := map[string]listEntries{
		"a": {At: now.Add(-47 * time.Hour), Entries: []string{"5.188.0.0/16", "45.155.205.233/32", "2a0e:1c80:1::/48", "10.0.0.0/8"}},
		"b": {At: now.Add(-time.Hour), Entries: []string{"5.188.10.0/24", "80.94.95.81/32", "91.198.174.200/32"}},
		"c": {At: now.Add(-49 * time.Hour), Entries: []string{"185.1.1.1/32"}},
	}
	protect := []netip.Prefix{netip.MustParsePrefix("91.198.174.192/27")}
	got := renderListsRuleset(lists, protect, now)

	for _, want := range []string{
		"table inet jabali_lists\ndelete table inet jabali_lists\n",
		"flags interval, timeout",
		"type filter hook input priority -10; policy accept;",
		"type filter hook forward priority -10; policy accept;",
		"ip saddr @v4 drop",
		"ip6 saddr @v6 drop",
		"add element inet jabali_lists v4 { 5.188.0.0/16 timeout 169200s, 45.155.205.233/32 timeout 3600s, 80.94.95.81/32 timeout 169200s }\n",
		"add element inet jabali_lists v6 { 2a0e:1c80:1::/48 timeout 3600s }\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("script lacks %q:\n%s", want, got)
		}
	}
	for _, never := range []string{"5.188.10.0/24", "10.0.0.0/8", "91.198.174.200", "185.1.1.1"} {
		if strings.Contains(got, never) {
			t.Errorf("script holds %s:\n%s", never, got)
		}
	}
}

// With nothing to ban the script only removes the table.
func TestRenderListsRuleset_EmptyRemovesTable(t *testing.T) {
	got := renderListsRuleset(nil, nil, time.Now())
	if got != "table inet jabali_lists\ndelete table inet jabali_lists\n" {
		t.Fatalf("script:\n%s", got)
	}
}

// A large list goes in add element statements of at most listNftChunk.
func TestRenderListsRuleset_Chunks(t *testing.T) {
	now := time.Now()
	var entries []string
	for i := range listNftChunk*2 + 1 {
		entries = append(entries, fmt.Sprintf("45.%d.%d.%d/32", i>>16&0xff, i>>8&0xff, i&0xff))
	}
	got := renderListsRuleset(map[string]listEntries{"a": {At: now, Entries: entries}}, nil, now)
	if n := strings.Count(got, "add element inet jabali_lists v4 {"); n != 3 {
		t.Fatalf("%d add element statements, want 3", n)
	}
	if n := strings.Count(got, " timeout "); n != len(entries) {
		t.Fatalf("%d elements, want %d", n, len(entries))
	}
}

// An address allowlisted after its list was downloaded is let in when the
// allowlist entry is added, without waiting for the next refresh.
func TestAddToAllowlist_LetsTheAddressIn(t *testing.T) {
	f := &listsFake{bodies: map[string]string{level1URL: "5.188.10.0/24\n80.94.95.81\n"}}
	installListsFake(t, f, `[]`)
	prevDelay := listsRerenderDelay
	listsRerenderDelay = 10 * time.Millisecond
	t.Cleanup(func() { listsRerenderDelay = prevDelay })

	runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1"}})
	if got := f.banned(); !slices.Equal(got, []string{"5.188.10.0/24", "80.94.95.81/32"}) {
		t.Fatalf("firewall bans %v", got)
	}

	f.mu.Lock()
	f.allowlists = `[{"name":"jabali-admin-allowlist","items":[{"value":"80.94.95.81"}]}]`
	n := len(f.scripts)
	f.mu.Unlock()
	if err := addToAllowlist(context.Background(), jabaliAllowlistName, "80.94.95.81", "admin", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		done := len(f.scripts) > n
		f.mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("adding to the allowlist did not render the lists again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := f.banned(); !slices.Equal(got, []string{"5.188.10.0/24"}) {
		t.Fatalf("firewall bans %v after the allowlist add, want 80.94.95.81 let in", got)
	}
}

// When the allowlists cannot be read the firewall is left as it is, rather
// than banning an address that may be on one, and the run says so.
func TestRunLists_AllowlistsUnreadableLeavesFirewall(t *testing.T) {
	f := &listsFake{bodies: map[string]string{level1URL: "5.188.10.0/24\n"}}
	installListsFake(t, f, `[]`)
	f.allowlistsErr = true

	res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1"}})
	if len(f.scripts) != 0 {
		t.Fatalf("applied %d nft scripts without the allowlists", len(f.scripts))
	}
	if !strings.Contains(res[0].Error, "the firewall was not updated") || !strings.Contains(res[0].Error, "allowlists") {
		t.Fatalf("level 1 result %+v: want the allowlist error", res[0])
	}
	for _, r := range res[1:] {
		if r.Error != "" {
			t.Fatalf("result %+v: a list that is off reports no error", r)
		}
	}
}

// A failed nft load is reported on each list that is on.
func TestRunLists_NftFailureReported(t *testing.T) {
	f := &listsFake{bodies: map[string]string{level1URL: "5.188.10.0/24\n"}}
	installListsFake(t, f, `[]`)
	f.nftErr = errors.New("nft -f: exit status 1: Error: Could not process rule")

	res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1"}})
	if !strings.Contains(res[0].Error, "the firewall was not updated: nft -f") {
		t.Fatalf("level 1 result %+v: want the nft error", res[0])
	}
}

// The table does not survive a reboot: the agent puts the stored lists back
// when it starts, and tries again while CrowdSec is not up yet.
func TestStartCrowdsecLists_RestoresStoredLists(t *testing.T) {
	f := &listsFake{bodies: map[string]string{}}
	installListsFake(t, f, `[]`)
	prevRetry := listsStartRetry
	listsStartRetry = 10 * time.Millisecond
	t.Cleanup(func() { listsStartRetry = prevRetry })
	st := listsState{Lists: map[string]listEntries{"firehol_level1": {At: time.Now().UTC(), Entries: []string{"5.188.10.0/24"}}}}
	if err := writeListsState(st); err != nil {
		t.Fatal(err)
	}
	f.allowlistsErr = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	StartCrowdsecLists(ctx)
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	if len(f.scripts) != 0 {
		f.mu.Unlock()
		t.Fatal("rendered without the allowlists")
	}
	f.allowlistsErr = false
	f.mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for len(f.banned()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stored lists were not put back")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := f.banned(); !slices.Equal(got, []string{"5.188.10.0/24"}) {
		t.Fatalf("firewall bans %v", got)
	}
}
