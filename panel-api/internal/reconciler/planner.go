package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Reconciliation planner (JAB-369). The periodic tick rebuilds every
// resource's desired state from the database, but most of it has not changed
// since the last tick, so re-sending it to the Agent is wasted work. Each
// projection that can be skipped is a Phase. For a Phase, a caller builds the
// canonical desired specification of one resource, fingerprints it, and asks
// the ledger whether the Agent needs it this run. A successful apply stamps
// the fingerprint; a failed one never does, so the resource stays dirty and
// is retried on the next run.
//
// The ledger is process-local on purpose. A panel restart starts with an
// empty ledger and re-applies everything, and a promoted DR standby cannot
// inherit an "already applied on this host" decision from replicated
// database state, because that decision only ever lived in the previous
// primary's memory.

// RunMode selects how a run treats the ledger.
type RunMode int

const (
	// RunNormal skips a resource whose fingerprint is unchanged, until its
	// Phase's audit interval elapses; then it is re-applied to repair
	// out-of-band drift.
	RunNormal RunMode = iota
	// RunAudit re-applies every resource whether or not its fingerprint
	// changed, and stamps the result. It is a forced drift repair.
	RunAudit
	// RunForce ignores the ledger and applies everything. The admin
	// "reconcile (force)" action and DR promotion run in this mode.
	RunForce
)

func (m RunMode) String() string {
	switch m {
	case RunNormal:
		return "normal"
	case RunAudit:
		return "audit"
	case RunForce:
		return "force"
	default:
		return fmt.Sprintf("mode(%d)", int(m))
	}
}

// Phase is one fingerprinted projection. AuditInterval bounds how long an
// unchanged resource may go without being re-applied in a normal run.
type Phase struct {
	Name          string
	AuditInterval time.Duration
}

// The phases the ledger gates. The first four keep the intervals each pass
// used before the ledger existed. The rest used to re-send their projection
// on every tick and relied on the Agent to notice nothing changed.
var (
	PhaseDomainVhost = Phase{Name: "domain.vhost", AuditInterval: domainReDispatchInterval}
	PhaseDNSZone     = Phase{Name: "dns.zone", AuditInterval: dnsZoneReDispatchInterval}
	PhaseSSHKeys     = Phase{Name: "ssh.keys", AuditInterval: sshKeysReDispatchInterval}
	PhaseFTPAccounts = Phase{Name: "ftp.accounts", AuditInterval: ftpAccountsReDispatchInterval}

	// PhaseNginxRateLimits is the shared rate-limit zone fragment.
	PhaseNginxRateLimits = Phase{Name: "nginx.ratelimits", AuditInterval: domainReDispatchInterval}
	// PhaseDNSRecursor is one zone's pdns-recursor forwarder, added or
	// removed. Keyed by zone name, so an add and a removal of the same zone
	// share one entry and each replaces the other.
	PhaseDNSRecursor = Phase{Name: "dns.recursor", AuditInterval: dnsZoneReDispatchInterval}
	// PhaseWebmailVhost is one domain's mail.<domain> vhost, applied or
	// removed. Keyed by domain name, the Agent's own key for that vhost.
	PhaseWebmailVhost = Phase{Name: "webmail.vhost", AuditInterval: domainReDispatchInterval}
	// PhaseWebmailDaemon is the jabali-webmail unit's started-and-enabled
	// or stopped-and-disabled state. Its interval is shorter because it is
	// a liveness repair: it restarts a daemon someone stopped by hand.
	PhaseWebmailDaemon = Phase{Name: "webmail.daemon", AuditInterval: 5 * time.Minute}
	// PhasePHPPoolGC is the orphan FPM pool sweep. Its input is the set of
	// usernames to keep, so a deleted user re-runs it at once; orphans that
	// appear with no user change are swept within the interval.
	PhasePHPPoolGC = Phase{Name: "php.pool.gc", AuditInterval: domainReDispatchInterval}
)

