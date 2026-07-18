package egressops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-13: FlipMature graduates mature LEARNING policies to ENFORCED, honoring
// the operator pin and a dry-run preview mode. Same logic backs the CLI and the
// admin GUI, so these tests lock the shared semantics.

func learningRows(ids ...string) []models.UserEgressPolicy {
	out := make([]models.UserEgressPolicy, 0, len(ids))
	for _, id := range ids {
		out = append(out, models.UserEgressPolicy{UserID: id, State: models.UserEgressStateLearning})
	}
	return out
}

func TestFlipMature_PromotesEligible(t *testing.T) {
	pols := &fakePolicyRepo{mature: learningRows("u1", "u2")}
	res, err := FlipMature(context.Background(), pols, 7, "/nonexistent/pin", false)
	if err != nil {
		t.Fatalf("flip: %v", err)
	}
	if res.Pinned || res.DryRun {
		t.Fatalf("expected a real flip, got pinned=%v dryRun=%v", res.Pinned, res.DryRun)
	}
	if len(res.Flipped) != 2 || pols.upsertCalls != 2 {
		t.Fatalf("expected 2 flips + 2 upserts, got flipped=%v upserts=%d", res.Flipped, pols.upsertCalls)
	}
	if pols.upserted.State != models.UserEgressStateEnforced {
		t.Errorf("flipped row must be ENFORCED, got %q", pols.upserted.State)
	}
}

func TestFlipMature_DryRunWritesNothing(t *testing.T) {
	pols := &fakePolicyRepo{mature: learningRows("u1", "u2")}
	res, err := FlipMature(context.Background(), pols, 7, "/nonexistent/pin", true)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !res.DryRun || len(res.Eligible) != 2 {
		t.Fatalf("dry-run should list 2 eligible, got dryRun=%v eligible=%d", res.DryRun, len(res.Eligible))
	}
	if len(res.Flipped) != 0 || pols.upsertCalls != 0 {
		t.Errorf("dry-run must not write, got flipped=%v upserts=%d", res.Flipped, pols.upsertCalls)
	}
}

func TestFlipMature_OperatorPinBlocks(t *testing.T) {
	pinFile := filepath.Join(t.TempDir(), "per-user-egress.mode")
	if err := os.WriteFile(pinFile, []byte("learning\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pols := &fakePolicyRepo{mature: learningRows("u1")}
	res, err := FlipMature(context.Background(), pols, 7, pinFile, false)
	if err != nil {
		t.Fatalf("pinned flip: %v", err)
	}
	if !res.Pinned || res.PinValue != "learning" {
		t.Errorf("expected pinned=true value=learning, got %+v", res)
	}
	if pols.upsertCalls != 0 || len(res.Eligible) != 0 {
		t.Errorf("pin must no-op: upserts=%d eligible=%d", pols.upsertCalls, len(res.Eligible))
	}
}

func TestFlipMature_RejectsBadSoak(t *testing.T) {
	pols := &fakePolicyRepo{}
	if _, err := FlipMature(context.Background(), pols, 0, "/nonexistent/pin", true); err == nil {
		t.Fatal("soak_days=0 must error")
	}
}
