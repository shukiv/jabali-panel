package main

import (
	"testing"
)

func TestComputeBackfillPlan_AddsMissingZones(t *testing.T) {
	desired := map[string]bool{"alpha.com": true, "beta.com": true}
	actual := map[string]actualForwarder{}

	plan := computeBackfillPlan(desired, actual)

	if len(plan) != 2 {
		t.Fatalf("len=%d want 2. plan=%+v", len(plan), plan)
	}
	for _, p := range plan {
		if p.Action != "add" {
			t.Errorf("zone %s action=%q want 'add'", p.Zone, p.Action)
		}
		if p.Forwarder != "127.0.0.1:5300" {
			t.Errorf("zone %s forwarder=%q want 127.0.0.1:5300", p.Zone, p.Forwarder)
		}
	}
}

func TestComputeBackfillPlan_RemovesOrphans(t *testing.T) {
	desired := map[string]bool{}
	actual := map[string]actualForwarder{
		"stale.com": {Addr: "127.0.0.1", Port: 5300},
	}
	plan := computeBackfillPlan(desired, actual)

	if len(plan) != 1 || plan[0].Action != "remove" || plan[0].Zone != "stale.com" {
		t.Errorf("plan=%+v — want single remove for stale.com", plan)
	}
}

func TestComputeBackfillPlan_NoopForMatch(t *testing.T) {
	desired := map[string]bool{"existing.com": true}
	actual := map[string]actualForwarder{
		"existing.com": {Addr: "127.0.0.1", Port: 5300},
	}
	plan := computeBackfillPlan(desired, actual)

	if len(plan) != 1 || plan[0].Action != "noop" {
		t.Errorf("plan=%+v — want single noop", plan)
	}
}

func TestComputeBackfillPlan_UpdatesDrift(t *testing.T) {
	// Desired 127.0.0.1:5300, actual 127.0.0.1:9999 → needs update.
	desired := map[string]bool{"driftzone.com": true}
	actual := map[string]actualForwarder{
		"driftzone.com": {Addr: "127.0.0.1", Port: 9999},
	}
	plan := computeBackfillPlan(desired, actual)

	if len(plan) != 1 || plan[0].Action != "update" {
		t.Errorf("plan=%+v — want single update", plan)
	}
	if plan[0].Forwarder != "127.0.0.1:5300" {
		t.Errorf("update should target the canonical forwarder, got %q", plan[0].Forwarder)
	}
}

func TestComputeBackfillPlan_SortedByAction(t *testing.T) {
	// Mix of add, noop, remove. Add should come before noop before remove.
	desired := map[string]bool{
		"z-noop.com": true,
		"a-add.com":  true,
	}
	actual := map[string]actualForwarder{
		"z-noop.com":   {Addr: "127.0.0.1", Port: 5300},
		"m-remove.com": {Addr: "127.0.0.1", Port: 5300},
	}
	plan := computeBackfillPlan(desired, actual)

	if len(plan) != 3 {
		t.Fatalf("len=%d want 3. plan=%+v", len(plan), plan)
	}
	wantOrder := []string{"add", "noop", "remove"}
	for i, p := range plan {
		if p.Action != wantOrder[i] {
			t.Errorf("plan[%d].Action=%q, want %q (full plan=%+v)", i, p.Action, wantOrder[i], plan)
		}
	}
}

func TestCountNonNoop(t *testing.T) {
	plan := []recursorAction{
		{Action: "add"},
		{Action: "noop"},
		{Action: "remove"},
		{Action: "noop"},
		{Action: "update"},
	}
	if got := countNonNoop(plan); got != 3 {
		t.Errorf("countNonNoop=%d want 3 (add+remove+update)", got)
	}
}
