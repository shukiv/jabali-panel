package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeAuthCacheFlusher is a mailAuthCacheFlusher on a hand-driven clock:
// flushes are counted, and a scheduled trailing flush is held until the test
// runs it.
type fakeAuthCacheFlusher struct {
	*mailAuthCacheFlusher
	clock     time.Time
	flushes   int
	flushErr  error
	scheduled []func()
	delays    []time.Duration
}

func newFakeAuthCacheFlusher() *fakeAuthCacheFlusher {
	f := &fakeAuthCacheFlusher{clock: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	f.mailAuthCacheFlusher = &mailAuthCacheFlusher{
		minGap: mailAuthCacheMinGap,
		now:    func() time.Time { return f.clock },
		after: func(d time.Duration, fn func()) {
			f.delays = append(f.delays, d)
			f.scheduled = append(f.scheduled, fn)
		},
		flush: func(context.Context) error {
			f.flushes++
			return f.flushErr
		},
	}
	return f
}

// useFakeAuthCache swaps the package flusher for the test's.
func useFakeAuthCache(t *testing.T) *fakeAuthCacheFlusher {
	t.Helper()
	f := newFakeAuthCacheFlusher()
	prev := mailAuthCache
	mailAuthCache = f.mailAuthCacheFlusher
	t.Cleanup(func() { mailAuthCache = prev })
	return f
}

func TestMailAuthCache_FirstRequestFlushesAtOnce(t *testing.T) {
	f := newFakeAuthCacheFlusher()
	if err := f.request(context.Background()); err != nil {
		t.Fatalf("request: %v", err)
	}
	if f.flushes != 1 || len(f.scheduled) != 0 {
		t.Fatalf("flushes=%d scheduled=%d, want 1 flush at once and nothing scheduled", f.flushes, len(f.scheduled))
	}
}

// A change inside the gap must still be followed by a flush — a trailing one
// at the end of the gap, never dropped.
func TestMailAuthCache_RequestInsideTheGapSchedulesOneTrailingFlush(t *testing.T) {
	f := newFakeAuthCacheFlusher()
	_ = f.request(context.Background())

	f.clock = f.clock.Add(2 * time.Second)
	if err := f.request(context.Background()); err != nil {
		t.Fatalf("second request: %v", err)
	}
	f.clock = f.clock.Add(time.Second)
	_ = f.request(context.Background()) // a third change, same gap

	if f.flushes != 1 {
		t.Fatalf("flushes=%d inside the gap, want 1 (the second must wait)", f.flushes)
	}
	if len(f.scheduled) != 1 {
		t.Fatalf("scheduled=%d, want exactly one trailing flush for both changes", len(f.scheduled))
	}
	if f.delays[0] != 3*time.Second {
		t.Errorf("trailing flush in %v, want 3s (the rest of the 5s gap)", f.delays[0])
	}

	f.clock = f.clock.Add(2 * time.Second)
	f.scheduled[0]()
	if f.flushes != 2 {
		t.Fatalf("flushes=%d after the trailing flush ran, want 2", f.flushes)
	}

	// The trailing flush opened a new gap; a later change past it flushes at
	// once again.
	f.clock = f.clock.Add(mailAuthCacheMinGap)
	_ = f.request(context.Background())
	if f.flushes != 3 || len(f.scheduled) != 1 {
		t.Fatalf("flushes=%d scheduled=%d, want a third flush at once", f.flushes, len(f.scheduled))
	}
}

func TestMailAuthCache_VerbFlushesAndReportsFailure(t *testing.T) {
	f := useFakeAuthCache(t)
	if _, err := Default.Dispatch(context.Background(), "mail.auth_cache.flush", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if f.flushes != 1 {
		t.Fatalf("flushes=%d, want 1", f.flushes)
	}

	f.clock = f.clock.Add(mailAuthCacheMinGap)
	f.flushErr = errors.New("stalwart down")
	if _, err := Default.Dispatch(context.Background(), "mail.auth_cache.flush", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a failed flush was reported as done; the panel must be able to log it")
	}
}

// The real flush is Stalwart's InvalidateCaches action.
func TestMailAuthCache_FlushRunsInvalidateCaches(t *testing.T) {
	var argv []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		argv = append([]string{name}, args...)
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })
	prevToken := stalwartAdminTokenFunc
	stalwartAdminTokenFunc = func() (string, error) { return "test-token", nil }
	t.Cleanup(func() { stalwartAdminTokenFunc = prevToken })

	f := &mailAuthCacheFlusher{minGap: mailAuthCacheMinGap, now: time.Now, flush: mailAuthCache.flush}
	if err := f.request(context.Background()); err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := strings.Join(argv, " "); got != "stalwart-cli create Action/InvalidateCaches" {
		t.Fatalf("ran %q, want stalwart-cli create Action/InvalidateCaches", got)
	}
}

// Every verb that ends or changes a mailbox login flushes the cache after it
// succeeds — including the "nothing in Stalwart to remove" early returns.
// Dispatched through Default, so the registration is what is tested.
func TestMailAuthCache_LoginEndingVerbsFlush(t *testing.T) {
	srv := newJMAPServer(t, map[string]jmapHandler{
		// No domain in Stalwart's registry: every verb takes its no-op path.
		"x:Domain/query": jmapHandlerReturning(jmapQueryResult{}),
		"x:Domain/set":   jmapHandlerReturning(jmapSetResult{Created: map[string]json.RawMessage{"#d1": json.RawMessage(`{"id":"dom-1"}`)}}),
		"x:Account/set":  jmapHandlerReturning(jmapSetResult{Created: map[string]json.RawMessage{"#a1": json.RawMessage(`{"id":"acct-1"}`)}}),
	})
	defer srv.Close()
	wireJMAP(t, srv)

	cases := []struct {
		verb   string
		params string
	}{
		{"mailbox.set_password", `{"id":"mb-1","email":"alice@example.com"}`},
		{"mailbox.delete", `{"id":"mb-1","email":"alice@example.com"}`},
		{"mail.domain.purge_accounts", `{"domain":"example.com"}`},
		{"mail.domain.rename", `{"old":"example.com","new":"example.org"}`},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			f := useFakeAuthCache(t)
			if _, err := Default.Dispatch(context.Background(), tc.verb, json.RawMessage(tc.params)); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if f.flushes != 1 {
				t.Fatalf("flushes=%d after %s, want 1 — a cached JMAP login would outlive the change", f.flushes, tc.verb)
			}
		})
	}
}

// A verb that failed changed nothing, so it does not flush.
func TestMailAuthCache_FailedVerbDoesNotFlush(t *testing.T) {
	f := useFakeAuthCache(t)
	if _, err := Default.Dispatch(context.Background(), "mailbox.delete", json.RawMessage(`{"email":"alice@example.com"}`)); err == nil {
		t.Fatal("mailbox.delete without an id succeeded")
	}
	if f.flushes != 0 {
		t.Fatalf("flushes=%d after a failed verb, want 0", f.flushes)
	}
}
