package hostreserve

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReserveFloor(t *testing.T) {
	if got := ReserveFloor(40 << 30); got != 4<<30 {
		t.Fatalf("40 GiB fs: floor = %d, want 4 GiB (10%%)", got)
	}
	if got := ReserveFloor(1 << 40); got != 5<<30 {
		t.Fatalf("1 TiB fs: floor = %d, want 5 GiB cap", got)
	}
}

func TestCheckReserve_StatfsFailureDoesNotBlock(t *testing.T) {
	if err := CheckReserve("/nonexistent/definitely/not/here", 1<<40); err != nil {
		t.Fatalf("unstatable path must not veto: %v", err)
	}
}

func TestCheckReserve_ImpossibleNeedFails(t *testing.T) {
	// Asking for more bytes than any filesystem holds must trip the floor.
	if err := CheckReserve(t.TempDir(), 1<<62); err == nil {
		t.Fatal("absurd need passed the reserve check")
	}
}

func TestBudget_ConsumeAndExhaust(t *testing.T) {
	b := NewBudget(100)
	if err := b.Consume(60); err != nil {
		t.Fatalf("within budget: %v", err)
	}
	if got := b.Remaining(); got != 40 {
		t.Fatalf("remaining = %d, want 40", got)
	}
	if err := b.Consume(41); err == nil {
		t.Fatal("overrun did not error")
	}
	if got := b.Remaining(); got != 0 {
		t.Fatalf("remaining after overrun = %d, want 0 (never negative)", got)
	}
}

func TestBudgetWriter_StopsAtBudget(t *testing.T) {
	b := NewBudget(10)
	var sink bytes.Buffer
	w := b.Writer(&sink, "")
	if _, err := io.Copy(w, strings.NewReader(strings.Repeat("x", 8))); err != nil {
		t.Fatalf("within budget: %v", err)
	}
	_, err := io.Copy(w, strings.NewReader(strings.Repeat("x", 8)))
	if err == nil {
		t.Fatal("overrun write did not error")
	}
	if !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGuardedWriter_NoGuardsPassthrough(t *testing.T) {
	var sink bytes.Buffer
	w := GuardedWriter(&sink, "")
	n, err := w.Write([]byte("abc"))
	if err != nil || n != 3 || sink.String() != "abc" {
		t.Fatalf("passthrough broken: n=%d err=%v got=%q", n, err, sink.String())
	}
}

func TestKeyedSemaphore_PerKeyAndGlobal(t *testing.T) {
	s := NewKeyedSemaphore(1, 2)
	relA, ok := s.TryAcquire("a")
	if !ok {
		t.Fatal("first acquire for a refused")
	}
	if _, ok := s.TryAcquire("a"); ok {
		t.Fatal("second acquire for a exceeded per-key limit")
	}
	relB, ok := s.TryAcquire("b")
	if !ok {
		t.Fatal("first acquire for b refused")
	}
	if _, ok := s.TryAcquire("c"); ok {
		t.Fatal("third concurrent acquire exceeded global limit")
	}
	relA()
	relA() // double release must be a no-op, not a count corruption
	if _, ok := s.TryAcquire("c"); !ok {
		t.Fatal("slot not freed after release")
	}
	relB()
}

func TestBudgetWriter_ErrorIsNotWritten(t *testing.T) {
	b := NewBudget(5)
	var sink bytes.Buffer
	w := b.Writer(&sink, "")
	if _, err := w.Write([]byte("123456")); err == nil {
		t.Fatal("want error")
	} else if !errors.Is(err, err) { // shape check only
		t.Fatal("unreachable")
	}
	if sink.Len() != 0 {
		t.Fatalf("bytes written past budget: %q", sink.String())
	}
}
