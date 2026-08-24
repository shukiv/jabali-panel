package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// JAB-387: the extraction wall-clock budget is enforced by the per-entry
// ex.ctx.Err() checks in untar/unzip (the handler feeds them a
// context.WithTimeout(extractWallClockBudget)). Prove the loop bails when that
// budget is already spent instead of extracting every entry.
func TestUntar_BailsWhenBudgetExpired(t *testing.T) {
	dir := t.TempDir()
	ex := testExtractor(dir)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond) // let the 1ns deadline pass
	ex.ctx = ctx

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < 5; i++ {
		_ = tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%d.txt", i), Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte("abc"))
	}
	_ = tw.Close()

	err := ex.untar(&buf)
	if err == nil {
		t.Fatal("untar must bail once the extract budget is exhausted, not run to completion")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("expected a deadline/cancelled error, got %v", err)
	}
}

// The budget constant is generous enough that legitimate extractions never hit
// it — a cheap guard so a future edit can't shrink it into flakiness.
func TestExtractWallClockBudget_Sane(t *testing.T) {
	if extractWallClockBudget < time.Minute {
		t.Fatalf("extract budget %v is too tight for legitimate large archives", extractWallClockBudget)
	}
}