// fingerprint is the canonical hash of a projection's desired payload:
// SHA-256 of its JSON encoding (encoding/json sorts map keys). It returns
// "" when v cannot be encoded, and the ledger always applies an empty
// fingerprint.
func fingerprint(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// decision is the ledger's answer for one resource in one run.
type decision int

const (
	// decisionSkip: the Agent already has this fingerprint and the audit
	// interval has not elapsed.
	decisionSkip decision = iota
	// decisionApply: the fingerprint is new or changed, or the run ignores
	// the ledger.
	decisionApply
	// decisionAudit: the fingerprint is unchanged but the audit interval
	// elapsed, or the run is an audit.
	decisionAudit
)

type ledgerKey struct {
	phase string
	id    string
}

// ledgerEntry is the last fingerprint the Agent accepted for one resource,
// and when.
type ledgerEntry struct {
	Hash string
	At   time.Time
}

// applyLedger records successful applies per (phase, resource). The zero
// value is ready to use.
type applyLedger struct {
	mu      sync.Mutex
	entries map[ledgerKey]ledgerEntry
}

func (l *applyLedger) lookup(p Phase, id string) (ledgerEntry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[ledgerKey{p.Name, id}]
	return e, ok
}

// decide reports what a run in mode should do with a resource whose desired
// fingerprint is hash. An empty hash (the spec could not be fingerprinted)
// always applies.
func (l *applyLedger) decide(p Phase, id, hash string, now time.Time, mode RunMode) decision {
	if mode == RunForce || hash == "" {
		return decisionApply
	}
	e, ok := l.lookup(p, id)
	if !ok || e.Hash != hash {
		return decisionApply
	}
	if mode == RunAudit || now.Sub(e.At) >= p.AuditInterval {
		return decisionAudit
	}
	return decisionSkip
}

// stamp records a successful apply. An empty hash is never recorded, so a
// resource that cannot be fingerprinted is never skipped.
func (l *applyLedger) stamp(p Phase, id, hash string, now time.Time) {
	if hash == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[ledgerKey]ledgerEntry{}
	}
	l.entries[ledgerKey{p.Name, id}] = ledgerEntry{Hash: hash, At: now}
}

// PhaseCounts is what one run did in one Phase.
type PhaseCounts struct {
	// Applied counts resources sent to the Agent because their fingerprint
	// was new or changed, or because the run ignores the ledger.
	Applied int
	// Audited counts unchanged resources re-sent for drift repair.
	Audited int
	// Skipped counts unchanged resources the run did not send.
	Skipped int
	// Failed counts resources whose apply failed; they stay dirty.
	Failed int
}

// Report is what one run did.
type Report struct {
	Mode   RunMode
	Took   time.Duration
	Phases map[string]PhaseCounts
}

// String renders the report on one line, phases sorted by name.
func (r Report) String() string {
	names := make([]string, 0, len(r.Phases))
	for n := range r.Phases {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		c := r.Phases[n]
		parts = append(parts, fmt.Sprintf("%s[applied=%d audited=%d skipped=%d failed=%d]",
			n, c.Applied, c.Audited, c.Skipped, c.Failed))
	}
	if len(parts) == 0 {
		return "no gated phases ran"
	}
	return strings.Join(parts, " ")
}

// runRecorder collects a run's counts. Phases run concurrently (the domain
// loop is a worker pool), so every update takes the lock. A nil recorder
// discards counts.
type runRecorder struct {
	mu      sync.Mutex
	mode    RunMode
	started time.Time
	phases  map[string]PhaseCounts
}

func newRunRecorder(mode RunMode) *runRecorder {
	return &runRecorder{mode: mode, started: time.Now(), phases: map[string]PhaseCounts{}}
}

