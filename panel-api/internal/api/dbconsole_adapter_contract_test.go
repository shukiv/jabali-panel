package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/config"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbconsoleops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
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
// COVERAGE SHAPE:
//   - Privileged doors take the mint surface as the dbconsoleops leaf
//     interfaces (PhpMyAdminMinter / AdminerMinter) and are driven
//     behaviourally with a recording fake minter
//     (TestDBConsoleContract_PrivilegedEncodingAnchoredToLeaf).
//   - Tenant doors now type their SSO/Adminer dependencies as the dbconsoleops
//     mint+shadow interfaces (PhpMyAdminConsole / ShadowService / AdminerConsole),
//     so a single fake drives them against the SAME matrix
//     (TestDBConsoleContract_TenantEncodingAnchoredToLeaf). The interface-vs-
//     concrete asymmetry that used to block a unified fake-minter matrix — and
//     forced the tenant doors onto source-delegation pins — is resolved (JAB-348
//     AC4).
//   - The CLI runs its issuance through dbSSOIssue, which takes the same
//     dbconsoleops console interfaces (dbSSODeps), so recording fakes drive it
//     through the same matrix in cmd/server/db_sso_contract_test.go
//     (TestDBSSOCLI_EncodingAnchoredToLeaf). The source pins there and in
//     TestDBConsoleContract_EveryAdapterDelegatesEncodingToLeaf remain as
//     belt-and-suspenders against inline re-implementation.

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

// TestDBConsoleContract_EveryAdapterDelegatesEncodingToLeaf is the source-level
// belt-and-suspenders for the single-authority invariant. The tenant doors are
// now also covered behaviourally (TestDBConsoleContract_TenantEncodingAnchoredToLeaf),
// and the CLI is covered both ways in cmd/server/db_sso_contract_test.go. Each
// adapter must route scope/engine/
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
		// Privileged doors: both admin-all doors now pair mint<->redirect through
		// the full issuance leaf (the Adminer door was unified onto
		// IssueAdminerLogin, resolving the earlier residual inconsistency where it
		// minted via MintAdminerToken and called AdminerRedirect separately).
		{"databases_admin_ops.go", []string{"dbconsoleops.IssuePhpMyAdminLogin", "dbconsoleops.IssueAdminerLogin"}},
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

// --- tenant-door fakes (JAB-348 AC4) ---
//
// The tenant handlers now type their SSO/Adminer dependencies as the dbconsoleops
// mint+shadow interfaces (PhpMyAdminConsole / ShadowService / AdminerConsole), so
// a single recording fake can drive them through the same behavioural matrix the
// privileged doors already run — the interface-vs-concrete asymmetry that used to
// force the tenant doors onto source-delegation pins is gone.

// contractTenantPMA satisfies dbconsoleops.PhpMyAdminConsole (EnsureShadow + MintToken).
type contractTenantPMA struct {
	token     string
	shadowErr error
	gotDBID   string
	gotDB     string
}

func (m *contractTenantPMA) EnsureShadow(context.Context, string) error { return m.shadowErr }
func (m *contractTenantPMA) MintToken(_ context.Context, _, databaseID, dbName string) (string, error) {
	m.gotDBID, m.gotDB = databaseID, dbName
	return m.token, nil
}

// contractTenantAdminerBase satisfies dbconsoleops.ShadowService — the Adminer
// door's mariadb shadow dependency (the SSO field).
type contractTenantAdminerBase struct {
	called bool
	err    error
}

func (m *contractTenantAdminerBase) EnsureShadow(context.Context, string) error {
	m.called = true
	return m.err
}

// contractTenantAdminer satisfies dbconsoleops.AdminerConsole (EnsurePgShadow +
// MintAdminerToken) — the Adminer door's postgres shadow + Adminer token mint.
type contractTenantAdminer struct {
	token       string
	pgShadowErr error
	pgCalled    bool
	gotDBID     string
	gotEng      string
}

func (m *contractTenantAdminer) EnsurePgShadow(context.Context, string) error {
	m.pgCalled = true
	return m.pgShadowErr
}
func (m *contractTenantAdminer) MintAdminerToken(_ context.Context, _, databaseID, engine string) (string, error) {
	m.gotDBID, m.gotEng = databaseID, engine
	return m.token, nil
}

