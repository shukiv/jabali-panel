package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// listsFake stands in for cscli and the network. imports maps a reason to
// the values its import file held; deletes lists each deleted reason.
type listsFake struct {
	mu      sync.Mutex
	bodies  map[string]string // URL -> body; missing = download error
	fetched []string
	imports map[string][]string
	deletes []string
}

func installListsFake(t *testing.T, f *listsFake, allowlists string) {
	t.Helper()
	f.imports = map[string][]string{}
	prevFetch, prevAddrs, prevExec := listFetch, listInterfaceAddrs, execCommandContext
	t.Cleanup(func() { listFetch, listInterfaceAddrs, execCommandContext = prevFetch, prevAddrs, prevExec })
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
		switch {
		case strings.HasPrefix(joined, "allowlists list"):
			return exec.CommandContext(ctx, "printf", "%s", allowlists)
		case strings.HasPrefix(joined, "decisions delete"):
			f.mu.Lock()
			f.deletes = append(f.deletes, args[len(args)-1])
			f.mu.Unlock()
			return exec.CommandContext(ctx, "true")
		case strings.HasPrefix(joined, "decisions import"):
			raw, err := os.ReadFile(args[slices.Index(args, "-i")+1])
			if err != nil {
				t.Errorf("import file: %v", err)
			}
			var ds []listDecision
			if err := json.Unmarshal(raw, &ds); err != nil {
				t.Errorf("import file is not JSON decisions: %v", err)
			}
			var vals []string
			for _, d := range ds {
				if d.Type != "ban" || d.Duration != listDecisionDuration {
					t.Errorf("decision %+v: want a %s ban", d, listDecisionDuration)
				}
				if (d.Scope == "ip") == strings.Contains(d.Value, "/") {
					t.Errorf("decision %+v: scope does not match the value", d)
				}
				vals = append(vals, d.Value)
			}
			f.mu.Lock()
			f.imports[ds[0].Reason] = vals
			f.mu.Unlock()
			return exec.CommandContext(ctx, "true")
		}
		t.Errorf("unexpected command %s %s", name, joined)
		return exec.CommandContext(ctx, "false")
	}
}

const level1URL = "https://iplists.firehol.org/files/firehol_level1.netset"
const level2URL = "https://iplists.firehol.org/files/firehol_level2.netset"

// The list's private, loopback, multicast and over-wide ranges, the server's
// own address, the panel's protected address and allowlist entries are never
// imported.
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
		"2a0e:1c80::/29",   // wider than /32
		"2a0e:1c80:1::/48", // kept
	}, "\n")}}
	installListsFake(t, f, `[{"name":"jabali-admin-allowlist","items":[{"value":"91.198.174.192/27"}]}]`)

	res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1"}, Protect: []string{"185.10.20.30"}})

	got := f.imports["jabali-list:firehol_level1"]
	want := []string{"5.188.10.0/24", "45.155.205.233", "2a0e:1c80:1::/48"}
	if !slices.Equal(got, want) {
		t.Fatalf("imported %v\nwant      %v", got, want)
	}
	r := res[0]
	if r.Key != "firehol_level1" || r.Imported != 3 || r.DroppedReserved != 10 || r.DroppedProtected != 3 || r.Error != "" {
		t.Fatalf("result %+v: want 3 imported, 10 reserved, 3 protected", r)
	}
}

// A list that is off has its bans removed, and is not downloaded.
func TestRunLists_ListTurnedOffLosesItsBans(t *testing.T) {
	f := &listsFake{bodies: map[string]string{}}
	installListsFake(t, f, `[]`)
	res := runLists(context.Background(), listsApplyParams{})
	if len(f.fetched) != 0 {
		t.Fatalf("downloaded %v with every list off", f.fetched)
	}
	want := []string{"jabali-list:firehol_level1", "jabali-list:firehol_level2", "jabali-list:firehol_level3"}
	if !slices.Equal(f.deletes, want) {
		t.Fatalf("deleted %v, want %v", f.deletes, want)
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
	f := &listsFake{bodies: map[string]string{
		level2URL: "<html><body>\n" + strings.Repeat("<p>Service unavailable</p>\n", 20) + "1.2.3.4\n</body></html>",
	}}
	installListsFake(t, f, `[]`)
	res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1", "firehol_level2"}})
	for _, reason := range f.deletes {
		if reason == "jabali-list:firehol_level1" || reason == "jabali-list:firehol_level2" {
			t.Fatalf("deleted %s although its new copy could not be used", reason)
		}
	}
	if len(f.imports) != 0 {
		t.Fatalf("imported %v from failed downloads", f.imports)
	}
	if !strings.Contains(res[0].Error, "download failed") || !strings.Contains(res[1].Error, "not an IP list") {
		t.Fatalf("results %+v / %+v: want a download error and a not-an-IP-list error", res[0], res[1])
	}
}

// An address on two lists is imported once, by the first.
func TestRunLists_AddressOnTwoListsImportedOnce(t *testing.T) {
	f := &listsFake{bodies: map[string]string{
		level1URL: "5.188.10.0/24\n45.155.205.233\n",
		level2URL: "45.155.205.233\n80.94.95.81\n",
	}}
	installListsFake(t, f, `[]`)
	res := runLists(context.Background(), listsApplyParams{Enabled: []string{"firehol_level1", "firehol_level2"}})
	if got := f.imports["jabali-list:firehol_level2"]; !slices.Equal(got, []string{"80.94.95.81"}) {
		t.Fatalf("level 2 imported %v, want only 80.94.95.81", got)
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
	if s.Running || s.LastRun == "" || len(s.Lists) != 3 || s.Lists[0].Imported != 1 {
		t.Fatalf("status %+v: want a finished run with level 1 imported", s)
	}
}
