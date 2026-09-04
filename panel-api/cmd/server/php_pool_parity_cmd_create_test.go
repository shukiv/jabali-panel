package main

import (
	"os"
	"strings"
	"testing"
)

// TestPHPPoolCreate_RoutesThroughSharedLifecycle pins the JAB-360 parity: the
// CLI create + ini mutations must drive the SAME phppoolops module the HTTP
// handlers do, not a hand-copied reconcile. cmd/server has no DB/agent fixture,
// so this source-pins the wiring (repo precedent: the delete guard test above);
// the behavioural matrix is unit-tested in internal/phppoolops.
func TestPHPPoolCreate_RoutesThroughSharedLifecycle(t *testing.T) {
	src, err := os.ReadFile("php_pool_parity_cmd.go")
	if err != nil {
		t.Fatalf("read php_pool_parity_cmd.go: %v", err)
	}
	s := string(src)

	// Create must clamp/validate through the shared create-side validator with
	// admin-scoped caps — the operator CLI is privileged, but a CLI pool must
	// still obey the pm_max_children cap + FPM dynamic-mode invariants a
	// tenant-facing create enforces (the old CLI create wrote pm_max_children
	// raw, with no cap and no dynamic constraint).
	if !strings.Contains(s, "phppoolops.ResolveCreateTuning(true,") {
		t.Fatal("CLI create must validate/clamp via phppoolops.ResolveCreateTuning (admin-scoped) — a CLI pool must not exceed the caps/tuning a tenant create enforces (JAB-360)")
	}

	// Every mutation must reconcile via the shared op, which resolves the
	// versioned slug/additive + carries the full pm.* / slowlog / extensions /
	// Xdebug model. The old reconcilePHPPoolCLI omitted all of that, so a CLI
	// mutation on a non-default pool applied to the user's DEFAULT socket.
	if !strings.Contains(s, "phppoolops.ReconcileViaAgent(phpPoolReconcileDeps()") {
		t.Fatal("CLI mutations must reconcile via phppoolops.ReconcileViaAgent (versioned slug + full payload), not a hand-copied apply (JAB-360)")
	}
	if strings.Contains(s, "reconcilePHPPoolCLI") {
		t.Fatal("the stale hand-copied reconcilePHPPoolCLI must be gone — it had drifted from the HTTP reconcile (JAB-360)")
	}
}
