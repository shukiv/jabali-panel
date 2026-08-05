package api

// GH #928: ITFlow's 2026-08 stable makes cron.php a dispatcher — the ONLY
// crontab entry, run every minute. Guard against re-introducing the old
// per-job crons (mail_queue.php / ticket_email_parser.php), which the
// dispatcher now invokes itself.

import "testing"

func TestITFlowCronSpec_SingleDispatcher(t *testing.T) {
	if len(itflowCronSpec) != 1 {
		t.Fatalf("itflowCronSpec has %d entries, want 1 (cron.php dispatcher)", len(itflowCronSpec))
	}
	c := itflowCronSpec[0]
	if c.file != "cron.php" {
		t.Errorf("cron file = %q, want cron.php", c.file)
	}
	if c.schedule != "* * * * *" {
		t.Errorf("cron schedule = %q, want every minute", c.schedule)
	}
}
