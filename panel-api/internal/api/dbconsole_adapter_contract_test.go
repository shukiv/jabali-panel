package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbconsoleops"
)

// JAB-348 AC4 — DB-Console SSO adapter contract matrix.
//
// The audit that opened JAB-348 warned that shadow selection, token issuance,
// engine/scope encoding, redirect construction, and audit emission are
// duplicated across the tenant doors (sso_phpmyadmin.go, sso_adminer.go), the
// privileged admin-all doors (databases_admin_ops.go), and the operator CLI
// (cmd/server/db_sso_cmd.go) — "duplicated issuance logic is where scope/engine
// encoding can silently drift between adapters." The dbconsoleops package (#1821)
// is the single encoding authority every adapter is meant to route through. This
// matrix is the drift guard.
//
// WHAT THE CONTRACT COVERS (the issuance tail): engine normalization, shadow
// dispatch, the mint<->redirect pairing, the issuance audit taxonomy, and the
// prefix-never-token audit rule. Pre-issuance gates (no session, cross-origin,
// bad JSON, db-not-found, owner-mismatch, engine-mismatch) are ADR-0083
// adapter-local authorization concerns — engine.go's header and #1823 keep them
// OUTSIDE the issuance taxonomy on purpose, so they are NOT drift and are not
// asserted here.
//
// HOW IT ANCHORS: the encoding assertions compare each door's real output to the
// leaf builder fed the door's OWN base URL and the token it actually minted
// (dbconsoleops.PhpMyAdminRedirect / AdminerRedirect). A door that hand-builds a
// URL instead of calling the leaf diverges from that anchor and reddens — the
// slice-1/JAB-318 bug shape, applied to scope encoding.
//
// COVERAGE SHAPE (an honest, deliberate consequence of the current wiring):
//   - Privileged doors take the mint surface as an INTERFACE (adminTokenMinter /
//     adminerTokenMinter), so they are driven behaviourally here with a recording
//     fake minter.
//   - Tenant doors hold a CONCRETE *sso.Service / *sso.AdminerService (no mint
//     seam), and the CLI has no injection seam at all, so those adapters are
//     pinned to the leaf by source delegation instead. Their issuance BEHAVIOUR is
//     already covered elsewhere: #1821 pairs the tenant mint<->redirect, and
//     sso_phpmyadmin_taxonomy_test.go pins the tenant taxonomy. The unified
//     behavioural matrix a single fake minter would give is blocked by exactly
//     that interface-vs-concrete asymmetry — which is the AC1 "one deep module"
//     work JAB-348 still tracks, called out in the PR body.

// contractAdminMinter / contractAdminerMinter satisfy the privileged doors' mint
// interfaces, record the scope they were called with, and can be scripted to
// fail so both audit outcomes are exercised.
type contractAdminMinter struct {
	token   string
	err     error
	gotDBID string
	gotDB   string
}

func (m *contractAdminMinter) MintToken(_ context.Context, _, databaseID, dbName string) (string, error) {
	m.gotDBID, m.gotDB = databaseID, dbName
	if m.err != nil {
		return "", m.err
	}
	return m.token, nil
}

type contractAdminerMinter struct {
	token   string
	err     error
	gotDBID string
	gotEng  string
}

func (m *contractAdminerMinter) MintAdminerToken(_ context.Context, _, databaseID, engine string) (string, error) {
	m.gotDBID, m.gotEng = databaseID, engine
	if m.err != nil {
		return "", m.err
	}
	return m.token, nil
}

func lastAudit(t *testing.T, fa *fakeDBAdmin) (outcome, target, detail string) {
	t.Helper()
	if len(fa.audits) == 0 {
		t.Fatal("expected an audit row, got none")
	}
	a := fa.audits[len(fa.audits)-1]
	return a.Outcome, a.Target, a.Detail
}

