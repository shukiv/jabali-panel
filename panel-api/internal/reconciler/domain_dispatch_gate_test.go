package reconciler

import (
	"context"
	"testing"
	"time"
)

func countDomainCreate(ag *fakeAgent) int {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	n := 0
	for _, c := range ag.calls {
		if c.method == "domain.create" {
			n++
		}
	}
	return n
}

// The headline JAB-369 win: an unchanged domain on a second periodic tick makes
// no domain.create agent call, while force paths always re-dispatch.
func TestCreateDomainOnAgent_UnchangedSecondTickSkips(t *testing.T) {
	r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)

	// First periodic tick (force=false) — dispatches.
	r.createDomainOnAgent(context.Background(), dom, false)
	if got := countDomainCreate(ag); got != 1 {
		t.Fatalf("first tick must dispatch domain.create once, got %d", got)
	}

	// Second periodic tick, nothing changed — must skip the agent call.
	r.createDomainOnAgent(context.Background(), dom, false)
	if got := countDomainCreate(ag); got != 1 {
		t.Fatalf("unchanged second tick must NOT re-dispatch domain.create, got %d", got)
	}

	// A force run re-dispatches even when unchanged.
	r.createDomainOnAgent(context.Background(), dom, true)
	if got := countDomainCreate(ag); got != 2 {
		t.Fatalf("force must re-dispatch domain.create, got %d", got)
	}

	// After the force dispatch stamped the cache, the next unchanged periodic
	// tick skips again.
	r.createDomainOnAgent(context.Background(), dom, false)
	if got := countDomainCreate(ag); got != 2 {
		t.Fatalf("unchanged tick after a force must skip again, got %d", got)
	}
}

// A failed dispatch is never stamped: the next tick re-attempts.
func TestCreateDomainOnAgent_FailedDispatchRetries(t *testing.T) {
	r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)
	ag.failMethod = "domain.create"

	r.createDomainOnAgent(context.Background(), dom, false)
	r.createDomainOnAgent(context.Background(), dom, false)
	if got := countDomainCreate(ag); got != 2 {
		t.Fatalf("a failed dispatch must stay dirty and retry next tick (expected 2 attempts), got %d", got)
	}
}

func TestDesiredDomainDispatchHash_StableAndSensitive(t *testing.T) {
	a := map[string]any{"domain_id": "d1", "serve_https": true, "custom_directives": "x"}
	// Same content, different key insertion order — json.Marshal sorts keys, so
	// the hash must be identical.
	b := map[string]any{"custom_directives": "x", "serve_https": true, "domain_id": "d1"}
	if desiredDomainDispatchHash(a) != desiredDomainDispatchHash(b) {
		t.Fatal("hash must be independent of map key order")
	}
	// Any field change flips the hash.
	c := map[string]any{"domain_id": "d1", "serve_https": false, "custom_directives": "x"}
	if desiredDomainDispatchHash(a) == desiredDomainDispatchHash(c) {
		t.Fatal("a changed field must change the hash")
	}
	if desiredDomainDispatchHash(a) == "" {
		t.Fatal("a marshalable payload must hash non-empty")
	}
}

func TestDomainDispatchNeeded(t *testing.T) {
	r := &Reconciler{}
	now := time.Now()
	const hash = "abc"

	// Never dispatched → needed.
	if !r.domainDispatchNeeded("d1", hash, now) {
		t.Fatal("an un-dispatched domain must be needed")
	}

	// Record a dispatch; an unchanged payload within the interval is NOT needed.
	r.domainDispatched("d1", hash, now)
	if r.domainDispatchNeeded("d1", hash, now.Add(time.Minute)) {
		t.Fatal("an unchanged, recently-dispatched domain must be skipped")
	}

	// A changed payload is needed even within the interval.
	if !r.domainDispatchNeeded("d1", "def", now.Add(time.Minute)) {
		t.Fatal("a changed payload must be needed")
	}

	// The self-heal interval forces a re-dispatch of unchanged content.
	if !r.domainDispatchNeeded("d1", hash, now.Add(domainReDispatchInterval+time.Second)) {
		t.Fatal("past the re-dispatch interval an unchanged domain must be re-dispatched (drift repair)")
	}

	// An empty hash (marshal failure) always dispatches and is never stamped.
	if !r.domainDispatchNeeded("d2", "", now) {
		t.Fatal("an empty hash must always dispatch")
	}
	r.domainDispatched("d2", "", now)
	if _, ok := r.domainDispatchCache.Load("d2"); ok {
		t.Fatal("an empty hash must not be recorded")
	}
}

// A failed dispatch must never be stamped: the domain stays dirty and the next
// tick still treats it as needed.
func TestDomainDispatch_FailedNotStamped(t *testing.T) {
	r := &Reconciler{}
	now := time.Now()
	// Simulate the createDomainOnAgent failure path: hash computed, but
	// domainDispatched is NOT called. Needed must remain true.
	if !r.domainDispatchNeeded("d1", "h", now) {
		t.Fatal("precondition")
	}
	// (no domainDispatched call)
	if !r.domainDispatchNeeded("d1", "h", now.Add(time.Second)) {
		t.Fatal("a domain whose dispatch failed (never stamped) must stay needed")
	}
}
