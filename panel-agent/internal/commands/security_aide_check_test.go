package commands

// GH #2079: "Run check now" ran aide --check inside the agent call. A check
// takes minutes, longer than the panel's request may last, so the page got no
// answer (on .60, nothing in 90 s) while the check went on. It now starts
// jabali-aide-check.service, the unit of the daily check, and returns;
// security.aide.status says while it runs.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestAideCheck_StartsTheCheckUnit(t *testing.T) {
	cmds := aideUnitsExec(t, nil)

	out, err := mwAideCheckHandler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := out.(map[string]any); m["started"] != true {
		t.Fatalf("response %v, want started", out)
	}
	var started bool
	for _, c := range *cmds {
		if c == "systemctl start --no-block "+aideCheckUnit {
			started = true
		}
		if strings.Contains(c, "aide --") {
			t.Fatalf("ran aide inside the call: %q", c)
		}
	}
	if !started {
		t.Fatalf("commands %q, want the check unit started without waiting", *cmds)
	}
}

func TestAideCheck_RefusesWhileOneIsRunning(t *testing.T) {
	cmds := aideUnitsExec(t, map[string]string{aideCheckUnit: "activating"})

	_, err := mwAideCheckHandler(context.Background(), json.RawMessage(`{}`))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("err %v, want a failed-precondition agent error", err)
	}
	for _, c := range *cmds {
		if strings.HasPrefix(c, "systemctl start") {
			t.Fatalf("started a second check: %q", c)
		}
	}
}

func TestAideCheckStatus(t *testing.T) {
	cases := []struct {
		state   string
		wantNil bool
		running bool
		failed  bool
	}{
		{state: "inactive", wantNil: true},
		{state: "activating", running: true},
		{state: "active", running: true},
		{state: "failed", failed: true},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			aideUnitsExec(t, map[string]string{aideCheckUnit: tc.state})
			st := readAideCheckStatus(context.Background())
			if tc.wantNil {
				if st != nil {
					t.Fatalf("status %+v, want none", st)
				}
				return
			}
			if st == nil || st.Running != tc.running || st.Failed != tc.failed {
				t.Fatalf("status %+v, want running=%v failed=%v", st, tc.running, tc.failed)
			}
		})
	}
}

// aide refuses a second run while another holds the database (exit 21, seen on
// .60): a check started during a rebuild just fails. Each refuses, with a
// reason, while the other runs.
func TestAideCheckAndRebuild_DontRunTogether(t *testing.T) {
	withAideRebuildResult(t, nil)

	cmds := aideUnitsExec(t, map[string]string{agentwire.AideRebuildUnit: "active"})
	_, err := mwAideCheckHandler(context.Background(), json.RawMessage(`{}`))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition || !strings.Contains(ae.Message, "rebuild") {
		t.Fatalf("check during a rebuild: err %v, want a failed-precondition that names the rebuild", err)
	}
	for _, c := range *cmds {
		if strings.HasPrefix(c, "systemctl start") {
			t.Fatalf("started a check during a rebuild: %q", c)
		}
	}

	cmds = aideUnitsExec(t, map[string]string{aideCheckUnit: "activating"})
	_, err = mwAideRebuildHandler(context.Background(), json.RawMessage(`{}`))
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition || !strings.Contains(ae.Message, "check") {
		t.Fatalf("rebuild during a check: err %v, want a failed-precondition that names the check", err)
	}
	for _, c := range *cmds {
		if strings.HasPrefix(c, "systemd-run ") {
			t.Fatalf("started a rebuild during a check: %q", c)
		}
	}
}