// TestDBConsoleContract_PrivilegedEncodingAnchoredToLeaf drives both privileged
// admin-all doors and asserts each redirect is byte-identical to the leaf builder
// fed the door's own base URL and the token it minted, and that the scope encoded
// into the mint is the admin-all sentinel. This is the AC3/AC4 "scope+engine
// encoded identically across adapters" invariant, proven behaviourally for the
// two doors that expose a mint seam.
func TestDBConsoleContract_PrivilegedEncodingAnchoredToLeaf(t *testing.T) {
	t.Run("phpmyadmin admin-all -> PhpMyAdminRedirect(token, db='')", func(t *testing.T) {
		minter := &contractAdminMinter{token: "PMA-TOK"}
		fa := &fakeDBAdmin{}
		h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
			Agent: stubAgent{}, DBAdmin: fa, SSO: minter, Log: slog.Default(),
		}}
		w, c := adminSSORequest("https://example.com")
		h.ssoPhpMyAdminAdmin(c)

		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200: %s", w.Code, w.Body.String())
		}
		var resp ssoRedirectResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response: %v", err)
		}
		want := dbconsoleops.PhpMyAdminRedirect(panelBaseURL(c), minter.token, "")
		if resp.RedirectURL != want {
			t.Errorf("redirect = %q, want leaf output %q — the door must build its URL through dbconsoleops, not inline", resp.RedirectURL, want)
		}
		if minter.gotDBID != ssoAdminAllSentinel || minter.gotDB != "" {
			t.Errorf("minted dbID=%q db=%q, want admin-all sentinel + empty scope", minter.gotDBID, minter.gotDB)
		}
		// prefix-never-token: the token is the deliverable in the redirect, but
		// the audit trail must never carry the raw token material.
		if _, _, detail := lastAudit(t, fa); strings.Contains(detail, minter.token) {
			t.Errorf("audit detail must not carry the raw token %q: %q", minter.token, detail)
		}
	})

	t.Run("adminer admin-all -> AdminerRedirect(token, db='', engine=postgres)", func(t *testing.T) {
		minter := &contractAdminerMinter{token: "ADM-TOK"}
		fa := &fakeDBAdmin{}
		h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
			DBAdmin: fa, AdminerSSO: minter, Log: slog.Default(),
		}}
		w, c := adminSSORequest("https://example.com")
		h.ssoAdminerAdmin(c)

		if w.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200: %s", w.Code, w.Body.String())
		}
		var resp ssoRedirectResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response: %v", err)
		}
		want := dbconsoleops.AdminerRedirect(panelBaseURL(c), minter.token, "", "postgres")
		if resp.RedirectURL != want {
			t.Errorf("redirect = %q, want leaf output %q", resp.RedirectURL, want)
		}
		if minter.gotDBID != ssoAdminAllSentinel || minter.gotEng != "postgres" {
			t.Errorf("minted dbID=%q engine=%q, want admin-all sentinel + postgres", minter.gotDBID, minter.gotEng)
		}
		// prefix-never-token: the audit trail must never carry the raw token.
		if _, _, detail := lastAudit(t, fa); strings.Contains(detail, minter.token) {
			t.Errorf("audit detail must not carry the raw token %q: %q", minter.token, detail)
		}
	})
}