func tenantSSOContext(dbID string) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := []byte(`{"database_id":"` + dbID + `"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/sso", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	// Same host as the request → passes the adapter-local same-origin gate.
	c.Request.Header.Set("Origin", "http://example.com")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "user1"})
	return w, c
}

func bufLogger() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	return &buf, slog.New(slog.NewJSONHandler(&buf, nil))
}

// TestDBConsoleContract_TenantEncodingAnchoredToLeaf drives the tenant phpMyAdmin
// and Adminer doors with recording fakes and asserts — for the first time
// behaviourally, not by source delegation — that each tenant redirect is
// byte-identical to the leaf builder fed the door's own base URL and minted
// token, that the engine is normalized identically, and that the shadow path is
// actually taken. This is the tenant half of the AC4 "same request matrix across
// tenant, privileged, and CLI adapters" invariant (the CLI half is
// TestDBSSOCLI_EncodingAnchoredToLeaf in cmd/server/db_sso_contract_test.go).
func TestDBConsoleContract_TenantEncodingAnchoredToLeaf(t *testing.T) {
	const pmaBase = "https://pma.example.com"
	const admBase = "https://adm.example.com"

	t.Run("phpmyadmin tenant -> PhpMyAdminRedirect(token, db=name)", func(t *testing.T) {
		minter := &contractTenantPMA{token: "PMA-TOK"}
		dbs := &mockDatabaseRepo{databases: []models.Database{{ID: "db1", Name: "testdb", UserID: "user1", Engine: "mariadb"}}}
		buf, log := bufLogger()
		h := &ssoPhpMyAdminHandler{cfg: SSOPhpMyAdminHandlerConfig{
			Databases: dbs, SSO: minter, Log: log,
			SSOConfig: config.SSOConfig{PhpMyAdminBaseURL: pmaBase},
		}}
		w, c := tenantSSOContext("db1")
		h.issueSSOToken(c)

		if w.Code != http.StatusOK {
			t.Fatalf("code=%d want 200: %s", w.Code, w.Body.String())
		}
		var resp ssoPhpMyAdminResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response: %v", err)
		}
		want := dbconsoleops.PhpMyAdminRedirect(pmaBase, minter.token, "testdb")
		if resp.RedirectURL != want {
			t.Errorf("redirect=%q, want leaf output %q — tenant door must build its URL through dbconsoleops", resp.RedirectURL, want)
		}
		if minter.gotDBID != "db1" || minter.gotDB != "testdb" {
			t.Errorf("minted dbID=%q db=%q, want db1/testdb", minter.gotDBID, minter.gotDB)
		}
		if logs := buf.String(); !strings.Contains(logs, dbconsoleops.OutcomeIssued) {
			t.Errorf("issuance must audit %q; logs=%s", dbconsoleops.OutcomeIssued, logs)
		}
		if strings.Contains(buf.String(), minter.token) {
			t.Errorf("audit must not carry the raw token %q: %s", minter.token, buf.String())
		}
	})

	// Proves the door actually calls EnsureShadow (not just the mint): a shadow
	// failure must surface as OutcomeEnsureShadowFail + 500, matching the CLI/Adminer.
	t.Run("phpmyadmin shadow-ensure failure audits ensure_shadow_fail", func(t *testing.T) {
		minter := &contractTenantPMA{token: "PMA-TOK", shadowErr: errors.New("no linux user")}
		dbs := &mockDatabaseRepo{databases: []models.Database{{ID: "db1", Name: "testdb", UserID: "user1", Engine: "mariadb"}}}
		buf, log := bufLogger()
		h := &ssoPhpMyAdminHandler{cfg: SSOPhpMyAdminHandlerConfig{
			Databases: dbs, SSO: minter, Log: log,
			SSOConfig: config.SSOConfig{PhpMyAdminBaseURL: pmaBase},
		}}
		w, c := tenantSSOContext("db1")
		h.issueSSOToken(c)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500 on shadow-ensure failure", w.Code)
		}
		if logs := buf.String(); !strings.Contains(logs, dbconsoleops.OutcomeEnsureShadowFail) {
			t.Errorf("shadow-ensure failure must audit %q; logs=%s", dbconsoleops.OutcomeEnsureShadowFail, logs)
		}
	})

	t.Run("adminer tenant postgres -> AdminerRedirect(token, db, engine=postgres)", func(t *testing.T) {
		base := &contractTenantAdminerBase{}
		adm := &contractTenantAdminer{token: "ADM-TOK"}
		dbs := &mockDatabaseRepo{databases: []models.Database{{ID: "db2", Name: "pgdb", UserID: "user1", Engine: "postgres"}}}
		buf, log := bufLogger()
		h := &ssoAdminerHandler{cfg: SSOAdminerHandlerConfig{
			Databases: dbs, SSO: base, Adminer: adm, Log: log,
			SSOConfig: config.SSOConfig{AdminerBaseURL: admBase},
		}}
		w, c := tenantSSOContext("db2")
		h.issueSSOToken(c)

		if w.Code != http.StatusOK {
			t.Fatalf("code=%d want 200: %s", w.Code, w.Body.String())
		}
		var resp ssoAdminerResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response: %v", err)
		}
		want := dbconsoleops.AdminerRedirect(admBase, adm.token, "pgdb", "postgres")
		if resp.RedirectURL != want {
			t.Errorf("redirect=%q, want leaf output %q", resp.RedirectURL, want)
		}
		if adm.gotDBID != "db2" || adm.gotEng != "postgres" {
			t.Errorf("minted dbID=%q engine=%q, want db2/postgres", adm.gotDBID, adm.gotEng)
		}
		if !adm.pgCalled {
			t.Error("postgres engine must provision the postgres shadow (EnsurePgShadow)")
		}
		if strings.Contains(buf.String(), adm.token) {
			t.Errorf("audit must not carry the raw token %q: %s", adm.token, buf.String())
		}
	})

	// Engine "" normalizes to mariadb and routes shadow provisioning through the
	// base ShadowService — the engine-dispatch invariant, proven on the tenant door.
	t.Run("adminer tenant mariadb (engine='') -> normalized engine + base shadow", func(t *testing.T) {
		base := &contractTenantAdminerBase{}
		adm := &contractTenantAdminer{token: "ADM-TOK"}
		dbs := &mockDatabaseRepo{databases: []models.Database{{ID: "db3", Name: "mydb", UserID: "user1", Engine: ""}}}
		buf, log := bufLogger()
		h := &ssoAdminerHandler{cfg: SSOAdminerHandlerConfig{
			Databases: dbs, SSO: base, Adminer: adm, Log: log,
			SSOConfig: config.SSOConfig{AdminerBaseURL: admBase},
		}}
		w, c := tenantSSOContext("db3")
		h.issueSSOToken(c)

		if w.Code != http.StatusOK {
			t.Fatalf("code=%d want 200: %s", w.Code, w.Body.String())
		}
		var resp ssoAdminerResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad response: %v", err)
		}
		want := dbconsoleops.AdminerRedirect(admBase, adm.token, "mydb", "mariadb")
		if resp.RedirectURL != want {
			t.Errorf("redirect=%q, want leaf output %q — empty engine must normalize to mariadb", resp.RedirectURL, want)
		}
		if adm.gotEng != "mariadb" {
			t.Errorf("minted engine=%q, want normalized mariadb", adm.gotEng)
		}
		if !base.called {
			t.Error("mariadb engine must provision the base shadow (EnsureShadow)")
		}
		if logs := buf.String(); !strings.Contains(logs, dbconsoleops.OutcomeIssued) {
			t.Errorf("issuance must audit %q; logs=%s", dbconsoleops.OutcomeIssued, logs)
		}
	})

	// Adminer shadow-fail rows — symmetry with the phpMyAdmin shadow-fail row.
	// A shadow-provisioning failure (not an invalid engine) must surface as
	// OutcomeEnsureShadowFail + 500 on BOTH engine paths, proving the door takes
	// the engine-correct shadow path and maps its failure into the issuance
	// taxonomy rather than an authorization denial.
	t.Run("adminer postgres shadow-ensure failure audits ensure_shadow_fail", func(t *testing.T) {
		base := &contractTenantAdminerBase{}
		adm := &contractTenantAdminer{token: "ADM-TOK", pgShadowErr: errors.New("boom")}
		dbs := &mockDatabaseRepo{databases: []models.Database{{ID: "db2", Name: "pgdb", UserID: "user1", Engine: "postgres"}}}
		buf, log := bufLogger()
		h := &ssoAdminerHandler{cfg: SSOAdminerHandlerConfig{
			Databases: dbs, SSO: base, Adminer: adm, Log: log,
			SSOConfig: config.SSOConfig{AdminerBaseURL: admBase},
		}}
		w, c := tenantSSOContext("db2")
		h.issueSSOToken(c)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500 on postgres shadow-ensure failure", w.Code)
		}
		if !adm.pgCalled {
			t.Error("postgres row must reach EnsurePgShadow")
		}
		if logs := buf.String(); !strings.Contains(logs, dbconsoleops.OutcomeEnsureShadowFail) {
			t.Errorf("postgres shadow-ensure failure must audit %q; logs=%s", dbconsoleops.OutcomeEnsureShadowFail, logs)
		}
	})

	t.Run("adminer mariadb shadow-ensure failure audits ensure_shadow_fail", func(t *testing.T) {
		base := &contractTenantAdminerBase{err: errors.New("boom")}
		adm := &contractTenantAdminer{token: "ADM-TOK"}
		dbs := &mockDatabaseRepo{databases: []models.Database{{ID: "db3", Name: "mydb", UserID: "user1", Engine: "mariadb"}}}
		buf, log := bufLogger()
		h := &ssoAdminerHandler{cfg: SSOAdminerHandlerConfig{
			Databases: dbs, SSO: base, Adminer: adm, Log: log,
			SSOConfig: config.SSOConfig{AdminerBaseURL: admBase},
		}}
		w, c := tenantSSOContext("db3")
		h.issueSSOToken(c)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("code=%d want 500 on mariadb shadow-ensure failure", w.Code)
		}
		if !base.called {
			t.Error("mariadb row must reach the base EnsureShadow")
		}
		if logs := buf.String(); !strings.Contains(logs, dbconsoleops.OutcomeEnsureShadowFail) {
			t.Errorf("mariadb shadow-ensure failure must audit %q; logs=%s", dbconsoleops.OutcomeEnsureShadowFail, logs)
		}
	})
}
