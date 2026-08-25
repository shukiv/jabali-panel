package eventsources

import (
	"strings"
	"testing"
	"time"
)

// fakeClock is a mutable clock for driving the grouper deterministically.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func newGrouperAt(base time.Time) (*sshLoginGrouper, *fakeClock) {
	c := &fakeClock{t: base}
	return newSSHLoginGrouper(c.now), c
}

func base() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) }

func TestSSHGroup_FirstLoginNotifiesImmediately(t *testing.T) {
	g, _ := newGrouperAt(base())
	out := g.observe("alice", "1.2.3.4", "publickey")
	if len(out) != 1 {
		t.Fatalf("first login should emit exactly one envelope, got %d", len(out))
	}
	if !strings.Contains(out[0].Title, "SSH login: alice from 1.2.3.4") {
		t.Errorf("unexpected title: %q", out[0].Title)
	}
}

func TestSSHGroup_RepeatsWithinWindowSuppressed(t *testing.T) {
	g, c := newGrouperAt(base())
	g.observe("drfeed", "1.2.3.4", "publickey") // first → immediate
	total := 0
	for i := 0; i < 20; i++ {
		c.add(time.Minute) // every minute, within the 10-min sliding window
		total += len(g.observe("drfeed", "1.2.3.4", "publickey"))
	}
	if total != 0 {
		t.Fatalf("repeat logins within the window must be suppressed, but %d envelopes were emitted", total)
	}
}

func TestSSHGroup_NewIPBypassesAggregation(t *testing.T) {
	g, _ := newGrouperAt(base())
	g.observe("alice", "1.2.3.4", "publickey")
	out := g.observe("alice", "9.9.9.9", "publickey") // different IP
	if len(out) != 1 || !strings.Contains(out[0].Title, "from 9.9.9.9") {
		t.Fatalf("a new source IP must fire immediately; got %+v", out)
	}
}

func TestSSHGroup_NewMethodBypassesAggregation(t *testing.T) {
	g, _ := newGrouperAt(base())
	g.observe("alice", "1.2.3.4", "publickey")
	out := g.observe("alice", "1.2.3.4", "password") // different method
	if len(out) != 1 || !strings.Contains(out[0].Body, "via password") {
		t.Fatalf("a new auth method must fire immediately; got %+v", out)
	}
}

func TestSSHGroup_QuietCloseEmitsCountedSummary(t *testing.T) {
	g, c := newGrouperAt(base())
	g.observe("drfeed", "1.2.3.4", "publickey") // 1
	c.add(time.Minute)
	g.observe("drfeed", "1.2.3.4", "publickey") // 2
	c.add(time.Minute)
	g.observe("drfeed", "1.2.3.4", "publickey") // 3

	// Go quiet past the window, then flush.
	c.add(11 * time.Minute)
	out := g.flush()
	if len(out) != 1 {
		t.Fatalf("a closed group with repeats should emit one summary, got %d", len(out))
	}
	if !strings.Contains(out[0].Body, "3 times") {
		t.Errorf("summary should carry the count of 3; body=%q", out[0].Body)
	}
	// A single-login group emits no summary on close.
	g.observe("bob", "5.6.7.8", "publickey")
	c.add(11 * time.Minute)
	if s := g.flush(); len(s) != 0 {
		t.Errorf("a single-login group must not emit a close summary, got %d", len(s))
	}
}

func TestSSHGroup_ContinuouslyActiveRollsUpPerWindow(t *testing.T) {
	g, c := newGrouperAt(base())
	g.observe("drfeed", "1.2.3.4", "publickey") // first → immediate
	// Keep it active every minute for > one window, never quiet.
	summaries := 0
	for i := 0; i < 12; i++ {
		c.add(time.Minute)
		g.observe("drfeed", "1.2.3.4", "publickey")
		summaries += len(g.flush())
	}
	if summaries == 0 {
		t.Fatal("a continuously-active group must emit at least one rolling digest across a window")
	}
}
