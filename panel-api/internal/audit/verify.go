package audit

import "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"

// VerifyChain recomputes the hash chain to detect tampering (ADR-0106;
// powers `jabali audit verify`).
//
// rows MUST be the FULL set in total chain order (ts ASC, id ASC) —
// the same order the single-writer consumer sealed them in. Rows with
// a nil RowHash are pre-chain (M46 db_admin_audit fold-in /
// Redis-down-fallback rows the consumer hasn't sealed yet) and are
// skipped: they were never part of the chain, so they cannot break it.
//
// Returns the id of the first sealed row whose stored RowHash does not
// match the recomputation (tamper / corruption / a logic change to
// canonical()), how many sealed rows were verified, and ok.
//
// Uses the SAME computeRowHash the consumer seals with — the integrity
// guarantee is only as good as that function staying stable; a
// deliberate change to canonical()/computeRowHash is itself a chain
// break by design (old rows will fail verify) and must be versioned.
// startPrev is the chain root the recomputation begins from: "" for a full
// (never-pruned) log, or server_settings.audit_chain_anchor after a retention
// prune (JAB-105), so the surviving chain verifies against the pruned tail's
// last sealed hash.
func VerifyChain(rows []models.AuditEvent, startPrev string) (brokenID string, checked int, ok bool) {
	brokenID, checked, _, ok = VerifyChainBatch(rows, startPrev)
	return brokenID, checked, ok
}

// VerifyChainBatch verifies one contiguous slice of the chain and returns the
// hash to continue from, so a caller can walk the table in batches instead of
// loading all of it.
//
// The chain state is scalar (the previous row hash plus a count), which is why
// streaming is possible at all: the audit table is append-only and can reach
// hundreds of thousands of rows between prunes, and verify was the one
// endpoint that materialised every one of them at once — O(table) memory on a
// box the panel shares with everything else.
func VerifyChainBatch(rows []models.AuditEvent, startPrev string) (brokenID string, checked int, nextPrev string, ok bool) {
	prev := startPrev
	for i := range rows {
		r := &rows[i]
		if r.RowHash == nil {
			continue // pre-chain (folded/historical/fallback-pending)
		}
		if want := computeRowHash(prev, r); *r.RowHash != want {
			return r.ID, checked, prev, false
		}
		checked++
		prev = *r.RowHash
	}
	return "", checked, prev, true
}
