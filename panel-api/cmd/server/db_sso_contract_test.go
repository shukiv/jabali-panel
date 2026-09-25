package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbconsoleops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// JAB-348 AC4 — CLI adapter half of the DB-Console SSO contract.
//
// The operator CLI (`jabali db sso`) runs its issuance through dbSSOIssue, which
// takes its database lookup and the phpMyAdmin/Adminer consoles as the
// dbconsoleops interfaces (dbSSODeps). TestDBSSOCLI_EncodingAnchoredToLeaf drives
// it with recording fakes through the same request matrix the tenant and
// privileged doors run in internal/api/dbconsole_adapter_contract_test.go.
//
// The source pin below stays as belt-and-suspenders, matching the api side: the
// CLI must issue through dbconsoleops.Issue — the one entrypoint that owns the
// console choice, shadow selection, mint, redirect and hash-prefix — and audit
// in the canonical taxonomy (JAB-348 AC1/AC2/AC5). If the CLI grows its own
// shadow dispatch, minter call or URL builder, the pin reddens. It may still
// normalize the engine: the --engine guard compares against it.
func TestDBSSOCLI_DelegatesIssuanceToLeaf(t *testing.T) {
	s := stripLineComments(readGoSource(t, "db_sso_cmd.go"))

	needs := []struct {
		token string
		why   string
	}{
		{"dbconsoleops.Issue(", "issuance must run through the DB Console SSO entrypoint so the CLI encodes scope identically to the REST doors (AC1/AC3)"},
		{"dbconsoleops.OutcomeIssued", "a successful CLI issuance must audit the canonical issued outcome, matching the tenant doors (AC5)"},
		{"dbconsoleops.OutcomeMintFail", "a CLI mint failure must audit the canonical mint_fail outcome"},
		{"dbconsoleops.OutcomeEnsureShadowFail", "a CLI shadow-provision failure must audit the canonical ensure_shadow_fail outcome"},
	}
	for _, n := range needs {
		if !strings.Contains(s, n.token) {
			t.Errorf("db_sso_cmd.go must reference %s — %s", n.token, n.why)
		}
	}
	for _, banned := range []string{
		"dbconsoleops.IssuePhpMyAdminLogin", "dbconsoleops.IssueAdminerLogin",
		"dbconsoleops.EnsureShadowForEngine", "dbconsoleops.PhpMyAdminRedirect",
		"dbconsoleops.AdminerRedirect", "dbconsoleops.TokenAuditPrefix",
		".EnsureShadow(", ".EnsurePgShadow(", ".MintToken(", ".MintAdminerToken(",
	} {
		if strings.Contains(s, banned) {
			t.Errorf("db_sso_cmd.go calls %s — issuance steps belong to dbconsoleops.Issue (JAB-348 AC2)", banned)
		}
	}
}

// --- CLI fakes (JAB-348 AC4) ---

// cliSSODatabases serves one database row by ID; any other ID is not found.
type cliSSODatabases struct {
	repository.DatabaseRepository
	db *models.Database
}

func (f cliSSODatabases) FindByID(_ context.Context, id string) (*models.Database, error) {
	if f.db == nil || f.db.ID != id {
		return nil, repository.ErrNotFound
	}
	return f.db, nil
}

// cliPMA satisfies dbconsoleops.PhpMyAdminConsole (EnsureShadow + MintToken).
type cliPMA struct {
	token, gotDBID, gotDB string
	shadowErr, mintErr    error
	shadowCalled, minted  bool
}

func (m *cliPMA) EnsureShadow(context.Context, string) error {
	m.shadowCalled = true
	return m.shadowErr
}

func (m *cliPMA) MintToken(_ context.Context, _, databaseID, dbName string) (string, error) {
	m.minted = true
	m.gotDBID, m.gotDB = databaseID, dbName
	if m.mintErr != nil {
		return "", m.mintErr
	}
	return m.token, nil
}

