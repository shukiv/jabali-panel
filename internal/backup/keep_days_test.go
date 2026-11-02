package backup

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

func TestMaxKeepWithinDays(t *testing.T) {
	for _, c := range []struct {
		max     uint32
		cadence string
		want    int
	}{
		{7, CadenceDaily, 7},
		{28, CadenceEvery6h, 7},
		{7, CadenceEvery6h, 1}, // 2 days would be 8 backups
		{30, CadenceEvery12h, 15},
		{7, CadenceHourly, 1}, // a day is 24 backups; at least 1
		{4, CadenceWeekly, 28},
		{0, CadenceDaily, 1},
	} {
		if got := MaxKeepWithinDays(c.max, CadenceRunsPerWeek(c.cadence)); got != c.want {
			t.Errorf("%d backups, %s: max %d days, want %d", c.max, c.cadence, got, c.want)
		}
	}
}

func TestCronRunsPerWeek(t *testing.T) {
	from := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for expr, want := range map[string]int{
		"0 3 * * *":    7,
		"29 */6 * * *": 28,
		"0 3 * * 1":    1,
		"0 * * * *":    168,
	} {
		got, err := CronRunsPerWeek(expr, from)
		if err != nil || got != want {
			t.Errorf("%q: %d runs a week (err %v), want %d", expr, got, err, want)
		}
	}
	if _, err := CronRunsPerWeek("not a cron", from); err == nil {
		t.Error("an invalid expression must be an error")
	}
}

// The SPA shows the most days to keep and how many backups they make from its
// own copy of the table; it must match the API's, which clamps.
func TestCadenceRunsPerWeek_MatchesSPA(t *testing.T) {
	const tsPath = "../../panel-ui/src/lib/backupScheduleOptions.ts"
	src, err := os.ReadFile(tsPath)
	if err != nil {
		t.Fatalf("read %s: %v", tsPath, err)
	}
	block := regexp.MustCompile(`(?s)BACKUP_CADENCE_RUNS_PER_WEEK[^{]*\{(.*?)\n\}`).FindSubmatch(src)
	if block == nil {
		t.Fatalf("%s: BACKUP_CADENCE_RUNS_PER_WEEK not found", tsPath)
	}
	ts := map[string]int{}
	for _, m := range regexp.MustCompile(`(\w+):\s*(\d+)`).FindAllSubmatch(block[1], -1) {
		n, _ := strconv.Atoi(string(m[2]))
		ts[string(m[1])] = n
	}
	cadences := []string{CadenceHourly, CadenceEvery6h, CadenceEvery12h, CadenceDaily, CadenceWeekly}
	if len(ts) != len(cadences) {
		t.Errorf("SPA table has %d cadences, want %d: %v", len(ts), len(cadences), ts)
	}
	for _, c := range cadences {
		if ts[c] != CadenceRunsPerWeek(c) {
			t.Errorf("%s: SPA %d runs a week, API %d", c, ts[c], CadenceRunsPerWeek(c))
		}
	}
}
