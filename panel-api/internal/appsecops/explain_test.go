package appsecops

import (
	"reflect"
	"testing"
)

func ev(ts, ip, host, uri string, rules ...string) Event {
	return Event{Timestamp: ts, SourceIP: ip, TargetHost: host, TargetURI: uri, RuleIDs: rules}
}

func TestGroupEvents_GroupsCountsAndAnnotates(t *testing.T) {
	events := []Event{
		ev("2026-09-27T10:05:00Z", "1.1.1.1", "shop.example.com", "/cart", "901340", "942100", "949110"),
		ev("2026-09-27T10:01:00Z", "2.2.2.2", "shop.example.com", "/cart", "901340", "942100", "949110"),
		ev("2026-09-27T10:09:00Z", "1.1.1.1", "shop.example.com", "/cart", "901340", "942100", "949110"),
		ev("2026-09-27T10:02:00Z", "3.3.3.3", "blog.example.com", "/wp-json/x", "930130"),
	}
	got := GroupEvents(events)
	if len(got) != 2 {
		t.Fatalf("patterns = %d, want 2", len(got))
	}
	p := got[0]
	if p.Host != "shop.example.com" || p.Count != 3 || p.DistinctIPs != 2 {
		t.Errorf("top pattern = %+v, want shop.example.com with 3 blocks from 2 IPs", p)
	}
	if p.FirstAt != "2026-09-27T10:01:00Z" || p.LastAt != "2026-09-27T10:09:00Z" {
		t.Errorf("first/last = %s / %s, want the earliest and the latest", p.FirstAt, p.LastAt)
	}
	if !reflect.DeepEqual(p.Detections, []string{"942100"}) {
		t.Errorf("detections = %v, want only 942100", p.Detections)
	}
	if len(p.Infra) != 2 || p.Infra[0].ID != "901340" || p.Infra[1].ID != "949110" || p.Infra[0].Note == "" {
		t.Errorf("infra = %+v, want 901340 and 949110 with notes", p.Infra)
	}
	if !reflect.DeepEqual(got[1].RuleIDs, []string{"930130"}) || got[1].Count != 1 {
		t.Errorf("second pattern = %+v", got[1])
	}
}

// Equal counts sort by host, then URI, then rules, whatever order the agent
// returned the events in.
func TestGroupEvents_TiesAreDeterministic(t *testing.T) {
	a := ev("2026-09-27T10:00:00Z", "1.1.1.1", "b.example.com", "/x", "942100")
	b := ev("2026-09-27T10:00:00Z", "1.1.1.1", "a.example.com", "/y", "942100")
	c := ev("2026-09-27T10:00:00Z", "1.1.1.1", "a.example.com", "/x", "942200")
	d := ev("2026-09-27T10:00:00Z", "1.1.1.1", "a.example.com", "/x", "942100")
	want := []string{"a.example.com/x#942100", "a.example.com/x#942200", "a.example.com/y#942100", "b.example.com/x#942100"}
	for _, order := range [][]Event{{a, b, c, d}, {d, c, b, a}, {c, a, d, b}} {
		var got []string
		for _, p := range GroupEvents(order) {
			got = append(got, p.Host+p.URI+"#"+p.RuleIDs[0])
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("order = %v, want %v", got, want)
		}
	}
}

func TestGroupEvents_OnlyInfraLeavesNoDetection(t *testing.T) {
	got := GroupEvents([]Event{ev("2026-09-27T10:00:00Z", "1.1.1.1", "h.example.com", "/", "901340", "949110")})
	if len(got) != 1 || len(got[0].Detections) != 0 || got[0].Detections == nil {
		t.Errorf("detections = %#v, want an empty, non-nil list", got[0].Detections)
	}
}

func TestGroupEvents_Empty(t *testing.T) {
	if got := GroupEvents(nil); got == nil || len(got) != 0 {
		t.Errorf("GroupEvents(nil) = %#v, want an empty, non-nil list", got)
	}
}

func TestGroupInlineBlocks_ByIPInFirstSeenOrder(t *testing.T) {
	got := GroupInlineBlocks([]InlineBlock{
		{Timestamp: "t1", SourceIP: "9.9.9.9", Scores: "lfi: 5"},
		{Timestamp: "t2", SourceIP: "8.8.8.8", Scores: "rce: 5"},
		{Timestamp: "t3", SourceIP: "9.9.9.9", Scores: "sql_injection: 10"},
	})
	want := []InlineBlockGroup{
		{SourceIP: "9.9.9.9", Count: 2, LastAt: "t3", LastScores: "sql_injection: 10"},
		{SourceIP: "8.8.8.8", Count: 1, LastAt: "t2", LastScores: "rce: 5"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