// cliAdminer satisfies dbconsoleops.AdminerConsole (EnsurePgShadow + MintAdminerToken).
type cliAdminer struct {
	token, gotDBID, gotEng string
	pgShadowErr, mintErr   error
	pgCalled, minted       bool
}

func (m *cliAdminer) EnsurePgShadow(context.Context, string) error {
	m.pgCalled = true
	return m.pgShadowErr
}

func (m *cliAdminer) MintAdminerToken(_ context.Context, _, databaseID, engine string) (string, error) {
	m.minted = true
	m.gotDBID, m.gotEng = databaseID, engine
	if m.mintErr != nil {
		return "", m.mintErr
	}
	return m.token, nil
}

// TestDBSSOCLI_EncodingAnchoredToLeaf drives the CLI issuance behaviourally
// through the DB-Console SSO request matrix: each login URL must be
// byte-identical to the leaf builder fed the CLI's own base URL and minted token,
// the engine must normalize identically (empty -> mariadb), the engine-correct
// shadow path must be taken, and every outcome must audit exactly once without
// token material. The CLI-only guards (--engine mismatch, unknown database,
// unknown engine) run through the same harness.
func TestDBSSOCLI_EncodingAnchoredToLeaf(t *testing.T) {
	const (
		pmaBase = "https://pma.example.com"
		admBase = "https://adm.example.com"
		pmaTok  = "PMA-TOK-cli"
		admTok  = "ADM-TOK-cli"
		dbID    = "db_01"
		dbName  = "shopdb"
	)

	rows := []struct {
		name        string
		engine      string // the database row's engine
		missing     bool   // the database lookup fails
		engineFlag  string
		shadowFail  bool
		mintFail    bool
		wantOutcome string
		wantURL     string // empty: the call must fail
		wantEngine  string
		wantBase    bool   // base EnsureShadow called
		wantPg      bool   // EnsurePgShadow called
		wantMint    string // "pma", "adm", or "" for no mint
		wantTok     string // token whose audit prefix the issued line carries
	}{
		{name: "mariadb -> phpMyAdmin", engine: "mariadb",
			wantOutcome: dbconsoleops.OutcomeIssued, wantURL: dbconsoleops.PhpMyAdminRedirect(pmaBase, pmaTok, dbName),
			wantEngine: "mariadb", wantBase: true, wantMint: "pma", wantTok: pmaTok},
		{name: "engine '' normalizes to mariadb -> phpMyAdmin", engine: "",
			wantOutcome: dbconsoleops.OutcomeIssued, wantURL: dbconsoleops.PhpMyAdminRedirect(pmaBase, pmaTok, dbName),
			wantEngine: "mariadb", wantBase: true, wantMint: "pma", wantTok: pmaTok},
		{name: "postgres -> Adminer", engine: "postgres",
			wantOutcome: dbconsoleops.OutcomeIssued, wantURL: dbconsoleops.AdminerRedirect(admBase, admTok, dbName, "postgres"),
			wantEngine: "postgres", wantPg: true, wantMint: "adm", wantTok: admTok},
		{name: "mariadb shadow failure", engine: "mariadb", shadowFail: true,
			wantOutcome: dbconsoleops.OutcomeEnsureShadowFail, wantBase: true},
		{name: "postgres shadow failure", engine: "postgres", shadowFail: true,
			wantOutcome: dbconsoleops.OutcomeEnsureShadowFail, wantPg: true},
		{name: "mariadb mint failure", engine: "mariadb", mintFail: true,
			wantOutcome: dbconsoleops.OutcomeMintFail, wantBase: true, wantMint: "pma"},
		{name: "postgres mint failure", engine: "postgres", mintFail: true,
			wantOutcome: dbconsoleops.OutcomeMintFail, wantPg: true, wantMint: "adm"},
		{name: "--engine guard mismatch", engine: "mariadb", engineFlag: "postgres",
			wantOutcome: "engine_mismatch"},
		{name: "database not found", missing: true,
			wantOutcome: "db_not_found"},
		{name: "unknown engine", engine: "sqlite",
			wantOutcome: "unknown_engine"},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			pma := &cliPMA{token: pmaTok}
			adm := &cliAdminer{token: admTok}
			if r.shadowFail {
				pma.shadowErr = errors.New("no linux user")
				adm.pgShadowErr = errors.New("no pg role")
			}
			if r.mintFail {
				pma.mintErr = errors.New("token store down")
				adm.mintErr = errors.New("token store down")
			}
			dbs := cliSSODatabases{}
			if !r.missing {
				dbs.db = &models.Database{ID: dbID, Name: dbName, UserID: "user_01", Engine: r.engine}
			}
			var buf bytes.Buffer
			res, err := dbSSOIssue(context.Background(), dbSSODeps{
				databases:         dbs,
				phpMyAdmin:        pma,
				adminer:           adm,
				phpMyAdminBaseURL: pmaBase,
				adminerBaseURL:    admBase,
				audit:             slog.New(slog.NewJSONHandler(&buf, nil)),
			}, dbID, r.engineFlag)

			if r.wantURL != "" {
				if err != nil {
					t.Fatalf("dbSSOIssue: %v", err)
				}
				if res.loginURL != r.wantURL {
					t.Errorf("login URL = %q, want leaf output %q — the CLI must build its URL through dbconsoleops", res.loginURL, r.wantURL)
				}
				if res.engine != r.wantEngine || res.database != dbName {
					t.Errorf("result engine=%q database=%q, want %q/%q", res.engine, res.database, r.wantEngine, dbName)
				}
			} else {
				if err == nil {
					t.Fatalf("dbSSOIssue succeeded with %+v, want an error", res)
				}
				if res != (dbSSOResult{}) {
					t.Errorf("failed call returned a result %+v", res)
				}
			}

			if pma.shadowCalled != r.wantBase {
				t.Errorf("base EnsureShadow called=%v, want %v", pma.shadowCalled, r.wantBase)
			}
			if adm.pgCalled != r.wantPg {
				t.Errorf("EnsurePgShadow called=%v, want %v", adm.pgCalled, r.wantPg)
			}
			if pma.minted != (r.wantMint == "pma") || adm.minted != (r.wantMint == "adm") {
				t.Errorf("minted pma=%v adm=%v, want %q", pma.minted, adm.minted, r.wantMint)
			}
			if pma.minted && (pma.gotDBID != dbID || pma.gotDB != dbName) {
				t.Errorf("phpMyAdmin minted dbID=%q db=%q, want %s/%s", pma.gotDBID, pma.gotDB, dbID, dbName)
			}
			if adm.minted && (adm.gotDBID != dbID || adm.gotEng != "postgres") {
				t.Errorf("Adminer minted dbID=%q engine=%q, want %s/postgres", adm.gotDBID, adm.gotEng, dbID)
			}

			out := buf.String()
			if strings.Contains(out, pmaTok) || strings.Contains(out, admTok) {
				t.Fatalf("audit carried raw token material: %s", out)
			}
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) != 1 {
				t.Fatalf("want exactly one audit line, got %d: %s", len(lines), out)
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
				t.Fatalf("audit line is not JSON: %v (%s)", err, out)
			}
			if rec["msg"] != "sso_cli" || rec["outcome"] != r.wantOutcome {
				t.Errorf("audit msg=%v outcome=%v, want sso_cli/%s", rec["msg"], rec["outcome"], r.wantOutcome)
			}
			wantPrefix := ""
			if r.wantTok != "" {
				wantPrefix = dbconsoleops.TokenAuditPrefix(r.wantTok)
			}
			if rec["token_hash_prefix"] != wantPrefix {
				t.Errorf("audit token_hash_prefix=%v, want %q", rec["token_hash_prefix"], wantPrefix)
			}
		})
	}
}
