package main

import (
	"os"
	"strings"
	"testing"
)

// JAB-214: the criticals mechanism already downgrades a run to DEGRADED and
// exits non-zero unless --allow-degraded. A failed domain create simply never
// fed it, so a migration that produced no vhost, no DNS zone and no mail
// routing still exited 0 with a green summary.
//
// This asserts the wiring exists. It is a source check because the property is
// "the caller promotes domain failures into criticals" — a mock of the restore
// would not notice if that loop were deleted.
func TestFailedDomainCreatePromotedToCriticals(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("migrate_run_cmd.go")
	if err != nil {
		t.Fatalf("read migrate_run_cmd.go: %v", err)
	}
	body := string(src)

	if !strings.Contains(body, "domainsRes.Failed") {
		t.Fatal("migrate_run_cmd.go never reads domainsRes.Failed — a domain that failed " +
			"to create will be reported as a skip and the run will exit 0 while the site " +
			"is unreachable and mail unroutable (JAB-214)")
	}
	if !strings.Contains(body, `p.criticals = append(p.criticals, "domain create failed: "`) {
		t.Error("domain create failures must be promoted to criticals — that is what " +
			"downgrades the run to DEGRADED and makes it exit non-zero")
	}
}
