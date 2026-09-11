package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// fakeOrphanSweeper is a no-DB stand-in for pdns.Client, so the dns.reap-orphans
// safety guards can be tested without a live PowerDNS backend.
type fakeOrphanSweeper struct {
	domains    int
	domainsErr error
	counts     map[string]int
	names      []string
	deleted    map[string]int
	reapCalls  int
}

func (f *fakeOrphanSweeper) DomainsCount() (int, error)            { return f.domains, f.domainsErr }
func (f *fakeOrphanSweeper) OrphanCounts() (map[string]int, error) { return f.counts, nil }
func (f *fakeOrphanSweeper) OrphanRecordNames() ([]string, error)  { return f.names, nil }
func (f *fakeOrphanSweeper) ReapOrphans() (map[string]int, error) {
	f.reapCalls++
	return f.deleted, nil
}

func withFakeSweeper(t *testing.T, f orphanSweeper) {
	t.Helper()
	prev := reapOrphansClient
	reapOrphansClient = func() orphanSweeper { return f }
	t.Cleanup(func() { reapOrphansClient = prev })
}

func callReap(t *testing.T, apply bool) (dnsReapOrphansResponse, error) {
	t.Helper()
	params, _ := json.Marshal(map[string]any{"apply": apply})
	out, err := dnsReapOrphansHandler(context.Background(), params)
	if err != nil {
		return dnsReapOrphansResponse{}, err
	}
	resp, ok := out.(dnsReapOrphansResponse)
	if !ok {
		t.Fatalf("response type = %T, want dnsReapOrphansResponse", out)
	}
	return resp, nil
}

// TestDNSReapOrphans_EmptyDomainsRefused is the load-bearing safety guard: an
// empty domains table would make every record match the orphan predicate, so
// the handler must refuse (failed_precondition) and never call ReapOrphans —
// even with apply=true.
func TestDNSReapOrphans_EmptyDomainsRefused(t *testing.T) {
	f := &fakeOrphanSweeper{domains: 0, counts: map[string]int{"records": 999}, names: []string{"x"}}
	withFakeSweeper(t, f)

	_, err := callReap(t, true)
	if err == nil {
		t.Fatalf("expected refusal on empty domains table, got nil")
	}
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("error = %v, want AgentError{failed_precondition}", err)
	}
	if f.reapCalls != 0 {
		t.Fatalf("ReapOrphans called %d time(s) on refusal, want 0", f.reapCalls)
	}
}

// TestDNSReapOrphans_DryRunNeverDeletes: apply=false reports counts + names but
// must not call ReapOrphans and must return an empty Deleted map.
func TestDNSReapOrphans_DryRunNeverDeletes(t *testing.T) {
	f := &fakeOrphanSweeper{
		domains: 5,
		counts:  map[string]int{"records": 12, "domainmetadata": 3},
		names:   []string{"gone.example.com", "www.gone.example.com"},
		deleted: map[string]int{"records": 12, "domainmetadata": 3},
	}
	withFakeSweeper(t, f)

	resp, err := callReap(t, false)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if resp.Applied {
		t.Fatalf("Applied = true on dry run, want false")
	}
	if f.reapCalls != 0 {
		t.Fatalf("ReapOrphans called %d time(s) on dry run, want 0", f.reapCalls)
	}
	if len(resp.Deleted) != 0 {
		t.Fatalf("Deleted = %v on dry run, want empty", resp.Deleted)
	}
	if resp.Counts["records"] != 12 || len(resp.Names) != 2 {
		t.Fatalf("dry run should still report counts/names: counts=%v names=%v", resp.Counts, resp.Names)
	}
}

// TestDNSReapOrphans_ApplyDeletes: apply=true runs the sweep exactly once and
// surfaces the per-table deleted counts. Empty names keep the cache-purge loop
// (which shells out to pdns_control/rec_control) from running in the test.
func TestDNSReapOrphans_ApplyDeletes(t *testing.T) {
	f := &fakeOrphanSweeper{
		domains: 5,
		counts:  map[string]int{"records": 7},
		names:   nil, // no purge shell-outs
		deleted: map[string]int{"records": 7},
	}
	withFakeSweeper(t, f)

	resp, err := callReap(t, true)
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if !resp.Applied {
		t.Fatalf("Applied = false on apply, want true")
	}
	if f.reapCalls != 1 {
		t.Fatalf("ReapOrphans called %d time(s), want 1", f.reapCalls)
	}
	if resp.Deleted["records"] != 7 {
		t.Fatalf("Deleted[records] = %d, want 7", resp.Deleted["records"])
	}
}
