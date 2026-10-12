package commands

// AIDE replaces the report log on every run (report_url=file:), so the log
// holds one report: the summary at the top, then every changed path, then the
// details. The status read only the last 4 MiB of the log, so for a report
// bigger than that it found no summary and no entries, and said 0 changes: the
// page said no tamper was detected, and aide.tamper.detected never fired. On
// .60 a check with 148k changes made a 19.5 MB report and read as clean.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bigAideReport is a check report over 4 MiB in the AIDE 0.18 layout.
func bigAideReport(start string, added int) string {
	var b strings.Builder
	b.WriteString("Entry /usr/lib/firmware/x.zst in databases has different attributes: +sha256\n")
	fmt.Fprintf(&b, "Start timestamp: %s (AIDE 0.18.6)\nAIDE found differences between database and filesystem!!\n\n", start)
	fmt.Fprintf(&b, "Summary:\n  Total number of entries:\t%d\n  Added entries:\t\t%d\n  Removed entries:\t\t0\n  Changed entries:\t\t1\n\n", added+1, added)
	b.WriteString("---------------------------------------------------\nAdded entries:\n---------------------------------------------------\n\n")
	for i := 0; i < added; i++ {
		fmt.Fprintf(&b, "f+++++++++++++++++: /usr/lib/modules/6.8.0-146-generic/kernel/m%06d.ko.zst\n", i)
	}
	b.WriteString("\n---------------------------------------------------\nChanged entries:\n---------------------------------------------------\n\n")
	b.WriteString("f = ... mci.H     : /etc/apparmor.d/jabali-fpm-app\n")
	b.WriteString("\n---------------------------------------------------\nDetailed information about changes:\n---------------------------------------------------\n\n")
	for i := 0; i < added; i++ {
		fmt.Fprintf(&b, "File: /usr/lib/modules/6.8.0-146-generic/kernel/m%06d.ko.zst\n SHA256    : AfqH6LADPbuifJuAaY35fUBnlGLhTbnq | ehZBfZ6nqB4AfqH6LADPbuifJuAaY35f\n\n", i)
	}
	fmt.Fprintf(&b, "\nEnd timestamp: %s (run time: 2m 38s)\n", start)
	return b.String()
}

func statusFromReportLog(t *testing.T, content string) aideStatusResponse {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aide.report.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := readLastAideReport(path)
	if err != nil {
		t.Fatal(err)
	}
	resp := aideStatusResponse{Sample: []aideSampleRow{}}
	applyAideReport(data, &resp)
	return resp
}

func TestAideStatus_ABigReportIsNotClean(t *testing.T) {
	report := bigAideReport("2026-10-12 14:55:54 +0000", 40000)
	if len(report) < 5<<20 {
		t.Fatalf("fixture is %d bytes, want over 4 MiB", len(report))
	}
	resp := statusFromReportLog(t, report)
	if resp.Summary.Added != 40000 || resp.Summary.Changed != 1 {
		t.Fatalf("summary %+v, want 40000 added and 1 changed", resp.Summary)
	}
	if !strings.HasPrefix(resp.LastCheckTS, "2026-10-12 14:55:54") {
		t.Fatalf("last_check_ts %q, want the check's start", resp.LastCheckTS)
	}
	if resp.Categories["security-control"] != 1 {
		t.Fatalf("categories %v, want the AppArmor profile counted", resp.Categories)
	}
}

// A log that holds more than one report (the daily unit also appends aide's
// stdout to it) is read from the last one.
func TestAideStatus_TheLastReportCounts(t *testing.T) {
	old := bigAideReport("2026-10-11 04:30:00 +0000", 30000)
	resp := statusFromReportLog(t, old+"Start timestamp: 2026-10-12 04:30:00 +0000 (AIDE 0.18.6)\nAIDE found NO differences between database and filesystem. Looks okay!!\n\nNumber of entries:\t150552\n\nEnd timestamp: 2026-10-12 04:32:00 +0000 (run time: 2m 0s)\n")
	if total := resp.Summary.Added + resp.Summary.Changed + resp.Summary.Removed; total != 0 {
		t.Fatalf("summary %+v, want the last report's (none)", resp.Summary)
	}
	if !strings.HasPrefix(resp.LastCheckTS, "2026-10-12 04:30:00") {
		t.Fatalf("last_check_ts %q, want the last report's", resp.LastCheckTS)
	}
}
