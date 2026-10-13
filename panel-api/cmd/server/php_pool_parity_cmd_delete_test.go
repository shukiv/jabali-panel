package main

import (
	"os"
	"strings"
	"testing"
)

// TestPHPPoolDelete_GuardsBoundDomainsAndRemovesHostPool pins the JAB-342 fix.
// The CLI `php pool delete` deleted the pool + ini rows only: it never checked
// whether any domain still bound the pool (the HTTP handler returns 409
// pool_has_bound_domains) and never removed the live FPM pool on the host (the
// HTTP handler calls php.pool.remove). The first left domains pointing at a
// deleted pool (dangling binding); the second left the pool's socket + config
// orphaned under the FPM tree. cmd/server has no DB/agent fixture, so this
// source-pins both guards (repo precedent: log_cmd_scope_test.go).
func TestPHPPoolDelete_GuardsBoundDomainsAndRemovesHostPool(t *testing.T) {
	src, err := os.ReadFile("php_pool_parity_cmd.go")
	if err != nil {
		t.Fatalf("read php_pool_parity_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "CountByPHPPoolID(ctx, pool.ID)") {
		t.Fatal("pool delete must count bound domains before deleting — deleting under bound domains leaves dangling bindings (JAB-342)")
	}
	if !strings.Contains(s, "bound > 0") {
		t.Fatal("pool delete must refuse when domains are still bound (JAB-342)")
	}
	if !strings.Contains(s, `"php.pool.remove"`) {
		t.Fatal("pool delete must remove the live FPM pool on the host via php.pool.remove, not just the DB rows (JAB-342)")
	}
	// GH #329: without a slug php.pool.remove removes the user's DEFAULT pool,
	// so deleting a per-version pool took the default one down instead.
	if !strings.Contains(s, "phppoolops.RemoveSlug(ctx, poolRepo, *owner.Username, pool)") ||
		!strings.Contains(s, `"slug": slug`) {
		t.Fatal("pool delete must tell php.pool.remove which pool (its slug), resolved before the row is deleted")
	}
	if strings.Index(s, "phppoolops.RemoveSlug(") > strings.Index(s, "poolRepo.Delete(ctx, pool.ID)") {
		t.Fatal("the slug must be resolved before the pool row is deleted")
	}
}
