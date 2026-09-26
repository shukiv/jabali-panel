package reconciler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

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

// domainDispatchNeeded reports whether a normal run would dispatch
// domain.create: the payload changed, this PROCESS has not dispatched the
// domain yet, or the drift-repair interval elapsed. An empty hash (marshal
// failure) always dispatches. The dispatch itself goes through phaseDecide,
// which also honours Audit and Force runs; this is the RunNormal view.
func (r *Reconciler) domainDispatchNeeded(domainID, hash string, now time.Time) bool {
	return r.ledger.decide(PhaseDomainVhost, domainID, hash, now, RunNormal) != decisionSkip
}

// domainDispatched records a SUCCESSFUL dispatch. A failed domain.create is
// never recorded, and an empty hash is never recorded.
func (r *Reconciler) domainDispatched(domainID, hash string, now time.Time) {
	r.ledger.stamp(PhaseDomainVhost, domainID, hash, now)
}