func (rr *runRecorder) add(p Phase, apply func(*PhaseCounts)) {
	if rr == nil {
		return
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	c := rr.phases[p.Name]
	apply(&c)
	rr.phases[p.Name] = c
}

func (rr *runRecorder) report() Report {
	if rr == nil {
		return Report{Phases: map[string]PhaseCounts{}}
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	out := Report{Mode: rr.mode, Took: time.Since(rr.started), Phases: make(map[string]PhaseCounts, len(rr.phases))}
	for k, v := range rr.phases {
		out.Phases[k] = v
	}
	return out
}

type runCtxKey struct{}

// withRun starts a run in mode: the returned context carries the mode and a
// fresh recorder to every pass below it.
func withRun(ctx context.Context, mode RunMode) (context.Context, *runRecorder) {
	rr := newRunRecorder(mode)
	return context.WithValue(ctx, runCtxKey{}, rr), rr
}

// ensureRun keeps a run already carried by ctx, or starts one in mode. Entry
// points that can be called directly (the admin endpoint, tests) use it so
// their passes always see a mode and their tick log always has a report.
func ensureRun(ctx context.Context, mode RunMode) (context.Context, *runRecorder) {
	if rr, ok := ctx.Value(runCtxKey{}).(*runRecorder); ok && rr != nil {
		return ctx, rr
	}
	return withRun(ctx, mode)
}

// runFrom returns the run carried by ctx: its mode, and its recorder (nil
// when ctx carries no run, which counts nothing and behaves as RunNormal).
func runFrom(ctx context.Context) (RunMode, *runRecorder) {
	if rr, ok := ctx.Value(runCtxKey{}).(*runRecorder); ok && rr != nil {
		return rr.mode, rr
	}
	return RunNormal, nil
}

// phaseDecide asks the ledger what this run should do with one resource.
// force upgrades the run's mode to RunForce for this resource (ReconcileOne
// and the pool-regeneration pass always re-dispatch their domain). A skip is
// counted here; the caller reports the outcome of an apply with phaseApplied
// or phaseFailed.
func (r *Reconciler) phaseDecide(ctx context.Context, p Phase, id, hash string, now time.Time, force bool) decision {
	mode, rr := runFrom(ctx)
	if force {
		mode = RunForce
	}
	d := r.ledger.decide(p, id, hash, now, mode)
	if d == decisionSkip {
		rr.add(p, func(c *PhaseCounts) { c.Skipped++ })
	}
	return d
}

// phaseApplied stamps a successful apply and counts it.
func (r *Reconciler) phaseApplied(ctx context.Context, p Phase, id, hash string, now time.Time, d decision) {
	r.ledger.stamp(p, id, hash, now)
	_, rr := runFrom(ctx)
	rr.add(p, func(c *PhaseCounts) {
		if d == decisionAudit {
			c.Audited++
		} else {
			c.Applied++
		}
	})
}

// phaseFailed counts a failed apply. Nothing is stamped, so the resource is
// retried on the next run.
func (r *Reconciler) phaseFailed(ctx context.Context, p Phase) {
	_, rr := runFrom(ctx)
	rr.add(p, func(c *PhaseCounts) { c.Failed++ })
}

// project runs one fingerprinted projection through the ledger: apply is
// called unless the run may skip this resource, a success is stamped, and a
// failure is counted and left dirty for the next run. force applies
// whatever the run's mode. It reports whether apply ran and apply's error.
func (r *Reconciler) project(ctx context.Context, p Phase, id, hash string, force bool, apply func() error) (bool, error) {
	now := time.Now()
	d := r.phaseDecide(ctx, p, id, hash, now, force)
	if d == decisionSkip {
		return false, nil
	}
	if err := apply(); err != nil {
		r.phaseFailed(ctx, p)
		return true, err
	}
	r.phaseApplied(ctx, p, id, hash, now, d)
	return true, nil
}

// Run executes one planned reconcile pass in mode and reports what each
// gated phase did. RunNormal and RunAudit run the periodic pass
// (ReconcileAll); RunForce runs the full re-render (ReconcileAllForce).
func (r *Reconciler) Run(ctx context.Context, mode RunMode) (Report, error) {
	ctx, rr := withRun(ctx, mode)
	var err error
	if mode == RunForce {
		err = r.ReconcileAllForce(ctx)
	} else {
		err = r.ReconcileAll(ctx)
	}
	return rr.report(), err
}

// ScheduleResource requests an out-of-band reconcile of key. Like Schedule it
// never blocks and coalesces duplicates; the dirty state is kept even when a
// wake signal is already pending.
func (r *Reconciler) ScheduleResource(key ResourceKey) {
	r.markDirty(key)
}