// TestDBConsoleContract_PrivilegedAuditsBothOutcomesInOwnTaxonomy pins a
// deliberate divergence rather than a bug: the privileged doors audit success and
// failure in their own ok/error + reason taxonomy (databases_admin_ops.go:446
// documents this — "This door keeps its own audit taxonomy... the leaf's returned
// hash-prefix is not used here"), NOT the tenant/CLI issuance constants
// (OutcomeIssued / OutcomeMintFail). This test records that boundary so a future
// "unify the taxonomy" change is a conscious edit here, and so the doors are
// proven to audit BOTH outcomes (the ADR-0099 invariant).
func TestDBConsoleContract_PrivilegedAuditsBothOutcomesInOwnTaxonomy(t *testing.T) {
	t.Run("phpmyadmin success audits ok", func(t *testing.T) {
		fa := &fakeDBAdmin{}
		h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
			Agent: stubAgent{}, DBAdmin: fa, SSO: &contractAdminMinter{token: "T"}, Log: slog.Default(),
		}}
		_, c := adminSSORequest("https://example.com")
		h.ssoPhpMyAdminAdmin(c)
		outcome, target, _ := lastAudit(t, fa)
		if outcome != "ok" || target != "phpmyadmin" {
			t.Errorf("audit outcome=%q target=%q, want ok/phpmyadmin", outcome, target)
		}
		if outcome == dbconsoleops.OutcomeIssued {
			t.Errorf("privileged door must keep its own taxonomy, not the issuance constant %q", dbconsoleops.OutcomeIssued)
		}
	})

	t.Run("phpmyadmin mint failure audits error, not the mint_fail constant", func(t *testing.T) {
		fa := &fakeDBAdmin{}
		h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
			Agent: stubAgent{}, DBAdmin: fa, SSO: &contractAdminMinter{err: errors.New("boom")}, Log: slog.Default(),
		}}
		w, c := adminSSORequest("https://example.com")
		h.ssoPhpMyAdminAdmin(c)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("code = %d, want 500 on mint failure", w.Code)
		}
		outcome, _, _ := lastAudit(t, fa)
		if outcome != "error" {
			t.Errorf("audit outcome = %q, want error (own taxonomy)", outcome)
		}
		if outcome == dbconsoleops.OutcomeMintFail {
			t.Errorf("privileged door must not emit the issuance constant %q", dbconsoleops.OutcomeMintFail)
		}
	})

	t.Run("adminer mint failure audits error", func(t *testing.T) {
		fa := &fakeDBAdmin{}
		h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
			DBAdmin: fa, AdminerSSO: &contractAdminerMinter{err: errors.New("boom")}, Log: slog.Default(),
		}}
		w, c := adminSSORequest("https://example.com")
		h.ssoAdminerAdmin(c)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("code = %d, want 500 on mint failure", w.Code)
		}
		outcome, target, _ := lastAudit(t, fa)
		if outcome != "error" || target != "adminer" {
			t.Errorf("audit outcome=%q target=%q, want error/adminer", outcome, target)
		}
	})
}

// TestDBConsoleContract_EveryAdapterDelegatesEncodingToLeaf pins the single-
// authority invariant across the adapters that have no mint/injection seam
// (tenant doors hold a concrete *sso.Service; the CLI is pinned in
// cmd/server/db_sso_contract_test.go). Each adapter must route scope/engine/
// redirect encoding through dbconsoleops rather than re-implementing it inline —
// that delegation is what makes "encoded identically across every adapter" true
// by construction. An adapter that inlines its own URL/engine handling drops the
// leaf reference and reddens its pin.
func TestDBConsoleContract_EveryAdapterDelegatesEncodingToLeaf(t *testing.T) {
	pins := []struct {
		file  string
		needs []string
	}{
		// Tenant phpMyAdmin: full issuance leaf (mint<->redirect paired).
		{"sso_phpmyadmin.go", []string{"dbconsoleops.IssuePhpMyAdminLogin"}},
		// Tenant Adminer: engine normalization + full issuance leaf.
		{"sso_adminer.go", []string{"dbconsoleops.NormalizeEngine", "dbconsoleops.IssueAdminerLogin"}},
		// Privileged doors: phpMyAdmin uses the full leaf; Adminer admin-all still
		// mints via MintAdminerToken and only calls AdminerRedirect (it does NOT
		// use IssueAdminerLogin) — a residual pairing inconsistency this contract
		// records as a finding for the AC1 unified-module follow-up. Its ENCODING
		// is still the leaf's (asserted behaviourally above), so we pin the
		// redirect builder it does use.
		{"databases_admin_ops.go", []string{"dbconsoleops.IssuePhpMyAdminLogin", "dbconsoleops.AdminerRedirect"}},
	}
	for _, p := range pins {
		src, err := os.ReadFile(p.file)
		if err != nil {
			t.Fatalf("read %s: %v", p.file, err)
		}
		for _, need := range p.needs {
			if !strings.Contains(string(src), need) {
				t.Errorf("%s must delegate encoding to %s — inlining it lets scope/engine drift from the other adapters (JAB-348 AC3/AC4)", p.file, need)
			}
		}
	}
}
