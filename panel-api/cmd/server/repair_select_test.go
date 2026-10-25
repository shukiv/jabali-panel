package main

import (
	"strings"
	"testing"
)

// allBroken is every known repair step reported broken.
func allBroken() []repairResult {
	var results []repairResult
	for _, s := range repairSteps() {
		results = append(results, repairResult{step: s, broken: true})
	}
	return results
}

func selectedIDs(steps []repairStep) map[string]bool {
	ids := map[string]bool{}
	for _, s := range steps {
		ids[s.id] = true
	}
	return ids
}

// GH #2266: the Repair Center's Auto-Repair runs `jabali repair --auto`.
// The docroot group/setgid step was marked destructive, so --auto skipped
// it and the row stayed Broken after the run; `--all --yes` fixed it.
func TestSelectRepairs_AutoFixesDocrootGroup(t *testing.T) {
	got := selectedIDs(selectRepairs(allBroken(), nil, false, false, true))
	if !got["docroot-www-data-group"] {
		t.Fatal("--auto must fix a broken docroot-www-data-group")
	}
}

// --auto still leaves the steps that rebuild or delete operator data to
// --all --yes or their own flag.
func TestSelectRepairs_AutoSkipsDestructive(t *testing.T) {
	got := selectedIDs(selectRepairs(allBroken(), nil, false, false, true))
	for _, id := range []string{"git-pointer", "node-modules", "orphan-migration-staging"} {
		if got[id] {
			t.Errorf("--auto selected destructive step %s", id)
		}
	}
	all := selectedIDs(selectRepairs(allBroken(), nil, false, true, false))
	for _, s := range repairSteps() {
		if !all[s.id] {
			t.Errorf("--all left out %s", s.id)
		}
	}
}

func TestSelectRepairs_SpecificAndHealthy(t *testing.T) {
	results := allBroken()
	for i := range results {
		if results[i].step.id == "git-ownership" {
			results[i].broken = false
		}
	}
	sel := map[string]bool{"git-ownership": true, "node-modules": true}
	got := selectedIDs(selectRepairs(results, sel, true, false, true))
	if len(got) != 1 || !got["node-modules"] {
		t.Fatalf("--<id> must fix only the named broken steps, got %v", got)
	}
}

// The Repair Center parses these markers to say which broken rows
// Auto-Repair leaves alone (RepairCard.tsx parseDiagnose).
func TestRepairMarkers(t *testing.T) {
	byID := map[string]repairStep{}
	for _, s := range repairSteps() {
		byID[s.id] = s
	}
	cases := map[string]string{
		"docroot-www-data-group": "",
		"git-pointer":            " [destructive]",
		"dirty-migration":        " [manual]",
		"fpm-masters-down":       " [manual]",
	}
	for id, want := range cases {
		s, ok := byID[id]
		if !ok {
			t.Fatalf("no repair step %s", id)
		}
		if got := repairMarkers(s); got != want {
			t.Errorf("repairMarkers(%s) = %q, want %q", id, got, want)
		}
	}
	for _, s := range repairSteps() {
		if strings.HasSuffix(strings.TrimSpace(s.label), "]") {
			t.Errorf("label of %s ends in a bracket, which reads as a marker: %q", s.id, s.label)
		}
	}
}
