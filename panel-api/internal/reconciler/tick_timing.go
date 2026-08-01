package reconciler

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// tickTimings records coarse per-block durations of a ReconcileAll pass so
// an overrunning tick names the block that ate the budget instead of just
// "reconcile is slow". Checkpoints are cumulative: mark(name) attributes
// everything since the previous mark (or the start) to name.
type tickTimings struct {
	start time.Time
	last  time.Time
	names []string
	durs  []time.Duration
}

func newTickTimings() *tickTimings {
	now := time.Now()
	return &tickTimings{start: now, last: now}
}

func (t *tickTimings) mark(name string) {
	now := time.Now()
	t.names = append(t.names, name)
	t.durs = append(t.durs, now.Sub(t.last))
	t.last = now
}

func (t *tickTimings) total() time.Duration {
	return time.Since(t.start)
}

// summary renders the recorded blocks slowest-first, capped at top 5 so a
// log line stays readable: "domain_loop=42.1s post_sweeps=3.4s ...".
func (t *tickTimings) summary() string {
	type nd struct {
		name string
		dur  time.Duration
	}
	rows := make([]nd, len(t.names))
	for i := range t.names {
		rows[i] = nd{t.names[i], t.durs[i]}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].dur > rows[j].dur })
	if len(rows) > 5 {
		rows = rows[:5]
	}
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = fmt.Sprintf("%s=%s", r.name, r.dur.Round(time.Millisecond))
	}
	return strings.Join(parts, " ")
}
