package systemjobs

import (
	"regexp"
	"strings"
	"testing"
)

func TestCatalog_IDsUniqueAndUnitsWellFormed(t *testing.T) {
	idRe := regexp.MustCompile(`^[a-z0-9-]+$`)
	unitRe := regexp.MustCompile(`^[a-z0-9-]+\.(timer|service)$`)
	seen := map[string]bool{}
	for _, j := range All() {
		if seen[j.ID] {
			t.Errorf("duplicate id %q", j.ID)
		}
		seen[j.ID] = true
		if !idRe.MatchString(j.ID) {
			t.Errorf("%s: bad id", j.ID)
		}
		if !unitRe.MatchString(j.Timer) || !strings.HasSuffix(j.Timer, ".timer") {
			t.Errorf("%s: bad timer %q", j.ID, j.Timer)
		}
		if !unitRe.MatchString(j.Service) || !strings.HasSuffix(j.Service, ".service") {
			t.Errorf("%s: bad service %q", j.ID, j.Service)
		}
		if j.Label == "" || j.Description == "" || j.Category == "" {
			t.Errorf("%s: label, description and category are required", j.ID)
		}
	}
}

// Jobs another page runs with its own progress view, and a job that fires
// every 30 seconds, offer no manual run (reporter, GH #1686).
func TestCatalog_RunNowRefusedWhereNotMeaningful(t *testing.T) {
	for _, id := range []string{"os-updates", "panel-update", "sso-cleanup"} {
		j, ok := Lookup(id)
		if !ok {
			t.Fatalf("%s missing from catalog", id)
		}
		if j.RunNow {
			t.Errorf("%s must not allow Run now", id)
		}
	}
	for _, id := range []string{"os-updates", "panel-update"} {
		if j, _ := Lookup(id); j.ManagedBy != ManagedByUpdates {
			t.Errorf("%s must link to Updates, got %q", id, j.ManagedBy)
		}
	}
}

func TestLookup_Unknown(t *testing.T) {
	if _, ok := Lookup("jabali-panel"); ok {
		t.Fatal("unit-like names must not resolve; only catalog ids do")
	}
}

func TestStatusAndLastResult(t *testing.T) {
	cases := []struct {
		name       string
		s          State
		status     string
		lastResult string
	}{
		{"running", State{TimerActive: "active", ServiceActive: "activating", Result: "success", LastStartedAt: "x"}, StatusRunning, LastRunning},
		{"scheduled ok", State{TimerActive: "active", ServiceActive: "inactive", Result: "success", LastStartedAt: "x"}, StatusScheduled, LastSuccess},
		{"scheduled failed", State{TimerActive: "active", ServiceActive: "failed", Result: "exit-code", LastStartedAt: "x"}, StatusScheduled, LastFailed},
		{"never ran", State{TimerActive: "active", ServiceActive: "inactive", Result: "success"}, StatusScheduled, LastNever},
		{"disabled", State{TimerActive: "inactive", TimerEnabled: "disabled", ServiceActive: "inactive", Result: "success"}, StatusDisabled, LastNever},
	}
	for _, c := range cases {
		if got := c.s.Status(); got != c.status {
			t.Errorf("%s: Status() = %q, want %q", c.name, got, c.status)
		}
		if got := c.s.LastResult(); got != c.lastResult {
			t.Errorf("%s: LastResult() = %q, want %q", c.name, got, c.lastResult)
		}
	}
}

func TestDescribeSchedule(t *testing.T) {
	cases := []struct {
		cal   []string
		every string
		want  string
	}{
		{[]string{"*-*-* 04:30:00 UTC"}, "", "Daily at 04:30 UTC"},
		{[]string{"*-*-* 03:30:00"}, "", "Daily at 03:30"},
		{[]string{"Sun *-*-* 03:15:00"}, "", "Weekly on Sunday at 03:15"},
		{[]string{"*-*-* *:00/15:00"}, "", "Every 15 minutes"},
		{nil, "1h", "Every hour"},
		{nil, "30s", "Every 30 seconds"},
		{nil, "15min", "Every 15 minutes"},
		{[]string{"Mon..Fri *-*-* 09:00:00"}, "", "Mon..Fri *-*-* 09:00:00"},
		{nil, "", ""},
	}
	for _, c := range cases {
		if got := DescribeSchedule(c.cal, c.every); got != c.want {
			t.Errorf("DescribeSchedule(%q, %q) = %q, want %q", c.cal, c.every, got, c.want)
		}
	}
}

func TestClampLogLines(t *testing.T) {
	for in, want := range map[int]int{0: 200, -5: 200, 50: 50, 500: 500, 9000: 500} {
		if got := ClampLogLines(in); got != want {
			t.Errorf("ClampLogLines(%d) = %d, want %d", in, got, want)
		}
	}
}
