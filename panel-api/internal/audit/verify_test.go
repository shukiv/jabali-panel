package audit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// sealed builds a row sealed exactly as the consumer would (RowHash =
// computeRowHash(prev, row)), so VerifyChain must accept it.
func sealed(id string, tsNano int64, prev string) models.AuditEvent {
	e := models.AuditEvent{
		ID: id, TS: time.Unix(0, tsNano).UTC(),
		ActorKind: models.AuditActorSystem, Action: "act", Result: models.AuditResultOK,
	}
	h := computeRowHash(prev, &e)
	e.RowHash = &h
	if prev != "" {
		p := prev
		e.PrevHash = &p
	}
	return e
}

func TestVerifyChain_ValidChainPasses(t *testing.T) {
	r1 := sealed("01", 1, "")
	r2 := sealed("02", 2, *r1.RowHash)
	r3 := sealed("03", 3, *r2.RowHash)
	broken, checked, ok := VerifyChain([]models.AuditEvent{r1, r2, r3}, "")
	require.True(t, ok)
	require.Equal(t, "", broken)
	require.Equal(t, 3, checked)
}

func TestVerifyChain_TamperDetectedAtFirstBadRow(t *testing.T) {
	r1 := sealed("01", 1, "")
	r2 := sealed("02", 2, *r1.RowHash)
	r3 := sealed("03", 3, *r2.RowHash)
	bad := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	r2.RowHash = &bad // tamper
	broken, checked, ok := VerifyChain([]models.AuditEvent{r1, r2, r3}, "")
	require.False(t, ok)
	require.Equal(t, "02", broken, "first mismatching row id")
	require.Equal(t, 1, checked, "r1 verified before the break")
}

func TestVerifyChain_PreChainRowsSkipped(t *testing.T) {
	r1 := sealed("01", 1, "")
	pre := models.AuditEvent{ID: "fold", TS: time.Unix(0, 2).UTC(), ActorKind: "admin", Action: "db.admin.x", Result: "ok"} // RowHash nil (M46 fold-in / fallback-pending)
	r2 := sealed("02", 3, *r1.RowHash)                                                                                      // chained off r1, NOT off the nil row
	broken, checked, ok := VerifyChain([]models.AuditEvent{r1, pre, r2}, "")
	require.True(t, ok, "nil-RowHash rows are skipped, not chain breaks")
	require.Equal(t, "", broken)
	require.Equal(t, 2, checked, "only the 2 sealed rows counted")
}

// JAB-105: after pruning the chain tail, the surviving rows verify only when
// VerifyChain starts from the anchor (the last-pruned sealed row's hash) — not
// from genesis. This is the invariant the retention re-anchor relies on.
func TestVerifyChain_PrunedTailVerifiesFromAnchor(t *testing.T) {
	r1 := sealed("01", 1, "")
	r2 := sealed("02", 2, *r1.RowHash)
	r3 := sealed("03", 3, *r2.RowHash)

	// Prune r1 + r2. The anchor persisted by the prune = the newest deleted
	// sealed row's hash = r2.RowHash (which is r3.PrevHash).
	anchor := *r2.RowHash
	survivors := []models.AuditEvent{r3}

	// From genesis the pruned chain looks broken (r3's prev is not "").
	_, _, okGenesis := VerifyChain(survivors, "")
	require.False(t, okGenesis, "survivors must NOT verify from genesis after a tail prune")

	// From the anchor they verify cleanly.
	broken, checked, ok := VerifyChain(survivors, anchor)
	require.True(t, ok, "survivors verify from the anchor")
	require.Equal(t, "", broken)
	require.Equal(t, 1, checked)
}

// Verifying in batches must give byte-identical answers to verifying the
// whole slice at once. That equivalence is what lets the API stream the
// append-only table instead of loading every row into memory for one admin
// request.
func TestVerifyChainBatch_MatchesWholeSliceVerification(t *testing.T) {
	var rows []models.AuditEvent
	prev := ""
	for i := 1; i <= 25; i++ {
		r := sealed(string(rune('a'+i%26))+"row", int64(i), prev)
		prev = *r.RowHash
		rows = append(rows, r)
	}

	wantBroken, wantChecked, wantOK := VerifyChain(rows, "")

	// Fold the same rows in batches of 4, threading the chain state.
	gotPrev, gotChecked, gotOK, gotBroken := "", 0, true, ""
	for i := 0; i < len(rows); i += 4 {
		end := i + 4
		if end > len(rows) {
			end = len(rows)
		}
		b, n, next, ok := VerifyChainBatch(rows[i:end], gotPrev)
		gotChecked += n
		gotPrev = next
		if !ok {
			gotBroken, gotOK = b, false
			break
		}
	}
	require.Equal(t, wantOK, gotOK)
	require.Equal(t, wantBroken, gotBroken)
	require.Equal(t, wantChecked, gotChecked, "batched verification must check the same number of rows")
}

// A break mid-stream must be reported with the same row id, and the batched
// walk must be able to stop there rather than reading the rest of the table.
func TestVerifyChainBatch_ReportsBreakAndStops(t *testing.T) {
	r1 := sealed("01", 1, "")
	r2 := sealed("02", 2, *r1.RowHash)
	tampered := r2
	bad := "deadbeef"
	tampered.RowHash = &bad
	r3 := sealed("03", 3, *r2.RowHash)

	broken, checked, next, ok := VerifyChainBatch([]models.AuditEvent{r1, tampered, r3}, "")
	require.False(t, ok)
	require.Equal(t, "02", broken, "must name the first bad row")
	require.Equal(t, 1, checked, "only the rows before the break count as checked")
	require.Equal(t, *r1.RowHash, next, "chain state stops at the last good row")
}
