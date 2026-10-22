package commands

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// GH #2224: a learning chain counts and logs each new connection outside
// the allowlist once, not every packet of a connection it already let
// through, and still accepts everything.
func TestRenderEgressNFT_LearningCountsNewConnectionsOnly(t *testing.T) {
	always := func(string) bool { return true }
	users := []EgressUser{{Username: "carol", State: "learning", UID: 5001}}
	out := RenderEgressNFT(users, CanonicalDefaults(), nil, always)

	start := strings.Index(out, "chain user_carol_learning {")
	if start < 0 {
		t.Fatalf("no learning chain\n%s", out)
	}
	chain := out[start:]
	chain = chain[:strings.Index(chain, "\n  }")]
	established := strings.Index(chain, "    ct state != new accept\n")
	logLine := strings.Index(chain, `    limit rate 5/minute log prefix "jabali-egress-learn-carol " group 0`)
	counter := strings.Index(chain, "    counter name user_carol_drops accept")
	if established < 0 || logLine < 0 || counter < 0 {
		t.Fatalf("learning chain is missing a rule\n%s", chain)
	}
	if !(established < logLine && logLine < counter) {
		t.Fatalf("connections already let through must be accepted before the log and the counter\n%s", chain)
	}
}

// The enforced chain is unchanged: every packet outside the allowlist is
// dropped, whatever its conntrack state.
func TestRenderEgressNFT_EnforcedDropsEveryPacket(t *testing.T) {
	always := func(string) bool { return true }
	users := []EgressUser{{Username: "bob", State: "enforced", UID: 5002}}
	out := RenderEgressNFT(users, CanonicalDefaults(), nil, always)

	start := strings.Index(out, "chain user_bob_enforced {")
	if start < 0 {
		t.Fatalf("no enforced chain\n%s", out)
	}
	chain := out[start:]
	chain = chain[:strings.Index(chain, "\n  }")]
	if strings.Contains(chain, "ct state") {
		t.Fatalf("the enforced chain must not skip any conntrack state\n%s", chain)
	}
	if !strings.HasSuffix(chain, "    counter name user_bob_drops drop") {
		t.Fatalf("the enforced chain must end in the counted drop\n%s", chain)
	}
}

// The drops list shows what a learning policy let through (and would
// block once enforced) next to what an enforced policy dropped.
func TestEgressDrops_ListsLearningAndEnforcedLines(t *testing.T) {
	lines := strings.Join([]string{
		"2026-10-22T10:00:00+0000 host kernel: jabali-egress-drop-bob IN= OUT=eth0 SRC=10.0.0.1 DST=1.2.3.4 LEN=60 PROTO=TCP SPT=40000 DPT=3306",
		"2026-10-22T10:01:00+0000 host kernel: jabali-egress-learn-bob IN= OUT=eth0 SRC=10.0.0.1 DST=1.2.3.4 LEN=60 PROTO=TCP SPT=40001 DPT=3306",
		"2026-10-22T10:02:00+0000 host kernel: jabali-egress-learn-bob IN= OUT=eth0 SRC=10.0.0.1 DST=5.6.7.8 LEN=60 PROTO=TCP SPT=40002 DPT=8443",
		"2026-10-22T10:03:00+0000 host kernel: jabali-egress-learn-bob IN= OUT=eth0 SRC=10.0.0.1 DST=5.6.7.8 LEN=60 PROTO=TCP SPT=40003 DPT=8443",
		"2026-10-22T10:04:00+0000 host kernel: jabali-egress-learn-bobby IN= OUT=eth0 SRC=10.0.0.1 DST=9.9.9.9 LEN=60 PROTO=TCP SPT=40004 DPT=25",
	}, "\n") + "\n"
	var args []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, a ...string) *exec.Cmd {
		args = append([]string{name}, a...)
		return exec.CommandContext(ctx, "printf", "%s", lines)
	}
	t.Cleanup(func() { execCommandContext = prev })

	out, err := egressDropsHandler(context.Background(), json.RawMessage(`{"username":"bob"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); !strings.Contains(got, "-g jabali-egress-(drop|learn)-bob ") {
		t.Fatalf("journalctl must match both prefixes for this user, got %q", got)
	}
	resp := out.(egressDropsResponse)
	type row struct {
		dest  string
		port  int
		mode  string
		count int
	}
	var got []row
	for _, e := range resp.Events {
		got = append(got, row{e.Dest, e.Port, e.Mode, e.Count})
	}
	want := []row{
		{"5.6.7.8", 8443, "learning", 2},
		{"1.2.3.4", 3306, "learning", 1},
		{"1.2.3.4", 3306, "enforced", 1},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %+v, want %+v", got, want)
		}
	}
}
