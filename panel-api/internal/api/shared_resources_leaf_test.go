package api

import (
	"os"
	"strings"
	"testing"
)

// TestSharedResourceHTTP_RoutesThroughLeaf source-pins that the REST create
// handler routes through sharedresourceops rather than hand-building + persisting
// the row itself — so the create policy has one owner shared with the operator
// CLI (JAB-339 AC3). (The separate rename handler still fires sharedresource.apply
// directly; that is a different operation, out of this create-only slice.)
func TestSharedResourceHTTP_RoutesThroughLeaf(t *testing.T) {
	b, err := os.ReadFile("shared_resources.go")
	if err != nil {
		t.Fatalf("read shared_resources.go: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "sharedresourceops.Create(") {
		t.Error("create handler must call sharedresourceops.Create")
	}
	if strings.Contains(src, "h.cfg.Resources.Create(") {
		t.Error("create handler must not persist the row itself")
	}
}
