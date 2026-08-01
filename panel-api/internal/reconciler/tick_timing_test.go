package reconciler

import (
	"strings"
	"testing"
	"time"
)

func TestTickTimingsSummarySlowestFirstCapped(t *testing.T) {
	tt := newTickTimings()
	base := tt.start
	// Simulate 7 blocks with distinct durations by driving `last` manually.
	durations := []time.Duration{
		5 * time.Millisecond, 300 * time.Millisecond, 20 * time.Millisecond,
		900 * time.Millisecond, 50 * time.Millisecond, 10 * time.Millisecond,
		700 * time.Millisecond,
	}
	names := []string{"a", "b", "c", "d", "e", "f", "g"}
	cursor := base
	for i, d := range durations {
		cursor = cursor.Add(d)
		tt.names = append(tt.names, names[i])
		tt.durs = append(tt.durs, d)
	}

	sum := tt.summary()
	parts := strings.Fields(sum)
	if len(parts) != 5 {
		t.Fatalf("summary must cap at 5 blocks, got %d: %q", len(parts), sum)
	}
	if !strings.HasPrefix(parts[0], "d=") || !strings.HasPrefix(parts[1], "g=") || !strings.HasPrefix(parts[2], "b=") {
		t.Fatalf("summary not sorted slowest-first: %q", sum)
	}
	if strings.Contains(sum, "a=") || strings.Contains(sum, "f=") {
		t.Fatalf("summary must drop the fastest blocks beyond top 5: %q", sum)
	}
}

func TestTickTimingsMarkAttributesElapsedToName(t *testing.T) {
	tt := newTickTimings()
	tt.mark("first")
	if len(tt.names) != 1 || tt.names[0] != "first" {
		t.Fatalf("mark did not record name: %+v", tt.names)
	}
	if tt.durs[0] < 0 {
		t.Fatalf("negative duration recorded: %v", tt.durs[0])
	}
	if tt.last.Before(tt.start) {
		t.Fatalf("last checkpoint moved backwards")
	}
}
