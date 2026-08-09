package commands

import (
	"context"
	"strings"
	"testing"
	"time"
)

// dline builds a Stalwart delivery-tracer log line parseMailLogLine understands.
func dline(ts time.Time, event, from string, to []string, qid string) string {
	quoted := make([]string, len(to))
	for i, r := range to {
		quoted[i] = `"` + r + `"`
	}
	return ts.UTC().Format(time.RFC3339) + ` INFO msg (` + event + `) queueId = ` + qid +
		`, from = "` + from + `", to = [` + strings.Join(quoted, ", ") + `], size = 10, total = 1`
}

func runDomainSample(t *testing.T, since time.Time, local []string) map[string]int64 {
	t.Helper()
	resp, err := mailStatsDomainSampleHandler(context.Background(),
		mustJSON(t, map[string]any{"since": since.UTC().Format(time.RFC3339), "local_domains": local}))
	if err != nil {
		t.Fatalf("handler err: %v", err)
	}
	out := map[string]int64{}
	for _, c := range resp.(mailStatsDomainResponse).Counts {
		out[c.Metric+"|"+c.Domain] = c.Count
	}
	return out
}

func TestMailStatsDomain_AttributesSentReceivedDeliveredFailed(t *testing.T) {
	now := time.Now().UTC()
	ts := now.Add(-1 * time.Hour) // inside the 48h lookback + the window
	lines := strings.Join([]string{
		// local -> local: sent for a.test, received for b.test
		dline(ts, "queue.message-queued", "u@a.test", []string{"v@b.test"}, "1"),
		// inbound from remote -> local: received for b.test only (remote sender not counted)
		dline(ts, "queue.message-queued", "ext@remote.example", []string{"v@b.test"}, "2"),
		// outbound to remote: sent for a.test only (remote recipient not counted)
		dline(ts, "queue.message-queued", "u@a.test", []string{"x@remote.example"}, "3"),
		// terminal events
		dline(ts, "delivery.completed", "u@a.test", []string{"v@b.test"}, "4"),
		dline(ts, "delivery.failed", "ext@remote.example", []string{"v@b.test"}, "5"),
	}, "\n") + "\n"
	withMailLogDir(t, "delivery.now", lines)

	got := runDomainSample(t, now.Add(-2*time.Hour), []string{"a.test", "b.test"})
	want := map[string]int64{
		"sent|a.test":      2, // qid 1 + qid 3
		"received|b.test":  2, // qid 1 + qid 2
		"delivered|b.test": 1, // qid 4
		"failed|b.test":    1, // qid 5
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (all: %v)", k, got[k], v, got)
		}
	}
	// remote.example must never appear (not a local domain).
	for k := range got {
		if strings.Contains(k, "remote.example") {
			t.Errorf("remote domain leaked into counts: %s", k)
		}
	}
}

func TestMailStatsDomain_DedupesMultiRecipientSameDomain(t *testing.T) {
	now := time.Now().UTC()
	ts := now.Add(-30 * time.Minute)
	// two recipients in the SAME local domain on one message => received counts once.
	line := dline(ts, "queue.message-queued", "ext@remote.example", []string{"a@b.test", "b@b.test"}, "77") + "\n"
	withMailLogDir(t, "delivery.now", line)

	got := runDomainSample(t, now.Add(-1*time.Hour), []string{"b.test"})
	if got["received|b.test"] != 1 {
		t.Errorf("received|b.test = %d, want 1 (dedup by queueId+domain); all: %v", got["received|b.test"], got)
	}
}

func TestMailStatsDomain_ExcludesLinesBeforeWindow(t *testing.T) {
	now := time.Now().UTC()
	old := dline(now.Add(-5*time.Hour), "queue.message-queued", "u@a.test", []string{"v@a.test"}, "9")
	fresh := dline(now.Add(-10*time.Minute), "queue.message-queued", "u@a.test", []string{"v@a.test"}, "10")
	withMailLogDir(t, "delivery.now", old+"\n"+fresh+"\n")

	got := runDomainSample(t, now.Add(-1*time.Hour), []string{"a.test"}) // window starts 1h ago
	if got["sent|a.test"] != 1 {
		t.Errorf("sent|a.test = %d, want 1 (only the fresh line is inside the window); all: %v", got["sent|a.test"], got)
	}
}
