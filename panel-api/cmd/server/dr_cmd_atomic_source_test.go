package main

import (
	"os"
	"strings"
	"testing"
)

// Source-pin: `jabali dr feed` provisions its DR feed schedule and destination
// link through the atomic CreateWithMemberships primitive, not a bare Create
// followed by a separate ReplaceDestinations. A link-write failure after a bare
// Create would leave a runnable, user-less DR feed with zero destinations; because
// findDRScheduleForDest matches by destination link, that orphan is invisible on
// the next run and a SECOND all-tenants+system feed gets stacked (JAB-307). There
// is no cobra harness for this command, so a body-scoped source-pin is the
// proportionate guard for the one-line primitive swap.
func TestDRFeed_ProvisionsScheduleAtomically_Source(t *testing.T) {
	raw, err := os.ReadFile("dr_cmd.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	s := string(raw)
	start := strings.Index(s, "func newDRFeedCmd(")
	if start < 0 {
		t.Fatal("newDRFeedCmd not found")
	}
	rest := s[start+len("func newDRFeedCmd("):]
	end := strings.Index(rest, "\nfunc newDRUnfeedCmd(")
	if end < 0 {
		t.Fatal("could not bound newDRFeedCmd body")
	}
	body := rest[:end]

	if !strings.Contains(body, "CreateWithMemberships(") {
		t.Error("dr feed must provision the schedule + destination link through CreateWithMemberships")
	}
	// The existing-feed upgrade legitimately calls schedRepo.Update; only the
	// create path must be atomic, so guard the two writes that were split.
	for _, bad := range []string{"schedRepo.Create(", "schedRepo.ReplaceDestinations("} {
		if strings.Contains(body, bad) {
			t.Errorf("dr feed still calls %s (row + link must commit atomically)", bad)
		}
	}
}
