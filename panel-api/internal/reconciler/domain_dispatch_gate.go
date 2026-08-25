package reconciler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// domainDispatchState is what each domain's domainDispatchCache entry holds.
// Hash covers the fully-assembled domain.create wire payload — everything the
// agent renders the vhost from. At is when we last dispatched.
type domainDispatchState struct {
	Hash string
	At   time.Time
}

// domainReDispatchInterval forces a domain.create even when the payload hash
// matches, so out-of-band drift (an operator hand-editing the vhost, a partial
// apply, an agent binary whose template changed without a panel restart) is
// still corrected on a bounded schedule. Same self-healing contract as
// dnsZoneReDispatchInterval / sshKeysReDispatchInterval / ftpAccountsReDispatchInterval.
const domainReDispatchInterval = 15 * time.Minute

// desiredDomainDispatchHash hashes the fully-assembled domain.create params so
// an unchanged domain skips the per-tick agent round-trip (the agent's own
// content compare already no-ops an unchanged vhost; this stops the panel
// asserting it ~60 times an hour per domain — JAB-369). params is exactly what
// the agent receives as JSON, so hashing its JSON encoding hashes what the agent
// acts on; json.Marshal sorts map keys, so the encoding is stable for a given
// payload. On a marshal error it returns "" and the caller never skips.
func desiredDomainDispatchHash(params map[string]any) string {
	b, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// domainDispatchNeeded reports whether domain.create should run this tick. True
// when the payload changed, when this PROCESS has not dispatched the domain yet,
// or when the self-heal interval elapsed.
//
// The cache is process-local (sync.Map on the Reconciler) by design: after a
// panel restart the cache is empty, so every domain is re-dispatched and the
// host re-converged; and a promoted DR standby can never inherit a stale
// "already applied on this host" decision from replicated database state,
// because that decision lives only in the previous primary's memory. An empty
// hash (marshal failure) always dispatches.
func (r *Reconciler) domainDispatchNeeded(domainID, hash string, now time.Time) bool {
	if hash == "" {
		return true
	}
	v, ok := r.domainDispatchCache.Load(domainID)
	if !ok {
		return true
	}
	st, okT := v.(domainDispatchState)
	if !okT {
		return true
	}
	if st.Hash != hash {
		return true
	}
	return now.Sub(st.At) >= domainReDispatchInterval
}

// domainDispatched records a SUCCESSFUL dispatch so the next tick can
// short-circuit. A failed domain.create is never recorded — the domain stays
// "dirty" and retries next tick, never stamped as applied. An empty hash is not
// recorded (the caller then never short-circuits on it).
func (r *Reconciler) domainDispatched(domainID, hash string, now time.Time) {
	if hash == "" {
		return
	}
	r.domainDispatchCache.Store(domainID, domainDispatchState{Hash: hash, At: now})
}
