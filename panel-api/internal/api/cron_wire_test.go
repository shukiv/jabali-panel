package api

import (
	"encoding/json"
	"sort"
	"testing"
)

// TestCronAgentParamsWireShape guards against JSON-tag drift on the
// panel↔agent boundary. The panel-agent cron.* commands parse their
// params from these exact JSON keys; a rename here (or there) produces
// silent runtime validation failures that unit tests with mock agents
// do NOT catch (see memory: feedback_cross_boundary_contracts.md).
//
// If this test fails, change it AND the matching struct in
// panel-agent/internal/commands/cron_*.go together. Never one without
// the other.
//
// cron.apply / cron.remove canonical structs moved to internal/cronops
// (ADR-0101); their wire-shape guard lives in cronops_test.go.
func TestCronAgentParamsWireShape(t *testing.T) {
	cases := []struct {
		name    string
		payload any
		want    []string
	}{
		{
			name:    "cron.remove",
			payload: cronRemoveAgentParams{UserID: "u", Username: "shuki", JobID: "j"},
			want:    []string{"job_id", "user_id", "username"},
		},
		{
			name:    "cron.run_now",
			payload: cronRunNowAgentParams{UserID: "u", Username: "shuki", JobID: "j", Command: "php /home/u/public_html/wp-cron.php", OwnedDocroots: []string{"/home/u/public_html"}},
			want:    []string{"command", "job_id", "owned_docroots", "user_id", "username"},
		},
		{
			name:    "cron.tail_log_with_lines",
			payload: cronTailLogAgentParams{UserID: "u", Username: "shuki", JobID: "j", Lines: 100},
			want:    []string{"job_id", "lines", "user_id", "username"},
		},
		{
			name:    "cron.tail_log_omits_zero_lines",
			payload: cronTailLogAgentParams{UserID: "u", Username: "shuki", JobID: "j"},
			want:    []string{"job_id", "user_id", "username"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := make([]string, 0, len(m))
			for k := range m {
				got = append(got, k)
			}
			sort.Strings(got)
			if len(got) != len(tc.want) {
				t.Fatalf("wrong key count: got %v want %v", got, tc.want)
			}
			for i, k := range got {
				if k != tc.want[i] {
					t.Fatalf("key[%d]: got %q want %q (full got=%v want=%v)", i, k, tc.want[i], got, tc.want)
				}
			}
		})
	}
}
